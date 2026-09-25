"""Bilateral, agreement-only collaboration on the existing local authority store.

The host authenticates owner_session; the helper authenticates incoming senders.
Neither value can be supplied by a peer payload. This mixin never calls a model,
executes an external tool, or turns an arbitrary event into an authorized send.
"""
from datetime import datetime, timezone
import hashlib
import json
import re

from .identity import validate_urn
from .policy import validate_action

PROTOCOL = "agent-comm-collaboration/v2"
KINDS = frozenset({"invite", "join", "proposal", "change_request", "accept", "agreement",
                   "agreement_ack", "withdraw", "cancel_request", "cancel_ack", "receipt",
                   "sync_request", "sync_response"})
MAINTENANCE = frozenset({"agreement", "agreement_ack", "receipt", "sync_request", "sync_response"})
SYNC = frozenset({"sync_request", "sync_response"})
DECISIONS = frozenset({"invite", "join", "withdraw", "cancel_request", "cancel_ack"})
RECOVERY = frozenset({"withdraw", "cancel_request", "cancel_ack"})
MAX_MAINTENANCE = 32
MAX_EVENTS = 256
FIELDS = frozenset({"protocol", "collaboration_id", "event_id", "kind", "sender_urn", "recipient_urn",
                    "sender_sequence", "previous_event_id", "created_at", "expires_at", "payload", "payload_digest"})


def canonical(value):
    # Protocol keys are ASCII and numbers are bounded integers: this strict
    # subset has the same representation in Python, Go and JS (no float keys).
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False)


def digest(value):
    return hashlib.sha256(canonical(value).encode("utf-8")).hexdigest()


def identifier(value):
    if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9_.:-]{1,128}", value):
        raise ValueError("Provide an explicit bounded ASCII identifier")
    return value


def instant(value):
    if not isinstance(value, str) or not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,6})?(?:Z|[+-]\d{2}:\d{2})", value):
        raise ValueError("Timestamp must be RFC3339 with seconds and a timezone")
    return datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()


def timestamp(value):
    return datetime.fromtimestamp(value, timezone.utc).isoformat().replace("+00:00", "Z")


def exact(value, fields):
    if not isinstance(value, dict) or set(value) != set(fields):
        raise ValueError("Unsupported or missing collaboration fields")


def _bounded(value, depth=0):
    if depth > 12:
        raise ValueError("Collaboration data is nested too deeply")
    if isinstance(value, dict):
        if len(value) > 32 or any(not isinstance(k, str) or not k.isascii() for k in value):
            raise ValueError("Protocol object keys must be bounded ASCII strings")
        for item in value.values():
            _bounded(item, depth + 1)
    elif isinstance(value, list):
        if len(value) > MAX_EVENTS:
            raise ValueError("Protocol array is too long")
        for item in value:
            _bounded(item, depth + 1)
    elif isinstance(value, str):
        if len(value.encode("utf-8")) > 8000:
            raise ValueError("Protocol string is too long")
    elif value is not None and type(value) is not bool and (type(value) is not int or not 0 <= value <= 2147483647):
        raise ValueError("Protocol numbers must be bounded nonnegative integers")


def validate_event(packet):
    _bounded(packet)
    exact(packet, FIELDS)
    if packet["protocol"] != PROTOCOL or packet["kind"] not in KINDS:
        raise ValueError("Unsupported collaboration protocol or event")
    for key in ("collaboration_id", "event_id"):
        identifier(packet[key])
    for key in ("sender_urn", "recipient_urn"):
        validate_urn(packet[key])
    if packet["sender_urn"] == packet["recipient_urn"]:
        raise ValueError("A bilateral event must have two distinct endpoints")
    sequence = packet["sender_sequence"]
    if packet["kind"] in SYNC:
        if sequence != 0 or type(sequence) is not int or packet["previous_event_id"] is not None:
            raise ValueError("Recovery events are outside the ordered business stream")
    elif type(sequence) is not int or not 1 <= sequence <= MAX_EVENTS:
        raise ValueError("Invalid or exhausted sender sequence")
    previous = packet["previous_event_id"]
    if sequence in {0, 1}:
        if previous is not None:
            raise ValueError("First event cannot have a predecessor")
    else:
        identifier(previous)
    if instant(packet["created_at"]) >= instant(packet["expires_at"]):
        raise ValueError("Event expiry must follow creation")
    if not isinstance(packet["payload"], dict) or packet["payload_digest"] != digest(packet["payload"]):
        raise ValueError("Payload digest does not match")
    if len(canonical(packet).encode("utf-8")) > 90000:
        raise ValueError("Collaboration event is too large")
    return packet


class _Buffered(Exception):
    pass


