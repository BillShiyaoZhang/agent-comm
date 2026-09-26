"""Durable navigation provenance installed only by a trusted host or bridge.

These records connect facts to a conversation; they never establish consent.
The context is private to a Store and cannot be supplied in runtime tool args.
"""
from contextlib import contextmanager
from contextvars import ContextVar
import hashlib
import json


_source = ContextVar("agent_comm_source_context", default=None)
_KINDS = {"task": "task", "approval": "approval", "v2_collaboration": "collaboration"}


class SourceContextMixin:
    @contextmanager
    def bind_source_context(self, owner_session, context, console_urn):
        self._owner(owner_session)
        token = _source.set((self, self._principal(owner_session), dict(context), console_urn))
        try:
            yield
        finally:
            _source.reset(token)

    def _record_source_context(self, kind, key, body):
        binding = _source.get()
        if kind not in _KINDS or not binding or binding[0] is not self:
            return
        _, owner, context, console_urn = binding
        if not self._belongs(body, owner):
            return
        # Preserve the original creation provenance when another turn updates
        # this object, while connecting that later turn to the same object.
        body.setdefault("source_context", dict(context))
        link = {"owner_id": owner, "console_urn": console_urn, **context,
                "kind": _KINDS[kind], "id": key}
        task_id = self._approval_task_id(body) if kind == "approval" else body.get("task_id")
        if task_id:
            link["task_id"] = task_id
        link_key = hashlib.sha256(json.dumps(link, sort_keys=True).encode()).hexdigest()
        self._put("conversation_link", link_key, link)

    def conversation_links(self, owner_session, console_urn, conversation_id, turn_id):
        self._owner(owner_session)
        with self._lock:
            links = [item for item in self._all("conversation_link")
                     if self._belongs(item, owner_session) and item["console_urn"] == console_urn
                     and item.get("conversation_id") == conversation_id and item.get("turn_id") == turn_id]
            return [{key: item[key] for key in ("kind", "id", "task_id") if key in item} for item in links]
