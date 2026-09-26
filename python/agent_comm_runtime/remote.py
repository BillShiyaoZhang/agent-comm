"""Paired, durable control RPC over the existing authenticated helper mailbox.

Pairing is a LOCAL administrator operation. Neither a Web registration nor a
request's claimed owner establishes authority. Explicitly scoped pairings can
record owner decisions, never infer consent from conversation or transport ACKs.
"""
from contextlib import contextmanager
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
import sqlite3
import threading
import time
from .identity import validate_urn

PROTOCOL = "agent-comm-control/v1"
READ_METHODS = ("capabilities", "contacts.list", "collaboration.state", "inbox.list", "attention.list", "contacts.requests")
WRITE_METHODS = ("contacts.add", "contacts.respond", "messages.send", "inbox.mark_read", "approval.respond")
CONVERSATION_METHODS = ("conversation.send", "conversation.get")
CONTROL_KINDS = {"control.request", "control.response"}


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False)


def identity(value):
    return validate_urn(value)


def identifier(value):
    if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9._:-]{1,128}", value):
        raise ValueError("Invalid stable identifier")
    return value


def instant(value):
    if not isinstance(value, str):
        raise ValueError("A timezone-aware RFC3339 deadline is required")
    result = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if result.tzinfo is None:
        raise ValueError("Deadline must include a timezone")
    return result.timestamp()


def hermes_principal(profile):
    return "hermes-native-" + hashlib.sha256(str(Path(profile).expanduser().resolve()).encode()).hexdigest()


class RemoteError(ValueError):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code


