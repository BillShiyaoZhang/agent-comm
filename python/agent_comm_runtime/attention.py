"""Durable owner-scoped attention projections; reading never grants consent.

All writes share the business Store transaction. A feed contains the latest
revision of each item, including terminal tombstones, not model-written notices.
"""
import hashlib
import json


MAX_CURSOR = 9007199254740991


def _key(*parts):
    return hashlib.sha256(json.dumps(parts, ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()


class AttentionMixin:
    def _attention_put(self, owner, source_kind, source_id, *, kind, subject_id,
                       title, state="open", task_id=None, source_revision=1,
                       target_kind="task", summary="", expires_at=None, approval_id=None, created_at=None):
        if not owner:
            return
        key = "attention-" + _key(owner, source_kind, source_id)[:48]
        previous = self._get("attention", key)
        value = {"attention_id": key, "owner_id": owner, "kind": kind,
                 "subject_id": subject_id, "source_revision": source_revision,
                 "state": state, "title": title, "safe_summary": summary,
                 "target": {"kind": target_kind, "id": task_id if target_kind == "task" and task_id else subject_id},
                 "source_kind": source_kind, "source_id": source_id}
        if task_id is not None:
            value["task_id"] = task_id
        if expires_at is not None:
            value["expires_at"] = expires_at
        if approval_id is not None:
            value["approval_id"] = approval_id
        old_content = {k: v for k, v in (previous or {}).items() if k not in {"revision", "created_at", "updated_at"}}
        if old_content == value:
            return
        counter_key = _key(owner)
        counter = self._get("attention_counter", counter_key) or {"revision": 0}
        revision = counter["revision"] + 1
        if revision > MAX_CURSOR:
            raise ValueError("Attention revision exhausted")
        value.update(revision=revision, created_at=(previous or {}).get("created_at", self.clock() if created_at is None else created_at), updated_at=self.clock())
        self._put("attention_counter", counter_key, {"revision": revision})
        self._put("attention", key, value)

    def _attention_approval(self, approval, *, refresh=False):
        owner = self._principal(approval.get("owner_session", ""))
        approval_id = approval["approval_id"]
        approval_kind = approval["kind"]
        task_id = self._approval_task_id(approval)
        state = "open"
        if approval["status"] in {"approved", "denied"}:
            state = "resolved"
        elif refresh and not self._approval_current(approval):
            state = "superseded"
            task = self._get("task", task_id) if task_id else None
            if task and (task.get("status") == "revoked" or not self._live(task)):
                state = "expired" if task.get("status") != "revoked" else "superseded"
        # A presentation lease can expire while its underlying decision remains
        # valid and can be presented in a new native session. Never close it on
        # that short UI deadline alone.
        expires = None
        if task_id:
            task = self._get("task", task_id)
            if task:
                from .store import instant
                expires = instant(task["scope"]["expires_at"])
        if approval_kind == "collaboration_v2":
            op = self._get("v2_operation", approval["subject_id"])
            if op:
                expires = op["expires_at"]
        title = {"contact": "联系人绑定需要你确认", "task": "协作委托需要你确认",
                 "operation": "协作动作需要你确认", "collaboration_v2": "双边协作需要你确认"}.get(approval_kind, "有一项协作需要你确认")
        self._attention_put(owner, "approval", approval_id, kind="owner_decision_required",
                            subject_id=approval_id, task_id=task_id, title=title, state=state,
                            source_revision=approval["fingerprint"], target_kind="approval",
                            summary="请在原生渠道查看当前确切动作并决定。", expires_at=expires, approval_id=approval_id,
                            created_at=approval.get("created_at"))

    def _attention_inbound(self, message, owner_session=None):
        # Visibility uses the same confirmed local contacts as Store.inbox.
        contacts = [c for c in self._all("contact") if c.get("urn") == message.get("sender_urn")
                    and (owner_session is None or self._belongs(c, owner_session))]
        owners = {c.get("owner_id", self._principal(c.get("owner_session", ""))) for c in contacts}
        owners.discard("")
        packet = None
        try:
            packet = json.loads(message["text"])
        except (ValueError, TypeError, RecursionError):
            pass
        if isinstance(packet, dict) and packet.get("protocol") == "agent-comm-collaboration/v2":
            event_kind = packet.get("kind")
            if event_kind not in {"invite", "proposal", "change_request", "withdraw", "cancel_request"}:
                return
        else:
            event_kind = None
        for owner in owners:
            task_id = self._inbound_local_task(message, owner)
            if task_id is False:
                continue
            c = self._get("v2_collaboration", packet.get("collaboration_id", "")) if event_kind else None
            state = "resolved" if event_kind == "invite" and c else "open"
            expires = None
            if event_kind == "invite":
                from .store import instant
                expires = instant(packet["expires_at"])
                if state == "open" and expires <= self.clock():
                    state = "expired"
            self._attention_put(owner, "inbound", _key(message["sender_urn"], message["message_id"]),
                                kind="new_collaboration_request" if event_kind == "invite" else "peer_message_received",
                                subject_id=message["message_id"], title="收到新的协作请求" if event_kind == "invite" else "收到对端协作消息",
                                target_kind="inbox", task_id=task_id, state=state, expires_at=expires,
                                summary="对端消息已保存；内容与授权依据需要分别核实。",
                                created_at=message.get("received_at"))

    def _attention_on_write(self, record_kind, key, record):
        if record_kind == "approval":
            self._attention_approval(record)
        elif record_kind == "inbound":
            self._attention_inbound(record)
        elif record_kind == "v2_collaboration":
            self._attention_collaboration(record)
        elif record_kind in {"operation", "v2_operation"}:
            uncertain = any(d.get("status") == "uncertain" for d in record.get("deliveries", [])) or record.get("status") == "uncertain"
            # A hard crash between reservation and the local result commit can
            # leave "pending" even when the helper accepted the message. Expose
            # the uncertainty for inspection; do not infer success or re-send.
            stale_reservation = (record.get("status") == "sending"
                and any(d.get("status") == "pending" for d in record.get("deliveries", []))
                and record.get("reserved_at", record.get("created_at", self.clock())) + 30 <= self.clock())
            uncertain = uncertain or stale_reservation
            owner = record.get("owner_id") or self._principal(record.get("owner_session", ""))
            attention_key = "attention-" + _key(owner, record_kind, key)[:48]
            if uncertain or self._get("attention", attention_key):
                self._attention_put(owner, record_kind, key, kind="needs_recovery", subject_id=key,
                                    task_id=record.get("task_id"), title="协作发送结果需要核对",
                                    state="open" if uncertain else "resolved", source_revision=record.get("hash", 1),
                                    summary="请核对原操作结果；不要创建新的重复操作。")

    def _attention_collaboration(self, collaboration):
        """Project actionable local protocol facts, excluding routine receipts."""
        c = collaboration
        owner, subject = c["owner_id"], c["collaboration_id"]
        phase, reason = c["phase"], c.get("waiting_reason")
        own_acceptance = c.get("acceptances", {}).get(self.local_urn, {})
        request = c.get("cancel_request")
        kind, title, summary = None, "", ""
        if phase == "closed":
            kind, title = "collaboration_completed", "双边协作状态已确认"
            summary = "双方已确认取消约定。" if c.get("closure_reason") == "cancelled" else "双方已同步同版约定；本次只协调方案，未创建日历。"
        elif reason in {"maintenance_permission", "maintenance_budget", "event_chain_conflict", "missing_event"}:
            kind, title = "needs_recovery", "协作需要恢复或核对"
            summary = "请在原生渠道查看当前等待原因并恢复此事项。"
        elif (request and request["sender_urn"] != self.local_urn) or (
                c.get("joined") and not c.get("agreement") and not c.get("withdraw_pending") and
                ((c.get("terms") and not own_acceptance.get("active")) or
                 (not c.get("terms") and c["initiator_urn"] == self.local_urn))):
            kind, title = "needs_response", "协作等待本方处理"
            summary = "请查看当前方案或取消请求；需要授权时会另行展示原生确认。"
        key = "attention-" + _key(owner, "v2_collaboration", subject)[:48]
        previous = self._get("attention", key)
        if not kind and not previous:
            return
        state = "open" if kind else "resolved"
        task = self._get("task", c["task_id"])
        cancellation_decision = request and request["sender_urn"] != self.local_urn and c.get("agreement")
        if kind == "needs_response" and not cancellation_decision and (not task or not self._live(task)):
            state = "superseded" if task and task.get("status") == "revoked" else "expired"
        if not kind:
            kind, title, summary = previous["kind"], previous["title"], previous["safe_summary"]
        source = (_key(phase, (c.get("agreement") or {}).get("agreement_id"), c.get("closure_reason"))
                  if phase == "closed" else
                  _key(phase, reason, c.get("terms"), request, c.get("closure_reason"), bool(own_acceptance.get("active"))))
        self._attention_put(owner, "v2_collaboration", subject, kind=kind, title=title,
                            summary=summary, state=state, task_id=c["task_id"], subject_id=subject,
                            source_revision=source, created_at=c.get("created_at"))

    def _refresh_attention(self, owner_session):
        for approval in self._all("approval"):
            if self._belongs(approval, owner_session):
                self._attention_approval(approval, refresh=True)
        # Additive upgrade and newly confirmed contact: backfill the durable
        # inbox without manufacturing a new item on subsequent polls.
        for message in self._inbox(owner_session, None):
            self._attention_inbound(message, owner_session)
        for c in self._all("v2_collaboration"):
            if self._belongs(c, owner_session):
                self._attention_collaboration(c)
        for kind in ("operation", "v2_operation"):
            for operation in self._all(kind):
                if self._belongs(operation, owner_session):
                    self._attention_on_write(kind, operation["operation_id"], operation)

    def attention(self, owner_session, after=0, limit=100):
        self._owner(owner_session)
        if type(after) is not int or not 0 <= after <= MAX_CURSOR or type(limit) is not int or not 1 <= limit <= 100:
            raise ValueError("Attention requires a nonnegative integer cursor and a limit of 1–100")
        with self._transaction():
            self._refresh_attention(owner_session)
            owner = self._principal(owner_session)
            high = (self._get("attention_counter", _key(owner)) or {"revision": 0})["revision"]
            if after > high:
                raise ValueError("Attention cursor is ahead of this owner's feed")
            items = sorted((item for item in self._all("attention") if item["owner_id"] == owner and item["revision"] > after), key=lambda item: item["revision"])
            has_more = len(items) > limit
            page = items[:limit]
            return {"schema": "agent-comm-attention/v1",
                    "items": [{k: v for k, v in item.items() if k not in {"owner_id", "source_kind", "source_id"}} for item in page],
                    "cursor": page[-1]["revision"] if has_more else high, "has_more": has_more}
