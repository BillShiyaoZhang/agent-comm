"""Owner-scoped task discovery and evidence projections.

The journal records local state transitions after this version is installed.
Older transitions cannot be reconstructed from mutable records. Peer content is
read only through the existing owner-review gates.
"""

import base64
import json
import re


_IDENTIFIER = re.compile(r"[A-Za-z0-9._:-]{1,128}\Z")
_JOURNAL_KINDS = {"task", "approval", "operation", "v2_operation"}


def _id(value):
    if not isinstance(value, str) or not _IDENTIFIER.fullmatch(value):
        raise ValueError("Invalid stable identifier")
    return value


def _limit(value, maximum):
    if type(value) is not int or not 1 <= value <= maximum:
        raise ValueError(f"limit must be from 1 through {maximum}")
    return value


def _cursor(value):
    if value is None:
        return None
    if not isinstance(value, str) or len(value) > 256:
        raise ValueError("Invalid task event cursor")
    try:
        pair = json.loads(base64.urlsafe_b64decode(value + "=" * (-len(value) % 4)))
    except (ValueError, UnicodeError, TypeError) as exc:
        raise ValueError("Invalid task event cursor") from exc
    if (not isinstance(pair, list) or len(pair) != 2 or type(pair[0]) not in (int, float)
            or not isinstance(pair[1], str) or len(pair[1]) > 160):
        raise ValueError("Invalid task event cursor")
    return (pair[0], pair[1])


def _encode_cursor(pair):
    return base64.urlsafe_b64encode(json.dumps(list(pair), separators=(",", ":")).encode()).decode().rstrip("=")