class RemoteBridge:
    def __init__(self, path, store, local_urn, *, clock=time.time, conversations=False, bound_principal=None):
        self.path = Path(path).expanduser().resolve()
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.store = store
        self.local_urn = identity(local_urn)
        self.clock = clock
        self.conversations = conversations
        self.bound_principal = bound_principal
        if store is not None and bound_principal is not None:
            store.register_owner(bound_principal)
        self.handlers = {}
        self._lock = threading.RLock()
        self._db = sqlite3.connect(self.path, check_same_thread=False, isolation_level=None)
        self._db.execute("PRAGMA journal_mode=WAL")
        self._db.execute("PRAGMA synchronous=FULL")
        self._db.execute("CREATE TABLE IF NOT EXISTS remote_records(kind TEXT,id TEXT,body TEXT NOT NULL,PRIMARY KEY(kind,id))")

    def close(self):
        with self._lock:
            self._db.close()

    @contextmanager
    def _transaction(self):
        with self._lock:
            self._db.execute("BEGIN IMMEDIATE")
            try:
                yield
                self._db.execute("COMMIT")
            except BaseException:
                self._db.execute("ROLLBACK")
                raise

    def _get(self, kind, key):
        row = self._db.execute("SELECT body FROM remote_records WHERE kind=? AND id=?", (kind, key)).fetchone()
        return json.loads(row[0]) if row else None

    def _all(self, kind):
        return [json.loads(r[0]) for r in self._db.execute("SELECT body FROM remote_records WHERE kind=? ORDER BY id", (kind,))]

    def _put(self, kind, key, value):
        self._db.execute("INSERT INTO remote_records VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body",
                         (kind, key, canonical(value)))

    def pair(self, console_urn, owner_principal, methods, expires_at):
        """Local-only administrator API, intentionally absent from RPC methods."""
        identity(console_urn)
        if not isinstance(owner_principal, str) or not owner_principal or "|" in owner_principal or len(owner_principal) > 256:
            raise ValueError("Provide a local owner principal, not a remote session claim")
        if (not isinstance(methods, (list, tuple)) or not methods or
                any(not isinstance(m, str) or not re.fullmatch(r"[a-z][a-z0-9_.]{0,79}", m) for m in methods)):
            raise ValueError("List explicit allowed RPC methods")
        if instant(expires_at) <= self.clock():
            raise ValueError("Pairing deadline must be in the future")
        pairing = {"console_urn": console_urn, "owner_principal": owner_principal, "methods": sorted(set(methods)),
                   "expires_at": expires_at, "revoked": False, "paired_at": self.clock()}
        if self.store is not None:
            self.store.register_owner(owner_principal)
        with self._transaction():
            self._put("pairing", console_urn, pairing)
        return pairing

    def revoke(self, console_urn):
        identity(console_urn)
        with self._transaction():
            pairing = self._get("pairing", console_urn)
            if not pairing:
                raise ValueError("No pairing exists for this console")
            pairing["revoked"] = True
            self._put("pairing", console_urn, pairing)
        return {"console_urn": console_urn, "revoked": True}

    def pairings(self):
        with self._transaction():
            return self._all("pairing")

    def register_handler(self, method, handler):
        if method in {*READ_METHODS, *WRITE_METHODS, *CONVERSATION_METHODS} or not callable(handler):
            raise ValueError("Built-in methods cannot be overridden; extension handler must be callable")
        self.handlers[method] = handler

    def _authorized(self, console, method):
        pairing = self._get("pairing", console)
        if not pairing or pairing["revoked"] or instant(pairing["expires_at"]) <= self.clock():
            raise RemoteError("not_paired", "This console has no current local pairing")
        if self.bound_principal is not None and pairing["owner_principal"] != self.bound_principal:
            raise RemoteError("owner_mismatch", "The local pairing belongs to a different host profile")
        if method not in pairing["methods"]:
            raise RemoteError("method_not_allowed", "This method is outside the local pairing scope")
        return pairing

    def _parse(self, message):
        if not isinstance(message, dict) or message.get("kind") != "control.request":
            raise ValueError("Not a control request")
        text = message.get("text")
        if not isinstance(text, str) or len(text.encode()) > 50000:
            raise ValueError("Invalid control request size")
        try:
            request = json.loads(text)
        except RecursionError as exc:
            # A bounded byte count still permits thousands of nested arrays.
            # Normalize decoder exhaustion to an invalid individual message.
            raise ValueError("Control request nesting exceeds 32 containers") from exc
        pending = [(request, 0)]
        while pending:
            value, depth = pending.pop()
            if isinstance(value, (dict, list)):
                if depth >= 32:
                    raise ValueError("Control request nesting exceeds 32 containers")
                children = value.values() if isinstance(value, dict) else value
                pending.extend((child, depth + 1) for child in children)
        fields = {"protocol", "type", "request_id", "method", "params", "agent_urn", "console_urn", "deadline"}
        if not isinstance(request, dict) or set(request) != fields:
            raise ValueError("Invalid control request fields")
        request_id = identifier(request["request_id"])
        if (request["protocol"] != PROTOCOL or request["type"] != "request" or
                not isinstance(request["method"], str) or len(request["method"]) > 80 or not isinstance(request["params"], dict)):
            raise ValueError("Invalid control protocol")
        if identity(request["console_urn"]) != message.get("sender_urn") or identity(request["agent_urn"]) != self.local_urn:
            raise ValueError("Control request identity differs from authenticated helper routing")
        if (message.get("message_id") != request_id or message.get("conversation_id") != "control:" + request_id
                or message.get("deadline") != request["deadline"]):
            raise ValueError("Control envelope correlation mismatch")
        deadline = instant(request["deadline"])
        if deadline > self.clock() + 300:
            raise ValueError("Control request lifetime exceeds 300 seconds")
        return request

    @staticmethod
    def _params(params, required=(), optional=()):
        if set(params) - set(required) - set(optional) or set(required) - set(params):
            raise RemoteError("invalid_params", "Unexpected or missing method parameters")

    def _invoke(self, request, pairing):
        method, params = request["method"], request["params"]
        # Store separates profile visibility from exact native approval sessions.
        owner = pairing["owner_principal"] + "|remote:" + hashlib.sha256(pairing["console_urn"].encode()).hexdigest()[:24]
        if method == "capabilities":
            self._params(params)
            names = [*READ_METHODS, *WRITE_METHODS, *CONVERSATION_METHODS, *self.handlers]
            available = {*READ_METHODS, *WRITE_METHODS, *self.handlers, *(CONVERSATION_METHODS if self.conversations else ())}
            return {"protocol": PROTOCOL, "methods": [{"name": name,
                "available": name in available and name in pairing["methods"],
                **({"reason": "Not enabled by this adapter or local pairing"} if name not in available or name not in pairing["methods"] else {})}
                for name in names], "pairing": {"expires_at": pairing["expires_at"]}}
        if method in WRITE_METHODS:
            required = {"contacts.add": ("contact_id", "aliases", "urn"), "approval.respond": ("approval_id", "decision"),
                        "contacts.respond": ("request_id", "decision"), "messages.send": ("recipient_urn", "text"),
                        "inbox.mark_read": ("message_id",)}
            optional = {"contacts.respond": ("contact_id", "aliases"), "messages.send": ("message_id",)}
            self._params(params, required=required[method], optional=optional.get(method, ()))
            key = hashlib.sha256((request["console_urn"] + "\0" + request["request_id"]).encode()).hexdigest()
            return self.store.remote_mutation(method, params, owner, request_key=key,
                                              fingerprint=hashlib.sha256(canonical(request).encode()).hexdigest(),
                                              valid_until=min(instant(request["deadline"]), instant(pairing["expires_at"])))
        if method == "contacts.requests":
            self._params(params)
            return self.store.contact_requests(owner)
        if method == "contacts.list":
            self._params(params)
            return {"contacts": self.store.state(owner)["contacts"]}
        if method == "attention.list":
            self._params(params, optional=("after", "limit"))
            self._sync_conversation_events(pairing)
            return self.store.attention(owner, params.get("after", 0), params.get("limit", 100))
        if method in {"collaboration.state", "inbox.list"}:
            self._params(params, optional=("task_id",))
            task_id = params.get("task_id")
            if task_id is not None:
                identifier(task_id)
            return self.store.state(owner, task_id) if method == "collaboration.state" else self.store.inbox(owner, task_id)
        if method == "conversation.send" and self.conversations:
            self._params(params, required=("text",), optional=("conversation_id",))
            text = params["text"]
            if not isinstance(text, str) or not text.strip() or len(text.encode()) > 24000:
                raise RemoteError("invalid_params", "Conversation text must contain 1–24000 UTF-8 bytes")
            if sum(j["status"] in {"submitted", "running"} and j["console_urn"] == pairing["console_urn"]
                   for j in self._all("turn")) >= 100:
                raise RemoteError("queue_full", "This console already has 100 pending turns; wait before submitting more")
            conversation_id = identifier(params.get("conversation_id", request["request_id"]))
            turn_id = "turn-" + hashlib.sha256((pairing["console_urn"] + "\0" + request["request_id"]).encode()).hexdigest()[:40]
            job = {"turn_id": turn_id, "conversation_id": conversation_id, "console_urn": pairing["console_urn"],
                   "owner_principal": pairing["owner_principal"], "status": "submitted", "text": text,
                   "response": None, "error": None, "created_at": self.clock(), "updated_at": self.clock()}
            self._put("turn", turn_id, job)
            return {"status": "submitted", "conversation_id": conversation_id, "turn_id": turn_id}
        if method == "conversation.get" and self.conversations:
            self._params(params, required=("conversation_id",))
            conversation_id = identifier(params["conversation_id"])
            jobs = [j for j in self._all("turn") if j["console_urn"] == pairing["console_urn"]
                    and j["owner_principal"] == pairing["owner_principal"] and j["conversation_id"] == conversation_id]
            keys = ("turn_id", "status", "text", "response", "error", "created_at", "updated_at")
            latest = sorted(jobs, key=lambda j: (j["created_at"], j["turn_id"]))[-100:]
            return {"conversation_id": conversation_id, "turns": [{**{k: j[k] for k in keys},
                    "related": self.store.conversation_links(owner, pairing["console_urn"], conversation_id, j["turn_id"]) if self.store else []}
                    for j in latest], "history": {"limit": 100, "returned": len(latest), "truncated": len(jobs) > 100}}
        if method in self.handlers:
            # The generic owner action route can prepare a message without a
            # caller-generated ID. Derive it from the authenticated request so
            # a crash between Store commit and response caching cannot create
            # two approvals for the same submitted owner action.
            if method == "collaboration.execute" and params.get("action") == "prepare_message" and "message_id" not in params:
                params = {**params, "message_id": "web-message-" + hashlib.sha256(
                    (request["console_urn"] + "\0" + request["request_id"]).encode()).hexdigest()[:40]}
            if method == "collaboration.execute":
                source_conversation_id = params.get("source_conversation_id")
                source_context = {"origin": "paired_control", "request_id": request["request_id"]}
                if source_conversation_id is not None:
                    identifier(source_conversation_id)
                    if not any(j["console_urn"] == pairing["console_urn"] and j["owner_principal"] == pairing["owner_principal"]
                               and j["conversation_id"] == source_conversation_id for j in self._all("turn")):
                        raise RemoteError("invalid_source_context", "The source conversation is not owned by this pairing")
                    source_context["conversation_id"] = source_conversation_id
                    params = {k: v for k, v in params.items() if k != "source_conversation_id"}
                return self.handlers[method](params, owner,
                    source_context=source_context, source_console_urn=pairing["console_urn"],
                    request_key=hashlib.sha256((request["console_urn"] + "\0" + request["request_id"]).encode()).hexdigest(),
                    fingerprint=hashlib.sha256(canonical(request).encode()).hexdigest(),
                    valid_until=min(instant(request["deadline"]), instant(pairing["expires_at"])))
            return self.handlers[method](params, owner)
        raise RemoteError("unsupported_method", "The agent adapter has no trusted handler for this method")

    def _response(self, request, *, result=None, error=None):
        payload = {k: request[k] for k in ("protocol", "request_id", "method", "agent_urn", "console_urn", "deadline")}
        payload["type"] = "response"
        payload["error" if error else "result"] = error if error else result
        text = canonical(payload)
        if len(text.encode()) > 250000:
            return self._response(request, error={"code": "result_too_large", "message": "Narrow the requested task or conversation"})
        return {"message_id": "control-response-" + hashlib.sha256(text.encode()).hexdigest()[:48],
                "recipient_urn": request["console_urn"], "kind": "control.response", "in_reply_to": request["request_id"],
                "conversation_id": "control:" + request["request_id"], "deadline": request["deadline"], "text": text}

    def handle(self, message):
        """Durably resolve a valid request; caller sends the result before ACK."""
        request = self._parse(message)
        if instant(request["deadline"]) <= self.clock():
            return None
        key = hashlib.sha256((request["console_urn"] + "\0" + request["request_id"]).encode()).hexdigest()
        fingerprint = hashlib.sha256(canonical(request).encode()).hexdigest()
        with self._transaction():
            try:
                # Re-check before cached replies: revocation also blocks replayed disclosure.
                pairing = self._authorized(request["console_urn"], request["method"])
                old = self._get("request", key)
                if old:
                    if old["fingerprint"] != fingerprint or old["owner_principal"] != pairing["owner_principal"]:
                        raise RemoteError("request_conflict", "This request ID was already used for different contents or owner")
                    return old["response"]
                result = self._invoke(request, pairing)
                response = self._response(request, result=result)
                self._put("request", key, {"fingerprint": fingerprint, "owner_principal": pairing["owner_principal"], "response": response})
                return response
            except (RemoteError, ValueError, TypeError, KeyError) as exc:
                return self._response(request, error={"code": getattr(exc, "code", "invalid_params"), "message": str(exc)[:500]})

    def process(self, message, transport):
        response = self.handle(message)
        if self.store is not None:
            self.store.flush_social_outbox(transport)
        with self.delivery(message, response) as response:
            if response is not None:
                managed_store = getattr(transport, "store_managed_control", None)
                result = managed_store(response) if managed_store is not None else transport.store(response)
                if result.get("success") is not True or result.get("message_id") != response["message_id"]:
                    raise ValueError("Helper did not accept the correlated control response")
        transport.ack([message["message_id"]])
        return response

    @contextmanager
    def delivery(self, message, response):
        """Linearize disclosure with local revoke/re-pair, including other processes.

        Keep this context open through the bounded helper store call. A revoke
        that has returned can never be followed by a response under its old grant.
        ACK is outside the transaction and remains retryable.
        """
        request = self._parse(message)
        with self._transaction():
            if instant(request["deadline"]) <= self.clock():
                response = None
            elif response is not None and "result" in json.loads(response["text"]):
                try:
                    pairing = self._authorized(request["console_urn"], request["method"])
                    key = hashlib.sha256((request["console_urn"] + "\0" + request["request_id"]).encode()).hexdigest()
                    cached = self._get("request", key)
                    if not cached or cached["owner_principal"] != pairing["owner_principal"]:
                        raise RemoteError("owner_changed", "Local pairing changed before delivery")
                except RemoteError as exc:
                    response = self._response(request, error={"code": exc.code, "message": str(exc)})
            yield response

    def select_inbox_batch(self, messages, limit=100):
        """Persistent round-robin cursor; poison prefixes cannot starve later IDs."""
        if type(limit) is not int or not 1 <= limit <= 100:
            raise ValueError("Inbox batch limit must be from 1 through 100")
        ordered = sorted((m for m in messages if isinstance(m, dict) and isinstance(m.get("message_id"), str)),
                         key=lambda m: m["message_id"])
        if not ordered:
            return []
        with self._transaction():
            cursor = self._get("cursor", "mailbox") or {"last_id": ""}
            after = [m for m in ordered if m["message_id"] > cursor["last_id"]]
            before = [m for m in ordered if m["message_id"] <= cursor["last_id"]]
            batch = (after + before)[:limit]
            self._put("cursor", "mailbox", {"last_id": batch[-1]["message_id"]})
            return batch

    def claim_turn(self):
        changed = []
        claimed = None
        with self._transaction():
            # Serialize claims across workers/processes, not just in the Hermes
            # async loop. A running turn must finish or be marked interrupted.
            if any(j["status"] == "running" for j in self._all("turn")):
                return None
            for job in sorted(self._all("turn"), key=lambda j: (j["created_at"], j["turn_id"])):
                if job["status"] != "submitted":
                    continue
                try:
                    pairing = self._authorized(job["console_urn"], "conversation.send")
                    if pairing["owner_principal"] != job["owner_principal"]:
                        raise RemoteError("owner_changed", "Local pairing owner changed")
                except RemoteError:
                    job.update(status="failed", error="Pairing expired, changed or revoked before execution", updated_at=self.clock())
                    self._put("turn", job["turn_id"], job)
                    changed.append(job)
                    continue
                job.update(status="running", updated_at=self.clock())
                self._put("turn", job["turn_id"], job)
                claimed = job
                changed.append(job)
                break
        for job in changed:
            if self.store:
                self.store.record_conversation_event(job)
        return claimed

    def finish_turn(self, turn_id, *, response=None, error=None, interrupted=False):
        with self._transaction():
            job = self._get("turn", turn_id)
            if not job or job["status"] != "running":
                return
            if response is not None and (not isinstance(response, str) or len(response.encode()) > 200000):
                error, response = "Host response exceeded the supported text size", None
            job.update(status="interrupted" if interrupted else "failed" if error else "completed",
                       response=response, error=error, updated_at=self.clock())
            self._put("turn", turn_id, job)
        if self.store:
            self.store.record_conversation_event(job)

    def recover_interrupted_turns(self):
        """A crash cannot prove whether host tools ran; never silently replay them."""
        with self._transaction():
            for job in self._all("turn"):
                if job["status"] == "running":
                    job.update(status="interrupted", error="Agent restarted during this turn; execution outcome is uncertain",
                               updated_at=self.clock())
                    self._put("turn", job["turn_id"], job)
        self._sync_conversation_events()

    def _sync_conversation_events(self, pairing=None):
        """Repair only missing projections after commit; never replay a turn."""
        if self.store is None:
            return
        with self._lock:
            jobs = self._all("turn")
        for job in jobs:
            if pairing and (job["console_urn"] != pairing["console_urn"] or job["owner_principal"] != pairing["owner_principal"]):
                continue
            self.store.record_conversation_event(job)
