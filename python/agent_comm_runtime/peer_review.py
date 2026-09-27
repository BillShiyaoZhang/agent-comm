"""Owner review of authenticated peer content, independent of task approval.

The normal data/model surfaces receive metadata until a trusted host or an
explicitly paired UI reviews the immutable body. A model Runtime action cannot
make this decision. Transport ACK is permitted after this durable quarantine.
"""
import json

from .social import SOCIAL_KINDS, key

REVIEW_KINDS = {"invite", "proposal", "change_request"}
REVIEW_POLICY = {"version": 1, "approval_scope": "local_content_use_only"}
PEER_CONTENT_SAFETY = {"version": 1, "mode": "owner_review", "automatic_peer_model_execution": False}


class PeerReviewMixin:
    @staticmethod
    def _needs_peer_review(message):
        if message.get("kind") in SOCIAL_KINDS | {"control.request", "control.response"}:
            return False
        try:
            packet = json.loads(message["text"])
        except (ValueError, TypeError, RecursionError):
            return True
        if isinstance(packet, dict) and packet.get("protocol") == "agent-comm-collaboration/v2":
            # Recovery containers themselves can carry historical free text.
            # Their events are reviewed separately by _v2_drain.
            return packet.get("kind") in REVIEW_KINDS
        return True

    def _review_record(self, message_id, owner_session):
        matches = [r for r in self._all("peer_review") if r["message_id"] == message_id and self._belongs(r, owner_session)]
        if len(matches) != 1:
            raise ValueError("No peer review for this owner and message")
        return matches[0]

    def _stage_peer_review(self, message, owner_session):
        from .store import digest
        try:
            packet = json.loads(message["text"])
        except (ValueError, TypeError, RecursionError):
            packet = None
        if isinstance(packet, dict) and packet.get("protocol") == "agent-comm-collaboration/v2":
            from .collaboration_v2 import validate_event
            validate_event(packet)
            if packet["sender_urn"] != message["sender_urn"] or packet["recipient_urn"] != self.local_urn:
                raise ValueError("Peer review identities differ from authenticated helper routing")
            if message.get("task_id") not in (None, packet["collaboration_id"]):
                raise ValueError("Peer review collaboration association conflicts with routing")
        owner = self._principal(owner_session)
        record_id = key(owner, message["sender_urn"], message["message_id"])
        # Only authenticated wire fields participate. Projection/read flags and
        # migration time cannot change an immutable review fingerprint.
        wire = {k: message[k] for k in ("message_id", "sender_urn", "text", "task_id", "conversation_id", "kind", "in_reply_to", "deadline") if k in message}
        fingerprint = digest(wire)
        old = self._get("peer_review", record_id)
        if old:
            if old["fingerprint"] != fingerprint:
                raise ValueError("Review message ID conflicts with immutable content")
            return old
        record = {"owner_id": owner, "message_id": wire["message_id"], "sender_urn": wire["sender_urn"],
                  "kind": wire.get("kind", "chat.message"), "wire": wire,
                  "fingerprint": fingerprint, "received_at": message.get("received_at", self.clock()), "status": "pending"}
        self._put("peer_review", record_id, record)
        return record

    def _ensure_peer_reviews(self, owner_session):
        """Additive fail-closed projection for pre-review stored messages/events."""
        for message in self._all("inbound"):
            if not self._needs_peer_review(message) or message.get("quarantined"):
                continue
            if not self._message_visible(message, owner_session, review_gate=False):
                continue
            review = self._stage_peer_review(message, owner_session)
            if review["status"] != "approved":
                message.update(quarantined=True, quarantine_reason="pending_review")
                self._put("inbound", message["message_id"], message)
        # A historical sync_response is authenticated only for this sender's
        # events; do not let its nested proposal bypass the review boundary.
        for event in self._all("v2_event"):
            packet = event["packet"]
            if not event.get("outgoing") and self._belongs(event, owner_session) and packet["kind"] in REVIEW_KINDS:
                self._event_review(packet, owner_session)

    def _pending_peer_reviews(self, owner_session):
        self._ensure_peer_reviews(owner_session)
        return [{k: r[k] for k in ("message_id", "sender_urn", "kind", "received_at", "status")}
                for r in self._all("peer_review") if self._belongs(r, owner_session) and r["status"] == "pending"
                and not self._peer_blocked(r["sender_urn"], owner_session)
                and not self._get("peer_message_block", key(self._principal(owner_session), r["sender_urn"], r["message_id"]))][-100:]

    def review_preview(self, message_id, owner_session):
        from .store import identifier, canonical
        self._owner(owner_session)
        identifier(message_id, "message_id")
        with self._transaction():
            self._ensure_peer_reviews(owner_session)
            record = self._review_record(message_id, owner_session)
            if self._peer_blocked(record["sender_urn"], owner_session) or self._get("peer_message_block", key(self._principal(owner_session), record["sender_urn"], message_id)):
                raise ValueError("Blocked peer content is unavailable for review")
            # Requiring an explicit full preview in the same paired host session
            # prevents the normal inbox from being used as a blind approval UI.
            result = {**{k: record[k] for k in ("message_id", "sender_urn", "kind", "received_at", "status", "fingerprint")},
                    "text": record["wire"]["text"], "text_truncated": False}
            # JSON escaping can expand a valid 100 kB helper body sixfold.
            # Reserve ample space for the correlated control envelope; never
            # mark a preview that the paired UI cannot receive in full.
            if len(canonical(result).encode()) > 200000:
                raise ValueError("Complete peer preview exceeds the paired response limit")
            self._put("peer_review_preview", key(owner_session, message_id), {"fingerprint": record["fingerprint"]})
            return result

    def _review_peer(self, message_id, decision, owner_session):
        from .store import identifier
        identifier(message_id, "message_id")
        if decision not in {"approve", "reject"}:
            raise ValueError("Review decision must be approve or reject")
        record = self._review_record(message_id, owner_session)
        status = "approved" if decision == "approve" else "rejected"
        if record["status"] != "pending":
            if record["status"] != status:
                raise ValueError("Peer review is already finally decided")
            return {k: record[k] for k in ("message_id", "sender_urn", "status", "fingerprint")}
        if self._peer_blocked(record["sender_urn"], owner_session) or self._get("peer_message_block", key(self._principal(owner_session), record["sender_urn"], message_id)):
            raise ValueError("Blocked peer content cannot be approved")
        preview = self._get("peer_review_preview", key(owner_session, message_id))
        if status == "approved" and (not preview or preview["fingerprint"] != record["fingerprint"]):
            raise ValueError("Preview the complete current peer message before approving")
        record.update(status=status, decided_at=self.clock())
        self._put("peer_review", key(record["owner_id"], record["sender_urn"], message_id), record)
        message = self._get("inbound", message_id)
        if message and message.get("quarantine_reason") == "pending_review":
            if status == "approved":
                message = {k: v for k, v in message.items() if k not in {"quarantined", "quarantine_reason"}}
                self._put("inbound", message_id, message)
                self.ingest_v2_record(message)
            else:
                message["quarantine_reason"] = "rejected_review"
                self._put("inbound", message_id, message)
        # Previously buffered nested recovery events can now advance, while
        # rejected event bodies remain unavailable to normal/model reads.
        for c in self._all("v2_collaboration"):
            if self._belongs(c, owner_session) and c["peer_urn"] == record["sender_urn"]:
                self._v2_drain(c)
                self._put("v2_collaboration", c["collaboration_id"], c)
        self._audit("peer_content_reviewed", message_id=message_id, owner_id=record["owner_id"], status=status)
        return {k: record[k] for k in ("message_id", "sender_urn", "status", "fingerprint")}

    def review_peer(self, message_id, decision, owner_session):
        """Trusted local-host API. This is deliberately absent from Runtime tools."""
        self._owner(owner_session)
        with self._transaction():
            self._ensure_peer_reviews(owner_session)
            return self._review_peer(message_id, decision, owner_session)

    def _event_review(self, packet, owner_session):
        from .store import canonical, digest
        # A matching directly delivered packet and its recovery copy share one
        # owner decision, rather than creating two independently approved views.
        for record in self._all("peer_review"):
            if self._belongs(record, owner_session) and record["sender_urn"] == packet["sender_urn"]:
                try:
                    if json.loads(record["wire"]["text"]) == packet:
                        return record
                except (ValueError, TypeError, RecursionError):
                    pass
        return self._stage_peer_review({"message_id": "peer-event-" + digest([packet["sender_urn"], packet["event_id"]])[:48],
            "sender_urn": packet["sender_urn"], "kind": "collaboration.v2", "text": canonical(packet)}, owner_session)

    def _collaboration_review_safe(self, c, owner_session):
        if c.get("peer_blocked_at") is not None or self._peer_blocked(c["peer_urn"], owner_session):
            return False
        for event in self._all("v2_event"):
            p = event["packet"]
            if (not event.get("outgoing") and self._belongs(event, owner_session) and p["collaboration_id"] == c["collaboration_id"]
                    and p["kind"] in REVIEW_KINDS and self._event_review(p, owner_session)["status"] != "approved"):
                return False
        return True

    def _message_review_safe(self, message, owner_session):
        """Normal inbox/attention must not expose a recovery container's body.

        Ingestion still processes receipts and validated sync evidence; nested
        free-text events remain separately pending until this owner's review.
        """
        if self._needs_peer_review(message):
            record = self._get("peer_review", key(self._principal(owner_session), message["sender_urn"], message["message_id"]))
            return bool(record and record["status"] == "approved")
        try:
            packet = json.loads(message["text"])
        except (ValueError, TypeError, RecursionError):
            return True
        if isinstance(packet, dict) and packet.get("protocol") == "agent-comm-collaboration/v2":
            # Routine protocol packets still advance the typed state machine.
            # Their raw wire text (including peer-chosen receipt reason strings
            # or nested historical bodies) is not a model/inbox content surface.
            return False
        return True

    def _proposal_review_safe(self, proposal, owner_session):
        if not proposal or "source_message_id" not in proposal:
            return True
        message = self._get("inbound", proposal["source_message_id"])
        return bool(message and self._message_visible(message, owner_session))
