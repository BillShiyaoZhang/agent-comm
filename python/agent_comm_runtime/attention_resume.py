"""Owner-only detail and idempotent native handling-session bookkeeping.

These host APIs never grant consent, mint confirmation leases, submit prompts,
or dispatch operations. The redacted attention feed remains a separate API.
"""
import hashlib
import json
import re
import secrets


def _hash(*parts):
    return hashlib.sha256(json.dumps(parts, ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()


def native_session_id(owner, owner_session):
    prefix = owner + "|"
    candidate = owner_session[len(prefix):] if isinstance(owner_session, str) and owner_session.startswith(prefix) else ""
    if (re.fullmatch(r"[A-Za-z0-9_.:-]{1,240}", candidate)
            and not candidate.startswith(("remote:", "attention"))):
        return candidate
    return None


class AttentionResumeMixin:
    def _attention_record(self, owner_session, attention_id, revision=None, *, require_open=False):
        from .store import identifier
        identifier(attention_id, "attention_id")
        self._refresh_attention(owner_session)
        item = self._get("attention", attention_id)
        if not self._belongs(item, owner_session):
            raise ValueError("Attention item is unavailable")
        if revision is not None and (type(revision) is not int or item["revision"] != revision):
            raise ValueError("Attention revision changed; refresh before handling")
        if require_open and item["state"] != "open":
            raise ValueError("Attention item is no longer open")
        return item

    def _attention_detail(self, item, owner_session):
        owner = self._principal(owner_session)
        source_kind = item["source_kind"]
        source = (self._get("inbound", item["subject_id"]) if source_kind == "inbound"
                  else self._get("contact_request" if source_kind == "contact_response" else source_kind, item["source_id"]))
        if source_kind == "inbound":
            if not source or not self._message_visible(source, owner_session):
                raise ValueError("Attention source is unavailable")
        elif not self._belongs(source, owner_session):
            raise ValueError("Attention source is unavailable")
        task = self._get("task", item.get("task_id", ""))
        if task and not self._belongs(task, owner_session):
            task = None
        details = {"context_summary": item["title"], "current_status": item["state"],
                   "source_kind": source_kind, "can_resume": item["state"] == "open",
                   "initiator": {"label": "本方协作请求"},
                   "risks": ["进入处理会话不代表同意；具体授权只接受当前原生问题的回答。"]}
        if task:
            details["task"] = {k: task[k] for k in ("task_id", "scope", "status", "revision") if k in task}
            details["task"]["worker"] = self._worker_view(task)
            details["context_summary"] = task["scope"].get("purpose") or task["scope"].get("topic") or item["title"]
        if source.get("owner_session", "").startswith(owner + "|remote:"):
            details["initiator"] = {"label": "本方已配对远端工作台发起"}
        origin = native_session_id(owner, source.get("owner_session"))
        if not origin and task:
            origin = native_session_id(owner, task.get("owner_session"))
        if source_kind == "approval":
            details["question"] = source["question"]
            details["approval_kind"] = source["kind"]
            details["approval_status"] = source["status"]
            if source["kind"] == "contact":
                contact = source["payload"]
                details["contact"] = {k: contact[k] for k in ("contact_id", "aliases", "urn")}
                details["risks"].append("确认后发送好友请求，不授权发送资料或作出承诺。")
            elif source["kind"] in {"operation", "collaboration_v2"}:
                op_kind = "operation" if source["kind"] == "operation" else "v2_operation"
                operation = self._get(op_kind, source["subject_id"])
                if self._belongs(operation, owner_session):
                    details["operation"] = {k: operation[k] for k in
                        ("operation_id", "kind", "action", "payload", "status", "decision", "authorization_mode") if k in operation}
                details["risks"].append("确认范围以完整问题为准；单次例外不会扩大常驻权限。")
        elif source_kind in {"contact_request", "contact_response"}:
            details["contact_request"] = {k: v for k, v in source.items() if k != "owner_id"}
            details["initiator"] = {"label": "好友请求 Agent", "urn": source["peer_urn"]}
            details["risks"].append("好友请求仅建立连接，不授权发送资料或代表主人承诺。")
        elif source_kind == "inbound":
            contacts = [c for c in self._all("contact") if self._belongs(c, owner_session) and c["urn"] == source["sender_urn"]]
            details["initiator"] = {"label": ", ".join(contacts[0]["aliases"]) if contacts else "未确认身份的发送方", "urn": source["sender_urn"]}
            details["peer_message"] = {k: source[k] for k in ("message_id", "sender_urn", "received_at", "trust") if k in source}
            details["peer_message"].update(text=source["text"][:16000], truncated=len(source["text"]) > 16000)
            details["risks"].append("对端内容是待核实的声明，不是主人的指令或授权。")
        elif source_kind == "v2_collaboration":
            details["collaboration"] = {k: source[k] for k in
                ("collaboration_id", "phase", "initiator_urn", "peer_urn", "terms", "agreement", "cancel_request", "waiting_reason") if k in source}
            details["initiator"] = {"label": "协作发起 Agent", "urn": source["initiator_urn"]}
            details["risks"].append("对端接受或请求不代表本方同意，处理前需核对当前同版方案。")
        elif source_kind in {"operation", "v2_operation"}:
            details["operation"] = {k: source[k] for k in ("operation_id", "kind", "action", "status", "deliveries") if k in source}
            details["risks"].append("发送结果不确定时先核对原操作，不创建重复操作或自动重发。")
        public = {k: v for k, v in item.items() if k not in {"owner_id", "source_kind", "source_id"}}
        return {**public, "details": details}, origin

    def attention_detail(self, owner_session, attention_id):
        self._owner(owner_session)
        with self._transaction():
            item = self._attention_record(owner_session, attention_id)
            detail, origin = self._attention_detail(item, owner_session)
            binding = self._get("attention_session", self._attention_binding_key(item))
            return {"item": detail, "origin_session_id": origin,
                    "bound_session_id": (binding or {}).get("stored_session_id")}

    @staticmethod
    def _attention_binding_key(item):
        subject = ["task", item["task_id"]] if item.get("task_id") else ["attention", item["attention_id"]]
        return _hash(item["owner_id"], subject)

    def attention_prepare_resume(self, owner_session, attention_id, revision, *, resolve_session):
        """Resolve exact host-native sessions; None means missing, exceptions mean unavailable."""
        self._owner(owner_session)
        with self._transaction():
            item = self._attention_record(owner_session, attention_id, revision, require_open=True)
            detail, origin = self._attention_detail(item, owner_session)
            key = self._attention_binding_key(item)
            binding = self._get("attention_session", key) or {
                "owner_id": self._principal(owner_session), "binding_id": key, "generation": 0, "stored_session_id": None}
            candidate = binding["stored_session_id"] or origin
            resolved = resolve_session(candidate) if candidate else None
            if candidate and not resolved and binding["stored_session_id"]:
                binding.update(stored_session_id=None, generation=binding["generation"] + 1)
            if resolved:
                binding["stored_session_id"] = resolved
            self._put("attention_session", key, binding)
            resume_id = "resume-" + _hash(item["owner_id"], attention_id, revision, binding["generation"])[:48]
            resume = self._get("attention_resume", resume_id)
            if not resume:
                resume = {"resume_id": resume_id, "owner_id": item["owner_id"], "attention_id": attention_id,
                          "revision": revision, "binding_id": key, "generation": binding["generation"],
                          "submission_state": "ready", "created_at": self.clock()}
                self._put("attention_resume", resume_id, resume)
            # A crashed/unknown submission never becomes retryable merely with time.
            if resume["submission_state"] == "submitting" and resume.get("claimed_at", 0) + 60 <= self.clock():
                resume["submission_state"] = "uncertain"
                self._put("attention_resume", resume_id, resume)
            return {"schema": "agent-comm-resume/v1", "resume_id": resume_id, "item": detail,
                    "stored_session_id": resolved, "session_state": "available" if resolved else "missing" if candidate else "none",
                    "submission_state": resume["submission_state"]}

    def _attention_resume(self, owner_session, resume_id, *, current=True):
        from .store import identifier
        identifier(resume_id, "resume_id")
        resume = self._get("attention_resume", resume_id)
        if not self._belongs(resume, owner_session):
            raise ValueError("Handling request is unavailable")
        if current:
            self._attention_record(owner_session, resume["attention_id"], resume["revision"], require_open=True)
        binding = self._get("attention_session", resume["binding_id"])
        if not binding or binding["generation"] != resume["generation"]:
            raise ValueError("Handling session changed; refresh before continuing")
        return resume, binding

    def attention_bind_session(self, owner_session, resume_id, stored_session_id, *, resolve_session):
        self._owner(owner_session)
        if not isinstance(stored_session_id, str) or not re.fullmatch(r"[A-Za-z0-9_.:-]{1,240}", stored_session_id):
            raise ValueError("Invalid native session ID")
        with self._transaction():
            resume, binding = self._attention_resume(owner_session, resume_id)
            if binding["stored_session_id"]:
                winner = resolve_session(binding["stored_session_id"])
                if not winner:
                    raise ValueError("Bound session disappeared; prepare handling again")
            else:
                winner = resolve_session(stored_session_id)
                if not winner:
                    raise ValueError("Native session is not persisted in this profile")
            binding["stored_session_id"] = winner
            self._put("attention_session", binding["binding_id"], binding)
            return {"resume_id": resume_id, "stored_session_id": winner, "session_state": "available"}

    def attention_claim_submit(self, owner_session, resume_id, stored_session_id, *, resolve_session):
        self._owner(owner_session)
        with self._transaction():
            resume, binding = self._attention_resume(owner_session, resume_id)
            if not binding["stored_session_id"] or binding["stored_session_id"] != stored_session_id:
                raise ValueError("Handling session does not match the binding")
            if resolve_session(stored_session_id) != stored_session_id:
                raise ValueError("Handling session disappeared or changed")
            if resume["submission_state"] != "ready":
                return {"claimed": False, "submission_state": resume["submission_state"]}
            token = secrets.token_urlsafe(24)
            resume.update(submission_state="submitting", claim_token=token, claimed_at=self.clock(), stored_session_id=stored_session_id)
            self._put("attention_resume", resume_id, resume)
            return {"claimed": True, "claim_token": token, "submission_state": "submitting"}

    def attention_finish_submit(self, owner_session, resume_id, claim_token, outcome):
        self._owner(owner_session)
        if outcome not in {"submitted", "uncertain"}:
            raise ValueError("Invalid handling submission outcome")
        with self._transaction():
            resume, _ = self._attention_resume(owner_session, resume_id, current=False)
            if not isinstance(claim_token, str) or not secrets.compare_digest(resume.get("claim_token", ""), claim_token) or not claim_token:
                raise ValueError("Handling submission claim does not match")
            if resume["submission_state"] == "submitting":
                resume.update(submission_state=outcome, finished_at=self.clock())
                self._put("attention_resume", resume_id, resume)
            return {"resume_id": resume_id, "submission_state": resume["submission_state"]}