class TaskHistoryMixin:
    def _record_task_history(self, kind, key, body, *, previous=None):
        """Called from Store._put inside its existing transaction."""
        if kind not in _JOURNAL_KINDS or previous == body:
            return
        task_id = (body.get("task_id") if kind != "approval" else self._approval_task_id(body))
        owner_id = body.get("owner_id") or self._principal(body.get("owner_session", ""))
        if not task_id or not owner_id:
            return
        status = body.get("status", "unknown")
        # This journal is immutable, while an owner's permission to view peer
        # content can later be revoked. Retain transition metadata only; live
        # projections recheck the current review and blocking gates.
        details = {name: body[name] for name in (
            ("revision", "used_count") if kind == "task" else
            ("kind", "subject_id", "expires_at") if kind == "approval" else
            ("approval_id",) if kind == "operation" else
            ("kind", "approval_id")) if name in body}
        entry = {"kind": kind, "record_id": key, "status": status, "at": self.clock(),
                 "source": "local_store", "details": details,
                 **({"source_context": body["source_context"]} if "source_context" in body else {})}
        self._db.execute("INSERT INTO task_history(owner_id,task_id,body) VALUES(?,?,?)",
                         (owner_id, task_id, json.dumps(entry, ensure_ascii=False, sort_keys=True,
                          separators=(",", ":"), allow_nan=False)))

    def task_list(self, owner_session, *, query="", limit=50, cursor=None):
        self._owner(owner_session)
        if not isinstance(query, str) or len(query) > 120:
            raise ValueError("query must be at most 120 characters")
        _limit(limit, 100)
        if cursor is not None:
            _id(cursor)
        needle = query.casefold().strip()
        with self._lock:
            tasks = sorted((t for t in self._all("task") if self._belongs(t, owner_session)
                            and (cursor is None or t["task_id"] > cursor)
                            and (not needle or needle in " ".join((t["task_id"],
                                 t["scope"].get("topic", ""), t["scope"].get("purpose", ""))).casefold())),
                           key=lambda t: t["task_id"])
        page = tasks[:limit]
        return {"items": [{"task_id": t["task_id"], "topic": t["scope"].get("topic", ""),
                           "purpose": t["scope"].get("purpose", ""), "status": t["status"],
                           "revision": t["revision"], "expires_at": t["scope"]["expires_at"]}
                          for t in page],
                "next_cursor": page[-1]["task_id"] if len(tasks) > limit else None}

    def task_detail(self, owner_session, task_id):
        self._owner(owner_session)
        _id(task_id)
        with self._projection_transaction():
            self._ensure_peer_reviews(owner_session)
            task = self._task(task_id, owner_session)
            approvals = [a for a in self._all("approval") if self._belongs(a, owner_session)
                         and self._approval_task_id(a) == task_id]
            operations = [self._operation_view(o) for o in self._all("operation")
                          if self._belongs(o, owner_session) and o["task_id"] == task_id]
            collaboration = self.collaborations(owner_session, task_id)
            # inbox() enforces connection, blocking, owner and content-review gates.
            messages = self._inbox(owner_session, task_id)
            safe_operation_ids = {o["operation_id"] for o in operations
                                  if "peer_content_unavailable" not in o["reasons"]}
            safe_v2_operation_ids = {o["operation_id"] for o in collaboration["operations"]
                                     if "peer_content_unavailable" not in o["reasons"]}
            approval_views = []
            for approval in approvals:
                view = {k: approval[k] for k in ("approval_id", "kind", "subject_id", "expires_at",
                        "status", "created_at", "source_context") if k in approval}
                safe_question = (approval["kind"] != "operation" or approval["subject_id"] in safe_operation_ids)
                safe_question = safe_question and (approval["kind"] != "collaboration_v2"
                                  or approval["subject_id"] in safe_v2_operation_ids)
                if safe_question:
                    view["question"] = approval["question"]
                else:
                    view["question_redacted"] = True
                approval_views.append(view)
            operation_views = []
            for operation in operations:
                view = {k: operation[k] for k in ("operation_id", "task_id", "status", "decision",
                        "reasons", "approval_id") if k in operation}
                capability = operation["action"].get("capability")
                if capability:
                    view["capability"] = capability
                else:
                    view["content_redacted"] = True
                operation_views.append(view)
            collaboration_views = [{k: c[k] for k in ("collaboration_id", "task_id", "peer_urn",
                                   "phase", "waiting_reason", "joined", "agreement", "terms", "source_context") if k in c}
                                   for c in collaboration["collaborations"]]
            v2_operation_views = [{k: o[k] for k in ("operation_id", "task_id", "collaboration_id", "kind",
                                  "status", "decision", "reasons", "approval_id") if k in o}
                                  for o in collaboration["operations"]]
            message_views = [{k: m[k] for k in ("message_id", "sender_urn", "kind", "received_at", "read") if k in m}
                             | {"text": m.get("text", "")[:2000], "text_truncated": len(m.get("text", "")) > 2000}
                             for m in messages]
            result = {"task": {**task, "worker": self._worker_view(task)}, "operations": [], "approvals": [],
                      "collaboration": {"protocol": collaboration["protocol"], "collaborations": [],
                                        "operations": [], "authority_level": collaboration["authority_level"],
                                        "fulfillment_mode": collaboration["fulfillment_mode"],
                                        "calendar_created": collaboration["calendar_created"]},
                      "messages": [], "coverage": {"complete": False,
                          "journal": "local_transitions_since_upgrade",
                          "approvals": "question_only_while_current_peer_review_allows",
                          "messages": "latest_100_visible_per_store_projection",
                          "truncated": {}}}
            groups = ((result["approvals"], sorted(approval_views, key=lambda a: (a.get("status") not in
                       {"pending", "presenting", "expired"}, -a.get("created_at", 0))) , "approvals"),
                      (result["collaboration"]["collaborations"], collaboration_views, "collaborations"),
                      (result["collaboration"]["operations"], v2_operation_views, "v2_operations"),
                      (result["operations"], operation_views, "operations"),
                      (result["messages"], list(reversed(message_views)), "messages"))
            for bucket, candidates, label in groups:
                for item in candidates:
                    bucket.append(item)
                    if len(json.dumps(result, ensure_ascii=False, separators=(",", ":")).encode()) > 200000:
                        bucket.pop()
                        break
                result["coverage"]["truncated"][label] = len(bucket) < len(candidates)
            result["messages"].reverse()
            return result

    def task_events(self, owner_session, task_id, *, limit=10, cursor=None, turns=()):
        self._owner(owner_session)
        _id(task_id)
        _limit(limit, 20)
        after = _cursor(cursor)
        events = []
        with self._projection_transaction():
            self._ensure_peer_reviews(owner_session)
            self._task(task_id, owner_session)
            for sequence, body in self._db.execute(
                    "SELECT sequence,body FROM task_history WHERE owner_id=? AND task_id=? ORDER BY sequence",
                    (self._principal(owner_session), task_id)):
                item = json.loads(body)
                item["event_id"] = f"journal:{sequence:020d}"
                item["summary"] = f"{item['kind']} {item['status']}"
                events.append(item)
            # v2 packets are immutable protocol evidence, but inbound peer text
            # must pass the same whole-collaboration review gate as state().
            safe_collaborations = {c["collaboration_id"] for c in
                self.collaborations(owner_session, task_id)["collaborations"]}
            for event in self._all("v2_event"):
                if (not self._belongs(event, owner_session) or event.get("task_id") != task_id
                        or event["packet"]["collaboration_id"] not in safe_collaborations):
                    continue
                packet = event["packet"]
                events.append({"event_id": "protocol:" + event["digest"], "kind": "protocol_event",
                    "at": event["recorded_at"], "source": "authenticated_peer" if not event.get("outgoing") else "local_agent",
                    "summary": packet["kind"] + " " + event["status"],
                    "details": {"collaboration_id": packet["collaboration_id"], "event_id": packet["event_id"],
                                "sender_urn": packet["sender_urn"], "kind": packet["kind"],
                                "status": event["status"]}})
                # Routine protocol packets may carry peer-chosen receipt reasons
                # or nested historical bodies. Their raw wire is deliberately
                # hidden by the normal message projection, even after the typed
                # state machine consumes them.
            for message in self._all("inbound"):
                if (self._inbound_local_task(message, owner_session) != task_id
                        or not self._message_visible(message, owner_session)):
                    continue
                events.append({"event_id": "inbound:" + message["message_id"], "kind": "peer_message",
                    "at": message["received_at"], "source": "authenticated_peer",
                    "summary": message.get("kind", "message"),
                    "details": {k: message[k] for k in ("message_id", "sender_urn", "kind", "text", "received_at") if k in message}})
        for turn in turns:
            events.append({"event_id": "turn:" + turn["turn_id"], "kind": "owner_conversation",
                "at": turn["created_at"], "source": "paired_conversation", "summary": turn["status"],
                "details": {k: turn[k] for k in ("conversation_id", "turn_id", "status", "text", "response", "error") if k in turn},
                "mentions": turn.get("mentions", [])})
        events.sort(key=lambda item: (item["at"], item["event_id"]))
        if after is not None:
            events = [item for item in events if (item["at"], item["event_id"]) > after]
        # The outer control response has a 250 KiB cap. Keep a page below it
        # while retaining exact event contents and a stable resume position.
        page = []
        page_bytes = 0
        for item in events:
            item_bytes = len(json.dumps(item, ensure_ascii=False, separators=(",", ":")).encode())
            if item_bytes > 220000:
                # A huge model reply or peer wire body remains available from
                # its source record; a single event must not make paging fail.
                details = dict(item.get("details", {}))
                for field in ("response", "text", "packet", "question"):
                    if field in details:
                        details[field] = str(details[field])[:8000]
                        details[field + "_truncated"] = True
                item = {**item, "details": details}
                item_bytes = len(json.dumps(item, ensure_ascii=False, separators=(",", ":")).encode())
            if len(page) >= limit or (page and page_bytes + item_bytes > 220000):
                break
            page.append(item)
            page_bytes += item_bytes
        return {"items": page, "next_cursor": _encode_cursor((page[-1]["at"], page[-1]["event_id"]))
                if len(events) > len(page) else None,
                "coverage": {"complete": False, "journal": "local_transitions_since_upgrade",
                             "older_state_changes": "unavailable", "owner_turns": "explicit_mentions_or_trusted_links",
                             "peer_messages": "currently_visible_after_owner_review",
                             "delivery": "helper_acceptance_is_not_business_completion"}}