class CollaborationV2Mixin:
    """Requires Store's transaction, records, native approval and task helpers."""

    def _v2_key(self, collaboration_id, sender, event_id):
        return digest([collaboration_id, sender, event_id])

    def _v2_collaboration(self, collaboration_id, owner_session):
        record = self._get("v2_collaboration", identifier(collaboration_id))
        if not self._belongs(record, owner_session):
            raise ValueError("Collaboration is not available to this owner")
        return record

    def _v2_record(self, c, sender, event_id):
        return self._get("v2_event", self._v2_key(c["collaboration_id"], sender, event_id))

    def _v2_terms_action(self, c, terms, capability):
        exact(terms, {"proposal_id", "version", "topic", "participant_ids", "start", "end",
                      "fulfillment_mode", "meeting_mode", "obligations", "formation_rule"})
        if (terms["fulfillment_mode"] != "agreement_only" or terms["meeting_mode"] != "online"
                or terms["formation_rule"] != "initiator_durable_decision_v1"
                or terms["obligations"] != "each_party_attends_only"
                or sorted(terms["participant_ids"]) != sorted([self.local_urn, c["peer_urn"]])):
            raise ValueError("Only a bilateral online agreement-only meeting is supported")
        payload = {key: terms[key] for key in ("proposal_id", "version", "topic", "participant_ids", "start", "end")}
        payload["participant_ids"] = ["self" if p == self.local_urn else c["peer_id"] for p in payload["participant_ids"]]
        return validate_action({"capability": capability, "recipient_ids": [c["peer_id"]], "payload": payload})

    def _v2_maintenance_live(self, c):
        permit = c.get("maintenance")
        return bool(permit and not permit.get("revoked") and permit["until"] > self.clock() and permit["used"] < permit["max_events"])

    def _v2_context_digest(self, c):
        return digest({key: c.get(key) for key in ("joined", "terms", "acceptances", "agreement", "cancel_request", "withdraw_pending")})

    def _v2_payload(self, c, kind, payload, task):
        """Compile every outgoing payload from a strict typed request/local facts."""
        if kind in {"invite", "join"}:
            return {"topic": task["scope"]["topic"], "fulfillment_mode": "agreement_only",
                    "formation_rule": "initiator_durable_decision_v1"}, None
        if kind in {"proposal", "change_request"}:
            action = validate_action({"capability": "propose_meeting", "recipient_ids": [c["peer_id"]], "payload": payload})
            p = action["payload"]
            if set(p["participant_ids"]) != {"self", c["peer_id"]}:
                raise ValueError("The confirmed two parties must be the only participants")
            terms = {**p, "participant_ids": sorted([self.local_urn, c["peer_urn"]]),
                     "fulfillment_mode": "agreement_only", "meeting_mode": "online",
                     "obligations": "each_party_attends_only", "formation_rule": "initiator_durable_decision_v1"}
            return {"terms": terms, "terms_digest": digest(terms),
                    "parent_digest": digest(c["terms"]) if c.get("terms") else None}, action
        exact(payload, {"event_id"} if kind == "receipt" else set())
        if kind == "accept":
            if not c.get("terms"):
                raise ValueError("Record a current proposal before accepting")
            action = self._v2_terms_action(c, c["terms"], "accept_meeting")
            return {"terms_digest": digest(c["terms"]), "actor_urn": self.local_urn,
                    "own_obligations": "attend_only", "authority_basis": "peer_attested",
                    "valid_until": task["scope"]["expires_at"]}, action
        if kind == "agreement":
            if not c.get("agreement") or c["initiator_urn"] != self.local_urn:
                raise ValueError("Only the coordinator's existing formation can be sent")
            return c["agreement"], None
        if kind in {"agreement_ack", "cancel_request", "cancel_ack"}:
            if not c.get("agreement"):
                raise ValueError("No recorded agreement exists")
            if kind == "cancel_ack" and not c.get("cancel_request"):
                raise ValueError("No cancellation request exists")
            return {"agreement_id": c["agreement"]["agreement_id"]}, None
        if kind == "withdraw":
            acceptance = c.get("acceptances", {}).get(self.local_urn)
            if not acceptance:
                raise ValueError("There is no local acceptance to withdraw")
            return {"accept_event_id": acceptance["event_id"], "terms_digest": acceptance["terms_digest"]}, None
        if kind == "receipt":
            record = self._v2_record(c, c["peer_urn"], identifier(payload["event_id"]))
            if not record or record["packet"]["kind"] in {"receipt", "sync_response"}:
                raise ValueError("Receipt requires a recorded non-receipt peer event")
            return {"event_id": record["packet"]["event_id"], "event_digest": record["digest"],
                    "processing_status": record["status"], "reason": record.get("reason", "")}, None
        if kind == "sync_request":
            return {"after_sequence": c["in_sequence"]}, None
        if kind == "sync_response":
            after = c.get("sync_after", 0)
            # Include sequence tombstones/receipts too: otherwise the stream
            # could never fill a gap whose original event was a receipt.
            events = sorted([r["packet"] for r in self._all("v2_event")
                             if r.get("owner_id") == c["owner_id"] and r["packet"]["collaboration_id"] == c["collaboration_id"]
                             and r["packet"]["sender_urn"] == self.local_urn and r["packet"]["sender_sequence"] > after
                             and r["packet"]["kind"] not in SYNC and r["status"] == "applied"], key=lambda e: e["sender_sequence"])[:8]
            return {"events": events}, None
        raise ValueError("Unsupported collaboration action")

    def prepare_collaboration(self, task_id, collaboration_id, operation_id, kind, payload, owner_session):
        self._owner(owner_session)
        identifier(collaboration_id)
        identifier(operation_id)
        if kind not in KINDS or not isinstance(payload, dict):
            raise ValueError("Provide a supported typed collaboration request")
        _bounded(payload)
        request_digest = digest([task_id, collaboration_id, kind, payload])
        with self._transaction():
            old = self._get("v2_operation", operation_id)
            if old:
                if not self._belongs(old, owner_session) or old["request_digest"] != request_digest:
                    raise ValueError("Operation ID conflicts with an existing action or owner")
                return self._v2_operation_view(old)
            if self._get("operation", operation_id):
                raise ValueError("Operation ID is already used by a v1 action")
            task = self._task(task_id, owner_session)
            c = self._get("v2_collaboration", collaboration_id)
            if kind in {"invite", "join"}:
                exact(payload, {"peer_id"} if kind == "invite" else {"message_id"})
                if not self._live(task):
                    return {"decision": "deny", "reasons": ["task_not_active"]}
                if c:
                    raise ValueError("This collaboration is already bound")
                if kind == "invite":
                    peer_id = identifier(payload["peer_id"])
                    peer = self._get("contact", peer_id)
                    invitation = None
                else:
                    invitation = self._get("v2_invitation", identifier(payload["message_id"]))
                    if (not invitation or invitation["packet"]["collaboration_id"] != collaboration_id
                            or instant(invitation["packet"]["expires_at"]) <= self.clock()):
                        raise ValueError("No current authenticated invitation matches this task")
                    matches = [p for p in self._all("contact") if self._belongs(p, owner_session)
                               and p["urn"] == invitation["packet"]["sender_urn"]]
                    if len(matches) != 1:
                        raise ValueError("Resolve a unique confirmed peer before joining")
                    peer = matches[0]
                    peer_id = peer["contact_id"]
                    if invitation["packet"]["payload"]["topic"] != task["scope"]["topic"]:
                        raise ValueError("Invitation topic differs from the local mandate")
                if not self._belongs(peer, owner_session) or peer_id not in task["scope"]["recipient_ids"]:
                    raise ValueError("Peer must be a confirmed recipient of the local mandate")
                if not self._can_send_to(peer["urn"], owner_session):
                    return {"decision": "deny", "reasons": ["contact_not_connected"]}
                validate_urn(self.local_urn)
                if peer["urn"] == self.local_urn:
                    raise ValueError("A collaboration requires a distinct peer")
                c = {"collaboration_id": collaboration_id, "owner_id": self._principal(owner_session),
                     "task_id": task_id, "peer_id": peer_id, "peer_urn": peer["urn"],
                     "initiator_urn": self.local_urn if kind == "invite" else peer["urn"],
                     "phase": "invited", "joined": False, "terms": None, "acceptances": {},
                     "agreement": None, "agreement_synced": False, "withdraw_pending": False,
                     "out_sequence": 0, "last_out_event_id": None, "in_sequence": 0, "last_in_event_id": None,
                     "maintenance": None, "created_at": self.clock()}
                if invitation:
                    c["invitation"] = invitation["packet"]
            else:
                c = self._v2_collaboration(collaboration_id, owner_session)
                if c["task_id"] != task_id:
                    raise ValueError("Shared collaboration ID cannot select another local mandate")
            wire_payload, action = self._v2_payload(c, kind, payload, task)
            if kind not in {"invite", "join"}:
                self._v2_precondition(c, kind, wire_payload, self.local_urn)
            maintenance = kind in MAINTENANCE
            recovery_once = kind in RECOVERY and (not self._live(task) or task["used_count"] >= task["scope"]["max_actions"])
            if maintenance:
                if not self._v2_maintenance_live(c):
                    return {"decision": "deny", "reasons": ["maintenance_permission_expired_or_exhausted"]}
                verdict = {"decision": "allow", "reasons": ["confirmed_bounded_protocol_maintenance"]}
                expires = c["maintenance"]["until"]
            elif recovery_once:
                # A new exact native confirmation, not an exception to the old
                # grant. Its only effect is this one bounded recovery message.
                verdict = {"decision": "ask", "reasons": ["explicit_one_event_recovery"]}
                expires = self.clock() + 900
            else:
                if not self._live(task):
                    return {"decision": "deny", "reasons": ["task_not_active"]}
                if task["used_count"] >= task["scope"]["max_actions"]:
                    return {"decision": "deny", "reasons": ["action_limit_reached"]}
                verdict = self._evaluate_current(task, action) if action else {"decision": "ask", "reasons": ["explicit_protocol_decision"]}
                if verdict["decision"] in {"deny", "clarify"}:
                    return verdict
                expires = instant(task["scope"]["expires_at"])
            op = {"operation_id": operation_id, "task_id": task_id, "owner_id": c["owner_id"],
                  "collaboration_id": collaboration_id, "kind": kind, "payload": wire_payload,
                  "action": action, "revision": task["revision"], "request_digest": request_digest,
                  "context_digest": self._v2_context_digest(c), "status": "ready" if verdict["decision"] == "allow" else "awaiting_approval",
                  "decision": verdict, "expires_at": expires, "maintenance": maintenance,
                  "recovery_once": recovery_once,
                  "created_at": self.clock(), "text": canonical({"kind": kind, "recipient_urn": c["peer_urn"], "payload": wire_payload}),
                  "deliveries": []}
            if kind in {"invite", "join"}:
                # The native card approves these exact finite maintenance rights.
                op["new_collaboration"] = c
                op["maintenance_permit"] = {"until": min(expires + 86400, self.clock() + 7 * 86400),
                                             "max_events": MAX_MAINTENANCE, "used": 0}
            op["hash"] = digest({k: v for k, v in op.items() if k != "status"})
            if verdict["decision"] == "ask":
                question = "请确认这一次协作动作及完整对外内容：\n" + op["text"]
                if kind in {"invite", "join"}:
                    question += ("\n双方各自授权；范围内方案和接受继续按本事项委托检查。"
                                 "仅协调线上会议方案，不写入日历。A 的持久成约决定裁定接受与撤回顺序。"
                                 f"\n同时允许程序向此对象发送至多 {MAX_MAINTENANCE} 条固定状态/约定/同步回执，"
                                 f"至 {timestamp(op['maintenance_permit']['until'])}，此有限维护许可独立于业务委托，撤销业务后仍可对账；"
                                 "不允许新资料披露、新承诺、任意文本或外部工具。")
                if recovery_once:
                    question += ("\n原业务委托已经停止或耗尽；本次是独立恢复许可：仅允许向已绑定对象发送上述一条撤回/取消处理事件，"
                                 f"有效至 {timestamp(expires)}，不重新启用业务委托、不允许新提议、新承诺、资料披露或外部工具。")
                question += "\n是否允许？此问题需要本人在可信确认渠道回答。"
                approval = self._approval("collaboration_v2", operation_id, owner_session,
                                          {"hash": op["hash"]}, question, min(expires, self.clock() + 900))
                op["approval_id"] = approval["approval_id"]
            self._put("v2_operation", operation_id, op)
            return self._v2_operation_view(op)

    def _v2_operation_view(self, op):
        result = {k: op[k] for k in ("operation_id", "task_id", "collaboration_id", "kind", "status", "text", "deliveries")}
        result["decision"] = "deny" if op["status"] == "denied" else "ask" if op["status"] == "awaiting_approval" else "allow"
        result["reasons"] = op["decision"]["reasons"]
        if "approval_id" in op:
            result["approval_id"] = op["approval_id"]
        if op["status"] not in {"accepted", "denied"} and not self._v2_operation_current(op):
            result.update(decision="deny", reasons=["revoked_expired_or_superseded"])
        result["delivery_meaning"] = "accepted means queued locally, not peer consent"
        return result

    def _v2_operation_current(self, op):
        if op["expires_at"] <= self.clock():
            return False
        c = self._get("v2_collaboration", op["collaboration_id"])
        if "new_collaboration" in op:
            if c and op["status"] != "sending":
                return False
            c = c or op["new_collaboration"]
        if not c or c["owner_id"] != op["owner_id"]:
            return False
        if op["maintenance"]:
            return bool(c.get("maintenance") and not c["maintenance"].get("revoked") and c["maintenance"]["until"] > self.clock()
                        and (op["status"] == "sending" or c["maintenance"]["used"] < c["maintenance"]["max_events"]))
        task = self._get("task", op["task_id"])
        if not task or task["revision"] != op["revision"] or (not op.get("recovery_once") and not self._live(task)):
            return False
        if op["status"] != "sending" and self._v2_context_digest(c) != op["context_digest"]:
            return False
        return True

    def _v2_approval_current(self, approval):
        op = self._get("v2_operation", approval["subject_id"])
        return bool(op and op["status"] == "awaiting_approval" and op["hash"] == approval["payload"]["hash"]
                    and self._v2_operation_current(op))

    def _v2_approval_deadline(self, approval):
        op = self._get("v2_operation", approval["subject_id"])
        return op["expires_at"] if op else self.clock()

    def _v2_approval_task_id(self, approval):
        op = self._get("v2_operation", approval["subject_id"])
        return op["task_id"] if op else None

    def _v2_apply_approval(self, approval):
        op = self._get("v2_operation", approval["subject_id"])
        op.update(status="ready", approved_once=True)
        self._put("v2_operation", op["operation_id"], op)

    def _v2_reject_approval(self, approval):
        op = self._get("v2_operation", approval["subject_id"])
        if op:
            op["status"] = "denied"
            self._put("v2_operation", op["operation_id"], op)

    _v2_deny_approval = _v2_reject_approval

    def _v2_precondition(self, c, kind, payload, sender):
        if kind in MAINTENANCE:
            if kind == "agreement_ack" and sender == c["initiator_urn"]:
                raise ValueError("Only the peer acknowledges the coordinator's formation")
            return
        if kind in {"invite", "join"}:
            return
        if not c["joined"]:
            raise ValueError("Peer has not joined the collaboration")
        if c.get("closure_reason") == "cancelled":
            raise ValueError("Cancelled collaborations cannot be reopened")
        if kind in {"proposal", "change_request", "accept"} and c.get("agreement"):
            raise ValueError("An existing agreement requires a separate revision collaboration")
        if kind == "proposal":
            if sender != c["initiator_urn"]:
                raise ValueError("Only the initiator may publish terms")
            if any(a.get("active") for a in c["acceptances"].values()):
                raise ValueError("Withdraw existing acceptances before changing terms")
            expected = digest(c["terms"]) if c.get("terms") else None
            if payload["parent_digest"] != expected:
                raise _Buffered("Missing preceding terms")
            if c.get("terms") and payload["terms"]["version"] != c["terms"]["version"] + 1:
                raise ValueError("Terms revision must advance exactly once")
            if not c.get("terms") and payload["terms"]["version"] != 1:
                raise ValueError("The first terms revision must be one")
        if kind == "change_request" and sender == c["initiator_urn"]:
            raise ValueError("Initiator publishes proposals; peer requests changes")
        if kind == "accept":
            if not c.get("terms") or payload["terms_digest"] != digest(c["terms"]):
                raise _Buffered("Acceptance is missing its current terms")
            if payload["actor_urn"] != sender:
                raise ValueError("An agent cannot accept for the other participant")

    def _v2_packet(self, c, op):
        packet = {"protocol": PROTOCOL, "collaboration_id": c["collaboration_id"],
                  "event_id": "event-" + digest([op["operation_id"], op["hash"]])[:40], "kind": op["kind"],
                  "sender_urn": self.local_urn, "recipient_urn": c["peer_urn"],
                  "sender_sequence": 0 if op["kind"] in SYNC else c["out_sequence"] + 1,
                  "previous_event_id": None if op["kind"] in SYNC else c["last_out_event_id"],
                  "created_at": timestamp(self.clock()), "expires_at": timestamp(op["expires_at"]),
                  "payload": op["payload"], "payload_digest": digest(op["payload"])}
        return validate_event(packet)

    def dispatch_collaboration(self, operation_id, owner_session, transport, *, worker_context=None):
        self._owner(owner_session)
        with self._transaction():
            if worker_context is not None:
                self._check_worker_context(worker_context)
            op = self._get("v2_operation", operation_id)
            if not self._belongs(op, owner_session):
                raise ValueError("Operation is not available to this owner")
            if op["status"] == "accepted":
                return self._v2_operation_view(op)
            if op["status"] not in {"ready", "sending"} or not self._v2_operation_current(op):
                return {"decision": "deny", "reasons": ["not_authorized_or_superseded"]}
            peer = self._get("v2_collaboration", op["collaboration_id"]) or op.get("new_collaboration")
            if not peer or not self._can_send_to(peer["peer_urn"], owner_session):
                return {"decision": "deny", "reasons": ["contact_not_connected"]}
            if worker_context is not None:
                worker, _ = self._check_worker_context(worker_context)
                if (op["task_id"] != worker_context.task_id or op["owner_id"] != worker_context.principal_id
                        or op["collaboration_id"] != worker["policy"]["collaboration_id"]
                        or op["kind"] not in {"proposal", "accept"} | MAINTENANCE or op["status"] != "ready"
                        or (op["kind"] == "proposal" and (not worker["policy"]["allow_propose"] or op["action"]["payload"] != worker["policy"]["proposal"]))
                        or (op["kind"] == "accept" and not worker["policy"]["allow_accept"])):
                    raise ValueError("Worker may only send one fresh permitted structured operation")
                self._reserve_worker_send(worker_context, operation_id)
            c = self._get("v2_collaboration", op["collaboration_id"])
            if op["status"] == "ready":
                task = self._task(op["task_id"], owner_session)
                if "new_collaboration" in op:
                    c = op["new_collaboration"]
                    c["maintenance"] = op["maintenance_permit"]
                    if c.get("invitation"):
                        incoming = c.pop("invitation")
                        self._v2_save_event(c, incoming, "applied", "")
                        c.update(in_sequence=1, last_in_event_id=incoming["event_id"])
                if op["maintenance"]:
                    if not self._v2_maintenance_live(c):
                        return {"decision": "deny", "reasons": ["maintenance_limit"]}
                    c["maintenance"]["used"] += 1
                elif op.get("recovery_once"):
                    if not op.get("approved_once") or op.get("recovery_used"):
                        return {"decision": "deny", "reasons": ["one_event_recovery_not_authorized"]}
                    op["recovery_used"] = True
                else:
                    if task["used_count"] >= task["scope"]["max_actions"]:
                        return {"decision": "deny", "reasons": ["action_limit_reached"]}
                    verdict = self._evaluate_current(task, op["action"]) if op["action"] else op["decision"]
                    if verdict["decision"] in {"deny", "clarify"} or (verdict["decision"] == "ask" and not op.get("approved_once")):
                        return {"decision": "deny", "reasons": ["current_scope_requires_new_confirmation"]}
                    task["used_count"] += 1
                    self._put("task", task["task_id"], task)
                packet = self._v2_packet(c, op)
                if packet["kind"] not in SYNC:
                    c.update(out_sequence=packet["sender_sequence"], last_out_event_id=packet["event_id"])
                # A reservation is not yet an acceptance statement. In particular
                # revoke between these transactions cannot leak it through sync.
                self._v2_save_event(c, packet, "reserved", "not_submitted", outgoing=True)
                self._put("v2_collaboration", c["collaboration_id"], c)
                op.update(status="sending", packet=packet, reserved_at=self.clock(),
                          deliveries=[{"recipient_urn": c["peer_urn"], "message_id": "v2-" + digest(packet)[:48], "status": "pending"}])
                self._put("v2_operation", operation_id, op)
        # Reserve and persist exact wire bytes before any external side effect.
        with self._transaction():
            if worker_context is not None:
                self._check_worker_context(worker_context)
            op = self._get("v2_operation", operation_id)
            if not self._v2_operation_current(op):
                return {"decision": "deny", "reasons": ["revoked_expired_or_superseded"]}
            delivery = op["deliveries"][0]
            packet = op["packet"]
            c = self._v2_collaboration(op["collaboration_id"], owner_session)
            if not self._can_send_to(packet["recipient_urn"], owner_session):
                return {"decision": "deny", "reasons": ["contact_not_connected"]}
            record = self._v2_record(c, self.local_urn, packet["event_id"])
            if record["status"] == "reserved":
                # Linearize the intention to send with the bounded helper call.
                # An uncertain return may already have had the external effect.
                self._v2_apply(c, packet, outgoing=True)
                record.update(status="applied", reason="")
                self._put("v2_event", self._v2_key(c["collaboration_id"], self.local_urn, packet["event_id"]), record)
                self._put("v2_collaboration", c["collaboration_id"], c)
            body = {"message_id": delivery["message_id"], "recipient_urn": packet["recipient_urn"],
                    "text": canonical(packet), "task_id": packet["collaboration_id"],
                    "conversation_id": "collaboration:" + packet["collaboration_id"],
                    "kind": "collaboration.v2", "deadline": packet["expires_at"], "hop_limit": 8}
            try:
                response = transport.store(body)
                if not isinstance(response, dict) or response.get("success") is not True or response.get("message_id") != body["message_id"]:
                    raise ValueError("Helper did not acknowledge this stable message ID")
                op["status"] = "accepted"
                delivery["status"] = "accepted"
            except Exception:
                delivery["status"] = "uncertain"
            self._put("v2_operation", operation_id, op)
            return self._v2_operation_view(op)

    def _v2_save_event(self, c, packet, status, reason, *, outgoing=False):
        record = {"owner_id": c["owner_id"], "task_id": c["task_id"], "packet": packet,
                  "digest": digest(packet), "status": status, "reason": reason,
                  "outgoing": outgoing, "recorded_at": self.clock()}
        key = self._v2_key(c["collaboration_id"], packet["sender_urn"], packet["event_id"])
        self._put("v2_event", key, record)
        return record

    def _v2_queue(self, c, kind, payload=None):
        """Host-only template outbox; finite permission was approved at join."""
        if not c.get("maintenance") or c["maintenance"].get("revoked") or c["maintenance"]["until"] <= self.clock():
            c["waiting_reason"] = "maintenance_permission"
            return
        task = self._get("task", c["task_id"])
        wire, _ = self._v2_payload(c, kind, payload or {}, task)
        key = "v2-auto-" + digest([c["collaboration_id"], kind, wire])[:48]
        if self._get("v2_operation", key):
            return
        pending = sum(1 for op in self._all("v2_operation") if op["collaboration_id"] == c["collaboration_id"]
                      and op["maintenance"] and op["status"] in {"ready", "sending"})
        if pending + c["maintenance"]["used"] >= c["maintenance"]["max_events"]:
            c["waiting_reason"] = "maintenance_budget"
            return
        op = {"operation_id": key, "task_id": c["task_id"], "owner_id": c["owner_id"],
              "collaboration_id": c["collaboration_id"], "kind": kind, "payload": wire, "action": None,
              "revision": task["revision"], "request_digest": digest([kind, wire]), "context_digest": self._v2_context_digest(c),
              "status": "ready", "decision": {"decision": "allow", "reasons": ["confirmed_bounded_protocol_maintenance"]},
              "expires_at": c["maintenance"]["until"], "maintenance": True, "created_at": self.clock(),
              "text": canonical({"kind": kind, "recipient_urn": c["peer_urn"], "payload": wire}), "deliveries": []}
        op["hash"] = digest(op)
        self._put("v2_operation", key, op)

    def _v2_form(self, c):
        if c["initiator_urn"] != self.local_urn or c.get("agreement") or not c.get("terms"):
            return
        accepts = c["acceptances"]
        if set(accepts) != {self.local_urn, c["peer_urn"]}:
            return
        if any(not a["active"] or instant(a["valid_until"]) <= self.clock()
               or a["terms_digest"] != digest(c["terms"]) for a in accepts.values()):
            return
        refs = [{"actor_urn": urn, "event_id": accepts[urn]["event_id"], "event_digest": accepts[urn]["event_digest"]}
                for urn in sorted(accepts)]
        base = {"terms_digest": digest(c["terms"]), "formed_at": timestamp(self.clock()), "acceptances": refs,
                "initiator_sequence": c["out_sequence"], "peer_sequence": c["in_sequence"]}
        decision = "formation-" + digest([c["collaboration_id"], base])[:40]
        c["agreement"] = {**base, "formation_decision_id": decision,
                          "agreement_id": "agreement-" + digest([c["collaboration_id"], decision])[:40]}
        c.update(phase="agreed", waiting_reason="agreement_sync")
        self._v2_queue(c, "agreement")

    def _v2_apply(self, c, packet, *, outgoing=False):
        kind, p, sender = packet["kind"], packet["payload"], packet["sender_urn"]
        self._v2_precondition(c, kind, p, sender)
        if kind in {"invite", "join"}:
            exact(p, {"topic", "fulfillment_mode", "formation_rule"})
            if p["fulfillment_mode"] != "agreement_only" or p["formation_rule"] != "initiator_durable_decision_v1":
                raise ValueError("Unsupported collaboration terms")
            if kind == "join":
                if sender == c["initiator_urn"] or p["topic"] != self._get("task", c["task_id"])["scope"]["topic"]:
                    raise ValueError("Join does not match the invitation")
                c.update(joined=True, phase="negotiating")
        elif kind in {"proposal", "change_request"}:
            exact(p, {"terms", "terms_digest", "parent_digest"})
            self._v2_terms_action(c, p["terms"], "propose_meeting")
            if p["terms_digest"] != digest(p["terms"]):
                raise ValueError("Terms digest does not match")
            if kind == "proposal":
                c.update(terms=p["terms"], acceptances={}, phase="negotiating", withdraw_pending=False)
            else:
                c["change_request"] = p
        elif kind == "accept":
            exact(p, {"terms_digest", "actor_urn", "own_obligations", "authority_basis", "valid_until"})
            if p["own_obligations"] != "attend_only" or p["authority_basis"] != "peer_attested":
                raise ValueError("Unsupported acceptance authority or obligation")
            if instant(p["valid_until"]) <= self.clock():
                raise ValueError("Acceptance formation deadline has passed")
            previous = c["acceptances"].get(sender)
            if previous and previous["active"] and previous["event_id"] != packet["event_id"]:
                raise ValueError("A live acceptance cannot be replaced")
            c["acceptances"][sender] = {**p, "event_id": packet["event_id"], "event_digest": digest(packet), "active": True}
            c["phase"] = "partially_accepted"
            self._v2_form(c)
        elif kind == "agreement":
            exact(p, {"agreement_id", "formation_decision_id", "formed_at", "terms_digest", "acceptances", "initiator_sequence", "peer_sequence"})
            if sender != c["initiator_urn"]:
                raise ValueError("Only the designated coordinator can form an agreement")
            if c.get("agreement") and c["agreement"] != p:
                raise ValueError("An existing formation decision is immutable")
            if c.get("agreement") == p and c.get("closure_reason") == "cancelled":
                return
            if not c.get("terms") or digest(c["terms"]) != p["terms_digest"]:
                raise _Buffered("Agreement is missing its terms")
            if not isinstance(p["acceptances"], list) or len(p["acceptances"]) != 2:
                raise ValueError("An agreement needs exactly two acceptance references")
            actors = set()
            cursors = {c["initiator_urn"]: p["initiator_sequence"],
                       c["peer_urn"] if c["initiator_urn"] == self.local_urn else self.local_urn: p["peer_sequence"]}
            if any(type(n) is not int or not 1 <= n <= MAX_EVENTS for n in cursors.values()):
                raise ValueError("Invalid formation event cursors")
            if p["initiator_sequence"] >= packet["sender_sequence"] and not outgoing:
                raise ValueError("Formation cursor cannot reference the agreement or future events")
            if instant(p["formed_at"]) > instant(packet["created_at"]) or instant(p["formed_at"]) > self.clock() + 60:
                raise ValueError("Formation cannot occur after its publication")
            for ref in p["acceptances"]:
                exact(ref, {"actor_urn", "event_id", "event_digest"})
                actor = ref["actor_urn"]
                if actor not in {self.local_urn, c["peer_urn"]} or actor in actors:
                    raise ValueError("Acceptance actors must match both bound participants")
                actors.add(actor)
                record = self._v2_record(c, actor, ref["event_id"])
                if not record:
                    raise _Buffered("Agreement is missing independently authenticated acceptance evidence")
                accepted = record["packet"]
                if (record["status"] != "applied" or record["digest"] != ref["event_digest"] or accepted["kind"] != "accept"
                        or accepted["payload"]["actor_urn"] != actor
                        or accepted["payload"]["terms_digest"] != p["terms_digest"]
                        or accepted["payload"]["authority_basis"] != "peer_attested"
                        or accepted["payload"]["own_obligations"] != "attend_only"
                        or accepted["sender_sequence"] > cursors[actor]
                        or instant(accepted["created_at"]) > instant(p["formed_at"])
                        or instant(accepted["payload"]["valid_until"]) <= instant(p["formed_at"])):
                    raise ValueError("Agreement acceptance evidence does not match")
                for prior in self._all("v2_event"):
                    event = prior["packet"]
                    if (prior["owner_id"] == c["owner_id"] and prior["status"] == "applied"
                            and event["collaboration_id"] == c["collaboration_id"] and event["sender_urn"] == actor
                            and event["kind"] == "withdraw" and event["sender_sequence"] <= cursors[actor]
                            and event["payload"]["accept_event_id"] == ref["event_id"]):
                        raise ValueError("The formation cursor includes an earlier withdrawal")
            c.update(agreement=p, phase="agreed", waiting_reason="agreement_ack")
            if not outgoing:
                self._v2_queue(c, "agreement_ack")
        elif kind == "agreement_ack":
            exact(p, {"agreement_id"})
            if not c.get("agreement") or p["agreement_id"] != c["agreement"]["agreement_id"]:
                raise _Buffered("Acknowledgment is missing its agreement")
            if outgoing:
                c.update(phase="agreed", waiting_reason="agreement_ack_delivery")
            else:
                c.update(agreement_synced=True, phase="closed", closure_reason="agreement_only_complete", waiting_reason=None)
        elif kind == "withdraw":
            exact(p, {"accept_event_id", "terms_digest"})
            old = c["acceptances"].get(sender)
            if not old or old["event_id"] != p["accept_event_id"] or old["terms_digest"] != p["terms_digest"]:
                raise ValueError("Withdrawal does not identify the sender's acceptance")
            c.update(phase="reconciling", withdraw_pending=True, waiting_reason="withdrawal_decision")
            if not c.get("agreement"):
                old["active"] = False
                if c["initiator_urn"] == self.local_urn:
                    c.update(withdraw_pending=False, waiting_reason=None)
            elif c["initiator_urn"] == self.local_urn:
                self._v2_queue(c, "agreement")
        elif kind in {"cancel_request", "cancel_ack"}:
            exact(p, {"agreement_id"})
            if not c.get("agreement") or p["agreement_id"] != c["agreement"]["agreement_id"]:
                raise ValueError("Cancellation must identify the recorded agreement")
            if kind == "cancel_request":
                c.update(cancel_request={"sender_urn": sender, "event_id": packet["event_id"]}, phase="reconciling", waiting_reason="cancel_decision")
            else:
                request = c.get("cancel_request")
                if not request or request["sender_urn"] == sender:
                    raise ValueError("The other participant must accept the cancellation")
                c.update(phase="closed", closure_reason="cancelled", waiting_reason=None)
        elif kind == "receipt":
            exact(p, {"event_id", "event_digest", "processing_status", "reason"})
            record = self._v2_record(c, packet["recipient_urn"], p["event_id"])
            if not record or record["digest"] != p["event_digest"] or p["processing_status"] not in {"applied", "buffered", "rejected"}:
                raise ValueError("Receipt must match a persisted event")
            if not outgoing:
                record["peer_receipt"] = p
                self._put("v2_event", self._v2_key(c["collaboration_id"], packet["recipient_urn"], p["event_id"]), record)
                if record["packet"]["kind"] == "withdraw" and p["processing_status"] == "applied" and not c.get("agreement"):
                    c.update(withdraw_pending=False, phase="negotiating", waiting_reason=None)
                if record["packet"]["kind"] == "agreement_ack" and p["processing_status"] == "applied" and c.get("closure_reason") != "cancelled":
                    c.update(agreement_synced=True, phase="closed", closure_reason="agreement_only_complete", waiting_reason=None)
        elif kind == "sync_request":
            exact(p, {"after_sequence"})
            if type(p["after_sequence"]) is not int or not 0 <= p["after_sequence"] <= MAX_EVENTS:
                raise ValueError("Invalid recovery cursor")
            if not outgoing:
                c["sync_after"] = p["after_sequence"]
                self._v2_queue(c, "sync_response")
        elif kind == "sync_response":
            exact(p, {"events"})
            if not isinstance(p["events"], list) or len(p["events"]) > 8:
                raise ValueError("Recovery response must contain at most eight events")
            # This envelope authenticates only this sender's historical events.
            if not outgoing:
                for old in p["events"]:
                    validate_event(old)
                    if (old["sender_urn"] != sender or old["recipient_urn"] != self.local_urn
                            or old["collaboration_id"] != c["collaboration_id"] or old["kind"] in SYNC):
                        raise ValueError("Recovery cannot introduce another sender's evidence")
                for old in p["events"]:
                    self._v2_ingest_bound(c, old, historical=True)

    def _v2_ingest_bound(self, c, packet, *, historical=False):
        key = self._v2_key(c["collaboration_id"], packet["sender_urn"], packet["event_id"])
        old = self._get("v2_event", key)
        if old:
            if old["digest"] != digest(packet):
                raise ValueError("Event ID conflicts with immutable recorded content")
            return old
        for record in self._all("v2_event"):
            e = record["packet"]
            if (e["collaboration_id"] == c["collaboration_id"] and e["sender_urn"] == packet["sender_urn"]
                    and packet["kind"] not in SYNC and e["sender_sequence"] == packet["sender_sequence"]):
                raise ValueError("Sender sequence conflicts with an existing event")
        status, reason = "buffered", "missing_predecessor"
        if instant(packet["created_at"]) > self.clock() + 60:
            status, reason = "rejected", "future_event"
        elif instant(packet["expires_at"]) <= self.clock() and packet["kind"] not in {"agreement", "sync_response"}:
            status, reason = "rejected", "expired_event"
        record = self._v2_save_event(c, packet, status, reason)
        # Sync responses bypass the stream gap they are repairing. Their own
        # cursor advances only when all preceding sequence records are present.
        if packet["kind"] in SYNC and status == "buffered":
            before = json.loads(canonical(c))
            self._db.execute("SAVEPOINT v2_recovery_event")
            try:
                self._v2_apply(c, packet)
                record.update(status="applied", reason="")
            except (ValueError, KeyError, TypeError):
                self._db.execute("ROLLBACK TO v2_recovery_event")
                c.clear()
                c.update(before)
                record.update(status="rejected", reason="invalid_recovery_payload")
            finally:
                self._db.execute("RELEASE v2_recovery_event")
            self._put("v2_event", key, record)
        return record

    def _v2_drain(self, c):
        for _ in range(MAX_EVENTS):
            candidates = [r for r in self._all("v2_event") if r["owner_id"] == c["owner_id"]
                          and r["packet"]["collaboration_id"] == c["collaboration_id"]
                          and r["packet"]["sender_urn"] == c["peer_urn"]
                          and r["packet"]["sender_sequence"] == c["in_sequence"] + 1]
            if not candidates:
                break
            record = candidates[0]
            packet = record["packet"]
            if packet["previous_event_id"] != c["last_in_event_id"]:
                c.update(phase="reconciling", waiting_reason="event_chain_conflict")
                break
            if record["status"] == "buffered":
                if instant(packet["expires_at"]) <= self.clock() and packet["kind"] != "agreement":
                    record.update(status="rejected", reason="expired_while_buffered")
                    self._put("v2_event", self._v2_key(c["collaboration_id"], packet["sender_urn"], packet["event_id"]), record)
                    c.update(in_sequence=packet["sender_sequence"], last_in_event_id=packet["event_id"])
                    self._v2_queue(c, "receipt", {"event_id": packet["event_id"]})
                    continue
                # Advance the incoming cursor within the same transaction before
                # formation: the decision records the acceptance it consumed.
                before = json.loads(canonical(c))
                c.update(in_sequence=packet["sender_sequence"], last_in_event_id=packet["event_id"])
                self._db.execute("SAVEPOINT v2_reduce_event")
                try:
                    self._v2_apply(c, packet)
                    record.update(status="applied", reason="")
                except _Buffered as exc:
                    self._db.execute("ROLLBACK TO v2_reduce_event")
                    self._db.execute("RELEASE v2_reduce_event")
                    c.clear()
                    c.update(before)
                    c.update(phase="reconciling", waiting_reason="missing_event")
                    record["reason"] = str(exc)
                    self._put("v2_event", self._v2_key(c["collaboration_id"], packet["sender_urn"], packet["event_id"]), record)
                    break
                except (ValueError, KeyError, TypeError) as exc:
                    self._db.execute("ROLLBACK TO v2_reduce_event")
                    c.clear()
                    c.update(before)
                    record.update(status="rejected", reason="invalid_state_or_payload")
                self._db.execute("RELEASE v2_reduce_event")
            c.update(in_sequence=packet["sender_sequence"], last_in_event_id=packet["event_id"])
            self._put("v2_event", self._v2_key(c["collaboration_id"], packet["sender_urn"], packet["event_id"]), record)
            if packet["kind"] not in {"receipt", "sync_response"}:
                self._v2_queue(c, "receipt", {"event_id": packet["event_id"]})
        gaps = [r for r in self._all("v2_event") if r["owner_id"] == c["owner_id"]
                and r["packet"]["collaboration_id"] == c["collaboration_id"]
                and r["packet"]["sender_urn"] == c["peer_urn"] and r["status"] == "buffered"]
        if gaps and c.get("closure_reason") != "cancelled":
            c.update(phase="reconciling", waiting_reason="missing_event")
            self._v2_queue(c, "sync_request")
        elif c.get("waiting_reason") == "missing_event":
            if c.get("agreement"):
                c.update(phase="closed" if c["agreement_synced"] else "agreed",
                         waiting_reason=None if c["agreement_synced"] else "agreement_sync")
            elif c.get("withdraw_pending"):
                c.update(phase="reconciling", waiting_reason="withdrawal_decision")
            else:
                c.update(phase="partially_accepted" if any(a["active"] for a in c["acceptances"].values()) else "negotiating",
                         waiting_reason=None)

    def ingest_v2_record(self, message):
        """Called only from Store.ingest_message's authenticated-helper transaction.

        No nested transaction. Unknown peers remain invitations, never owners.
        Returns None for v1 or unrelated text, keeping legacy ingestion intact.
        """
        try:
            packet = json.loads(message.get("text", ""))
        except (ValueError, TypeError, RecursionError):
            return None
        if not isinstance(packet, dict) or packet.get("protocol") != PROTOCOL:
            return None
        validate_event(packet)
        if packet["sender_urn"] != message.get("sender_urn") or packet["recipient_urn"] != self.local_urn:
            raise ValueError("Collaboration identities do not match the authenticated helper")
        if message.get("task_id") not in (None, packet["collaboration_id"]):
            raise ValueError("Wire task association conflicts with the shared collaboration ID")
        c = self._get("v2_collaboration", packet["collaboration_id"])
        if not c:
            if packet["kind"] != "invite" or packet["sender_sequence"] != 1:
                raise ValueError("An unbound collaboration must start with an invitation")
            exact(packet["payload"], {"topic", "fulfillment_mode", "formation_rule"})
            if packet["payload"]["fulfillment_mode"] != "agreement_only" or packet["payload"]["formation_rule"] != "initiator_durable_decision_v1":
                raise ValueError("Unsupported invitation semantics")
            key = identifier(message["message_id"])
            invitation = {"message_id": key, "packet": packet, "digest": digest(packet), "received_at": self.clock()}
            old = self._get("v2_invitation", key)
            if old and old["digest"] != invitation["digest"]:
                raise ValueError("Invitation conflicts with an existing wire record")
            self._put("v2_invitation", key, old or invitation)
            return {"protocol": PROTOCOL, "status": "invitation_recorded_not_authorized"}
        if self._mail_owner() and c["owner_id"] != self._mail_owner():
            return None  # An authenticated peer cannot advance another profile's collaboration.
        if packet["sender_urn"] != c["peer_urn"]:
            raise ValueError("Shared ID does not authorize a different peer")
        record = self._v2_ingest_bound(c, packet)
        self._v2_drain(c)
        self._put("v2_collaboration", c["collaboration_id"], c)
        record = self._v2_record(c, packet["sender_urn"], packet["event_id"])
        return {"protocol": PROTOCOL, "collaboration_id": c["collaboration_id"], "status": record["status"]}

    def collaborations(self, owner_session, task_id=None):
        self._owner(owner_session)
        with self._lock:
            if task_id is not None:
                self._task(task_id, owner_session)
            items = [c for c in self._all("v2_collaboration") if self._belongs(c, owner_session)
                     and (task_id is None or c["task_id"] == task_id)]
            ids = {c["collaboration_id"] for c in items}
            peers = {c["urn"] for c in self._all("contact") if self._belongs(c, owner_session)
                     and (not self.local_urn or self._contact_status(c) == "connected")}
            invitations = [{"message_id": r["message_id"], "collaboration_id": r["packet"]["collaboration_id"],
                            "sender_urn": r["packet"]["sender_urn"], "topic": r["packet"]["payload"]["topic"],
                            "expires_at": r["packet"]["expires_at"], "authority": "peer_statement"}
                           for r in self._all("v2_invitation") if r["packet"]["sender_urn"] in peers
                           and not self._get("v2_collaboration", r["packet"]["collaboration_id"])]
            operations = [self._v2_operation_view(op) for op in self._all("v2_operation")
                          if self._belongs(op, owner_session) and (task_id is None or op["task_id"] == task_id)]
            return {"protocol": PROTOCOL, "collaborations": items, "invitations": invitations,
                    "operations": operations, "authority_level": "peer_attested",
                    "fulfillment_mode": "agreement_only", "calendar_created": False}

    def revoke_collaboration_maintenance(self, collaboration_id, owner_session):
        """Owner-scoped permission reduction; never asks for another approval."""
        self._owner(owner_session)
        with self._transaction():
            c = self._v2_collaboration(collaboration_id, owner_session)
            if c.get("maintenance"):
                c["maintenance"]["revoked"] = True
                c["maintenance"]["revoked_at"] = self.clock()
            c["waiting_reason"] = "maintenance_permission"
            self._put("v2_collaboration", collaboration_id, c)
            self._audit("collaboration_maintenance_revoked", collaboration_id=collaboration_id,
                        task_id=c["task_id"], owner_session=owner_session)
            return {"collaboration_id": collaboration_id, "status": "revoked",
                    "note": "后续维护发送停止；已进入 helper 的消息不能收回。业务委托不因此扩大。"}
