"""Agent-owned friendship, direct messages and durable cross-surface read state.

The platform transports these signed-sender envelopes; it never owns contacts or
owner decisions. An accepted local queue write is not a peer acceptance.
"""
import hashlib
import json
import secrets
from concurrent.futures import ThreadPoolExecutor

from .identity import validate_urn

SOCIAL_PROTOCOL = "agent-comm-contacts/v1"
SOCIAL_KINDS = {"contact.request", "contact.response"}


def key(*values):
    return hashlib.sha256(json.dumps(values, ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()


class SocialMixin:
    def register_owner(self, owner_session):
        """Bind unsolicited local-agent mail to its trusted host profile once."""
        self._owner(owner_session)
        owner = self._principal(owner_session)
        with self._transaction():
            profile = self._get("local_profile", "owner")
            if not profile:
                self._put("local_profile", "owner", {"owner_id": owner})
                for message in self._all("inbound"):
                    if message.get("kind") in SOCIAL_KINDS:
                        self._ingest_social(message)
            return (profile or {"owner_id": owner})["owner_id"]

    def _mail_owner(self):
        return (self._get("local_profile", "owner") or {}).get("owner_id")

    def _queue_social(self, message, owner):
        old = self._get("social_outbox", message["message_id"])
        if old:
            if old["message"] != message or old["owner_id"] != owner:
                raise ValueError("Outgoing message ID conflicts with its durable contents")
            return old
        record = {"message_id": message["message_id"], "owner_id": owner, "message": message,
                  "status": "queued", "created_at": self.clock(), "updated_at": self.clock()}
        self._put("social_outbox", message["message_id"], record)
        return record

    def _request_contact(self, contact):
        if not self.local_urn:
            return None  # Offline/local-only hosts can still maintain identity mappings.
        if contact["urn"] == self.local_urn:
            raise ValueError("Cannot send a friend request to the local agent")
        owner = contact["owner_id"]
        connection = self._get("connection", key(owner, contact["urn"]))
        if connection:
            return self._get("contact_request", connection["request_id"])
        existing = [r for r in self._all("contact_request") if r.get("owner_id") == owner
                    and r["peer_urn"] == contact["urn"] and r["direction"] == "outgoing"
                    and r["status"] in {"pending", "accepted"}]
        if existing:
            return existing[-1]
        request_id = "friend-" + secrets.token_hex(20)
        packet = {"protocol": SOCIAL_PROTOCOL, "type": "request", "request_id": request_id}
        request = {"request_id": request_id, "owner_id": owner, "direction": "outgoing",
                   "peer_urn": contact["urn"], "contact_id": contact["contact_id"], "status": "pending",
                   "created_at": self.clock(), "updated_at": self.clock()}
        self._put("contact_request", request_id, request)
        self._queue_social({"message_id": request_id, "recipient_urn": contact["urn"],
            "kind": "contact.request", "conversation_id": request_id,
            "text": json.dumps(packet, ensure_ascii=False, sort_keys=True, separators=(",", ":"))}, owner)
        return request

    def _prepare_existing_contact(self, contact, owner_session):
        status = self._contact_status(contact)
        if not self.local_urn or status == "connected":
            return {"decision": "allow", "contact": contact,
                    "status": "already_connected" if status == "connected" else "already_confirmed"}
        requests = [r for r in self._all("contact_request") if r["owner_id"] == contact["owner_id"]
                    and r["peer_urn"] == contact["urn"] and r["direction"] == "outgoing"]
        if status == "pending":
            request = next(r for r in requests if r["status"] == "pending")
            return {"decision": "allow", "contact": contact, "status": "already_requested", "request_id": request["request_id"]}
        # Upgraded local mappings and rejected requests need a fresh, explicit
        # owner decision before they result in a new network request.
        payload = {"contact": contact, "attempt": len(requests) + 1}
        return {"decision": "ask", **self._approval("friend_request", contact["contact_id"], owner_session,
            payload, f"向 {contact['urn']} 发送好友请求？对方接受后建立连接。", self.clock() + 900)}

    def _contact_status(self, contact):
        owner, urn = contact["owner_id"], contact["urn"]
        if self._get("connection", key(owner, urn)):
            return "connected"
        requests = [r for r in self._all("contact_request") if r.get("owner_id") == owner
                    and r["peer_urn"] == urn and r["direction"] == "outgoing"]
        if requests:
            latest = max(requests, key=lambda r: r["created_at"])
            return {"accepted": "connected", "rejected": "rejected"}.get(latest["status"], "pending")
        return "unverified"

    def _contacts_view(self, owner_session):
        result = []
        for contact in self._all("contact"):
            if not self._belongs(contact, owner_session):
                continue
            status = self._contact_status(contact)
            presence = self._get("contact_presence", key(contact["owner_id"], contact["urn"])) or {}
            if status != "connected" or presence.get("cache_expires_at", 0) <= self.clock() or (presence.get("status") == "online" and (presence.get("expires_at") or 0) <= self.clock()):
                presence = {"status": "unknown", "last_seen": presence.get("last_seen"), "expires_at": None}
            result.append({**contact, "connection_status": status,
                           "presence": {k: presence.get(k) for k in ("status", "last_seen", "expires_at")}})
        return result

    def refresh_presence(self, transport):
        if not callable(getattr(transport, "presence", None)):
            return
        with self._lock:
            contacts = [c for c in self._all("contact") if self._contact_status(c) == "connected"]
        # Bound network work per pump. Refresh the oldest observations first;
        # many offline contacts cannot stall owner mail or UI RPC indefinitely.
        with self._lock:
            contacts.sort(key=lambda c: (self._get("contact_presence", key(c["owner_id"], c["urn"])) or {}).get("observed_at", 0))
        def observe(contact):
            try:
                presence = transport.presence(contact["urn"])
                if not isinstance(presence, dict) or presence.get("urn") != contact["urn"] or presence.get("status") not in {"online", "offline", "unknown"}:
                    raise ValueError("Invalid helper presence response")
                for field in ("last_seen", "expires_at"):
                    if presence.get(field) is not None and (type(presence[field]) not in {int, float} or presence[field] < 0):
                        raise ValueError("Invalid presence timestamp")
            except (OSError, ValueError, TypeError):
                presence = {"status": "unknown", "last_seen": None, "expires_at": None}
            # Signature lease expiry and observation cache expiry are distinct:
            # an expired signature is a fresh offline observation, not unknown.
            return contact, {**presence, "observed_at": self.clock(), "cache_expires_at": self.clock() + 60}
        with ThreadPoolExecutor(max_workers=4) as executor:
            for contact, presence in executor.map(observe, contacts[:16]):
                with self._transaction():
                    self._put("contact_presence", key(contact["owner_id"], contact["urn"]), presence)

    def _contact_requests(self, owner_session):
        return [{k: v for k, v in r.items() if k != "owner_id"} for r in self._all("contact_request")
                if self._belongs(r, owner_session)]

    def contact_requests(self, owner_session):
        self._owner(owner_session)
        with self._transaction():
            return {"contact_requests": self._contact_requests(owner_session)}

    def _friend_attention(self, request):
        if request["direction"] == "incoming":
            self._attention_put(request["owner_id"], "contact_request", request["request_id"],
                kind="friend_request_received", subject_id=request["request_id"], target_kind="contact",
                title="收到好友请求", summary=request["peer_urn"],
                state="open" if request["status"] == "pending" else "resolved",
                source_revision=request["status"], created_at=request["created_at"])

    def _ingest_social(self, message):
        if message.get("kind") not in SOCIAL_KINDS:
            return None
        from .store import identifier
        try:
            packet = json.loads(message["text"])
        except (ValueError, TypeError, RecursionError) as exc:
            raise ValueError("Invalid contact protocol packet") from exc
        is_request = message["kind"] == "contact.request"
        fields = {"protocol", "type", "request_id"} | (set() if is_request else {"decision"})
        if not isinstance(packet, dict) or set(packet) != fields or packet.get("protocol") != SOCIAL_PROTOCOL or packet.get("type") != ("request" if is_request else "response"):
            raise ValueError("Invalid contact protocol fields")
        request_id = identifier(packet["request_id"], "request_id")
        if message.get("conversation_id") != request_id:
            raise ValueError("Contact request correlation mismatch")
        sender = message["sender_urn"]
        if is_request:
            if message["message_id"] != request_id or sender == self.local_urn:
                raise ValueError("Invalid contact request sender or identifier")
            old = self._get("contact_request", request_id)
            if old:
                if old["direction"] != "incoming" or old["peer_urn"] != sender:
                    raise ValueError("Contact request ID conflicts with another sender")
                return old
            owner = self._mail_owner()
            if not owner:
                return None  # Retained inbox is backfilled when the local host registers.
            request = {"request_id": request_id, "owner_id": owner, "direction": "incoming",
                       "peer_urn": sender, "status": "pending", "created_at": self.clock(), "updated_at": self.clock()}
            self._put("contact_request", request_id, request)
            self._friend_attention(request)
            return request
        if packet["decision"] not in {"accept", "reject"}:
            raise ValueError("Invalid contact response decision")
        request = self._get("contact_request", request_id)
        if not request or request["direction"] != "outgoing" or request["peer_urn"] != sender or message.get("in_reply_to") != request_id:
            raise ValueError("Contact response does not match an outgoing request")
        status = "accepted" if packet["decision"] == "accept" else "rejected"
        if request["status"] != "pending" and request["status"] != status:
            raise ValueError("Contact request already has a different final response")
        request.update(status=status, updated_at=self.clock())
        self._put("contact_request", request_id, request)
        if status == "accepted":
            self._put("connection", key(request["owner_id"], sender), {"owner_id": request["owner_id"],
                "peer_urn": sender, "request_id": request_id, "connected_at": self.clock()})
        self._attention_put(request["owner_id"], "contact_response", request_id,
            kind="friend_request_accepted" if status == "accepted" else "friend_request_rejected",
            subject_id=message["message_id"], target_kind="inbox", title="好友请求已接受" if status == "accepted" else "好友请求已拒绝",
            summary=sender, source_revision=status)
        return request

    def _response_values(self, params, owner_session):
        from .store import identifier
        request_id = identifier(params["request_id"], "request_id")
        request = self._get("contact_request", request_id)
        if not self._belongs(request, owner_session) or request["direction"] != "incoming":
            raise ValueError("No incoming friend request for this owner")
        decision = params["decision"]
        if decision not in {"accept", "reject"}:
            raise ValueError("Contact decision must be accept or reject")
        existing = next((c for c in self._all("contact") if self._belongs(c, owner_session) and c["urn"] == request["peer_urn"]), None)
        contact = None
        if decision == "accept":
            contact = existing or self._contact_value(params.get("contact_id", "peer-" + key(request["peer_urn"])[:20]),
                params.get("aliases", [request["peer_urn"].rsplit(":", 1)[-1][:100]]), request["peer_urn"], owner_session)
            old = self._get("contact", contact["contact_id"])
            if old and old != contact:
                raise ValueError("This contact ID already belongs to another identity")
        return request, decision, contact

    def _respond_contact(self, params, owner_session):
        request, decision, contact = self._response_values(params, owner_session)
        status = "accepted" if decision == "accept" else "rejected"
        if request["status"] != "pending":
            if request["status"] != status:
                raise ValueError("Friend request is already decided")
            return {"request_id": request["request_id"], "status": status}
        if contact:
            self._put("contact", contact["contact_id"], contact)
            self._put("connection", key(request["owner_id"], request["peer_urn"]), {
                "owner_id": request["owner_id"], "peer_urn": request["peer_urn"],
                "request_id": request["request_id"], "connected_at": self.clock()})
            request["contact_id"] = contact["contact_id"]
        request.update(status=status, updated_at=self.clock())
        self._put("contact_request", request["request_id"], request)
        packet = {"protocol": SOCIAL_PROTOCOL, "type": "response", "request_id": request["request_id"], "decision": decision}
        self._queue_social({"message_id": "friend-response-" + key(request["request_id"], self.local_urn)[:40],
            "recipient_urn": request["peer_urn"], "kind": "contact.response", "conversation_id": request["request_id"],
            "in_reply_to": request["request_id"], "text": json.dumps(packet, sort_keys=True, separators=(",", ":"))}, request["owner_id"])
        self._friend_attention(request)
        self._mark_read(request["request_id"], owner_session)
        return {"request_id": request["request_id"], "status": status}

    def prepare_contact_response(self, params, owner_session):
        with self._transaction():
            request, decision, contact = self._response_values(params, owner_session)
            if request["status"] != "pending":
                return {"request_id": request["request_id"], "status": request["status"]}
            payload = {"request_id": request["request_id"], "decision": decision,
                       **({"contact_id": contact["contact_id"], "aliases": contact["aliases"]} if contact else {})}
            return {"decision": "ask", **self._approval("contact_response", request["request_id"], owner_session,
                payload, f"{'接受' if decision == 'accept' else '拒绝'}来自 {request['peer_urn']} 的好友请求？", self.clock() + 900)}

    def _message_values(self, params, owner_session):
        from .store import identifier
        urn = validate_urn(params["recipient_urn"])
        text = params["text"]
        if not isinstance(text, str) or not text.strip() or len(text.encode()) > 24000:
            raise ValueError("Message text must contain 1–24000 UTF-8 bytes")
        if not any(c["urn"] == urn and self._belongs(c, owner_session) for c in self._all("contact")):
            raise ValueError("Confirm this recipient as a local contact before sending a message")
        return {"message_id": identifier(params.get("message_id", "message-" + secrets.token_hex(20)), "message_id"),
                "recipient_urn": urn, "kind": "chat.message", "text": text}

    def _send_message(self, params, owner_session):
        message = self._message_values(params, owner_session)
        record = self._queue_social(message, self._principal(owner_session))
        return {"message_id": record["message_id"], "status": record["status"]}

    def prepare_message(self, params, owner_session):
        with self._transaction():
            message = self._message_values(params, owner_session)
            return {"decision": "ask", **self._approval("direct_message", message["message_id"], owner_session,
                message, f"向 {message['recipient_urn']} 发送以下消息？\n\n{message['text']}", self.clock() + 900)}

    def _message_view(self, message, owner_session):
        read = self._get("message_read", key(self._principal(owner_session), message["sender_urn"], message["message_id"]))
        known = any(c["urn"] == message["sender_urn"] and self._belongs(c, owner_session) for c in self._all("contact"))
        return {**message, "read": bool(read), "read_at": read["read_at"] if read else None, "unknown_sender": not known}

    def _mark_read(self, message_id, owner_session):
        from .store import identifier
        identifier(message_id, "message_id")
        message = self._get("inbound", message_id)
        if not message or not self._message_visible(message, owner_session):
            raise ValueError("Message is not visible to this owner")
        read_key = key(self._principal(owner_session), message["sender_urn"], message_id)
        previous = self._get("message_read", read_key)
        self._put("message_read", read_key, previous or {"read_at": self.clock()})
        for item in self._all("attention"):
            if item["owner_id"] == self._principal(owner_session) and item["subject_id"] == message_id and item["state"] == "open" and item["source_kind"] in {"inbound", "contact_response"}:
                self._attention_put(item["owner_id"], item["source_kind"], item["source_id"], kind=item["kind"],
                    subject_id=message_id, target_kind=item["target"]["kind"], task_id=item.get("task_id"),
                    title=item["title"], summary=item["safe_summary"], state="resolved", source_revision=item["source_revision"])
        return {"message_id": message_id, "status": "read", "message": self._message_view(message, owner_session)}

    def mark_read(self, message_id, owner_session):
        self._owner(owner_session)
        with self._transaction():
            return self._mark_read(message_id, owner_session)

    def _message_visible(self, message, owner_session):
        owner = self._principal(owner_session)
        matched = [c for c in self._all("contact") if c["urn"] == message["sender_urn"]]
        # A known contact of a different profile must never leak via stranger mail.
        visible = any(self._belongs(c, owner_session) for c in matched) if matched else self._mail_owner() == owner
        return visible and self._inbound_local_task(message, owner_session) is not False

    def _sent_messages(self, owner_session):
        return [{"message_id": r["message_id"], "recipient_urn": r["message"]["recipient_urn"],
                 "text": r["message"]["text"], "status": r["status"], "created_at": r["created_at"]}
                for r in self._all("social_outbox") if self._belongs(r, owner_session) and r["message"]["kind"] == "chat.message"][-100:]

    def flush_social_outbox(self, transport, limit=16):
        """Retry the identical helper ID after uncertain delivery; never invent a resend."""
        with self._lock:
            pending = [r for r in self._all("social_outbox") if r["status"] != "accepted"][:limit]
        accepted = 0
        if not callable(getattr(transport, "store", None)):
            return {"accepted": 0, "pending": len(pending)}
        for record in pending:
            try:
                result = transport.store(record["message"])
                if result.get("success") is not True or result.get("message_id") != record["message_id"]:
                    raise ValueError("Helper did not accept this exact outgoing message")
            except OSError:
                break  # One unavailable helper must not incur N serial timeouts.
            except (ValueError, TypeError):
                continue
            with self._transaction():
                current = self._get("social_outbox", record["message_id"])
                current.update(status="accepted", updated_at=self.clock())
                self._put("social_outbox", record["message_id"], current)
            accepted += 1
        return {"accepted": accepted, "pending": len(pending) - accepted}
