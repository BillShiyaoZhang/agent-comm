"""Hermes adapter for the local agent-comm helper's durable mailbox."""

import asyncio
import hashlib
import json
import logging
import os
import re
import sqlite3
import threading
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Optional
from urllib.parse import urlsplit

import aiohttp
from gateway.config import Platform
from gateway.platforms.base import BasePlatformAdapter, MessageEvent, MessageType, SendResult
from gateway.platforms.event import ProcessingOutcome
from hermes_constants import get_hermes_home

logger = logging.getLogger(__name__)
WIRE_FIELDS = ("conversation_id", "in_reply_to", "task_id", "kind", "deadline", "hop_limit")
DEFAULT_HOP_LIMIT = 8


def canonical_remote_route(job):
    # Both principal and paired console participate, so a reused public
    # conversation_id never inherits another local owner's conversation.
    encoded = json.dumps([job["owner_principal"], job["console_urn"], job["conversation_id"]], separators=(",", ":"))
    return "remote-" + hashlib.sha256(encoded.encode()).hexdigest()


class ReceiptStore:
    """Completion receipts and reply routes; pending plaintext remains in the helper."""

    def __init__(self, path):
        path = Path(path).expanduser().resolve()
        path.parent.mkdir(parents=True, exist_ok=True)
        self._lock = threading.Lock()
        self._db = sqlite3.connect(path, check_same_thread=False)
        self._db.execute("PRAGMA journal_mode=WAL")
        self._db.execute("PRAGMA synchronous=FULL")
        self._db.execute("""CREATE TABLE IF NOT EXISTS receipts (
            message_id TEXT PRIMARY KEY, route TEXT NOT NULL, outcome TEXT,
            completed_at TEXT)""")
        self._db.commit()

    def remember(self, message):
        route = {key: message[key] for key in ("sender_urn", *WIRE_FIELDS) if key in message}
        route["_text_sha256"] = hashlib.sha256(message["text"].encode()).hexdigest()
        serialized = json.dumps(route, sort_keys=True)
        with self._lock, self._db:
            row = self._db.execute("SELECT route, outcome FROM receipts WHERE message_id=?",
                                   (message["message_id"],)).fetchone()
            if row:
                if row[0] != serialized:
                    raise ValueError("message_id was replayed with different routing metadata")
                return row[1]
            self._db.execute("INSERT INTO receipts(message_id, route) VALUES (?, ?)",
                             (message["message_id"], serialized))
        return None

    def route(self, message_id):
        with self._lock:
            row = self._db.execute("SELECT route FROM receipts WHERE message_id=?", (message_id,)).fetchone()
        return json.loads(row[0]) if row else None

    def complete(self, message_id, outcome):
        with self._lock, self._db:
            self._db.execute("UPDATE receipts SET outcome=?, completed_at=? WHERE message_id=?",
                             (outcome, datetime.now(timezone.utc).isoformat(), message_id))

    def close(self):
        with self._lock:
            self._db.close()


def _expired(deadline):
    if deadline is None:
        return False
    if not isinstance(deadline, str):
        raise ValueError("deadline must be an RFC3339 timestamp")
    instant = datetime.fromisoformat(deadline.replace("Z", "+00:00"))
    if instant.tzinfo is None:
        raise ValueError("deadline must include a timezone")
    return instant <= datetime.now(timezone.utc)


class AgentCommAdapter(BasePlatformAdapter):
    def __init__(self, config, **kwargs):
        super().__init__(config=config, platform=Platform("agent_comm"))
        self.running = False
        self._session = None
        self._response = None
        self._reader = None
        self._consumer = None
        self._reconciler = None
        self._receipts = None
        self._queue = asyncio.Queue()
        self._pending_ids = set()
        self._retry_after = {}
        self._completion = {}
        self._active_event = None
        self._remote_bridge = None
        self._remote_store = None
        self._remote_worker = None
        self._remote_events = {}
        self._lifecycle_lock = asyncio.Lock()

    @property
    def _extra(self):
        return getattr(self.config, "extra", {}) or {}

    def _get_api_url(self, endpoint):
        base = self._extra.get("platform_url", "http://127.0.0.1:45042").strip().rstrip("/")
        parsed = urlsplit(base)
        # This API carries plaintext and local ACK authority, not cloud credentials.
        if parsed.scheme != "http" or parsed.hostname not in {"127.0.0.1", "localhost", "::1"}:
            raise ValueError("platform_url must address the local helper over loopback HTTP")
        if parsed.username or parsed.password or parsed.query or parsed.fragment:
            raise ValueError("platform_url must not contain credentials, query or fragment")
        if parsed.path not in {"", "/api/v1/mq"}:
            raise ValueError("platform_url path must be empty or /api/v1/mq")
        if not parsed.path:
            base += "/api/v1/mq"
        return f"{base}/{endpoint}"

    def _receipt_path(self):
        override = self._extra.get("state_path")
        if override:
            return Path(os.path.expandvars(override)).expanduser()
        # Namespace receipt IDs by helper identity/address, under the active Hermes profile.
        identity = self._extra.get("urn") or self._get_api_url("")
        suffix = hashlib.sha256(identity.encode()).hexdigest()[:16]
        return get_hermes_home() / "agent-comm" / f"receipts-{suffix}.sqlite3"

    async def connect(self, *, is_reconnect=False) -> bool:
        async with self._lifecycle_lock:
            if self.running and self._reader and not self._reader.done():
                return self.is_connected
            await self._disconnect()
            try:
                self._get_api_url("subscribe")
                self._receipts = await asyncio.to_thread(ReceiptStore, self._receipt_path())
                self._session = aiohttp.ClientSession(trust_env=False)
                self._response = await self._open_sse()
                self.running = True
                self._mark_connected()
                self._consumer = asyncio.create_task(self._consume(), name="agent-comm-consumer")
                self._reconciler = asyncio.create_task(self._reconcile(), name="agent-comm-reconcile")
                self._reader = asyncio.create_task(self._read_loop(), name="agent-comm-sse")
                return True
            except Exception:
                logger.exception("Could not connect to the local agent-comm helper")
                await self._disconnect()
                return False

    async def disconnect(self) -> None:
        async with self._lifecycle_lock:
            await self._disconnect()

    async def _disconnect(self):
        self.running = False
        self._mark_disconnected()
        if self._response is not None:
            self._response.close()
            self._response = None
        tasks = [task for task in (self._reader, self._reconciler, self._consumer, self._remote_worker) if task]
        for task in tasks:
            task.cancel()
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)
        self._reader = self._reconciler = self._consumer = None
        self._remote_worker = None
        # A processing hook must finish/cancel before closing its durable receipt store.
        await self.cancel_background_tasks()
        self._remote_events.clear()
        if self._remote_bridge is not None:
            await asyncio.to_thread(self._remote_bridge.close)
            self._remote_bridge = None
        if self._remote_store is not None:
            await asyncio.to_thread(self._remote_store.close)
            self._remote_store = None
        if self._session is not None:
            await self._session.close()
            self._session = None
        if self._receipts is not None:
            await asyncio.to_thread(self._receipts.close)
            self._receipts = None
        self._queue = asyncio.Queue()
        self._pending_ids.clear()
        self._retry_after.clear()
        self._completion.clear()
        self._active_event = None

    async def _open_sse(self):
        connect_timeout = float(self._extra.get("connect_timeout", 10))
        timeout = aiohttp.ClientTimeout(total=None, sock_connect=connect_timeout, sock_read=45)
        response = await asyncio.wait_for(
            self._session.get(self._get_api_url("subscribe"),
                              headers={"Accept": "text/event-stream"},
                              timeout=timeout, allow_redirects=False), connect_timeout)
        if response.status != 200 or response.content_type != "text/event-stream":
            response.close()
            raise RuntimeError(f"Helper subscribe failed: HTTP {response.status}, {response.content_type}")
        return response

    async def _read_loop(self):
        delay = 1
        while self.running:
            try:
                if self._response is None:
                    self._response = await self._open_sse()
                    self._mark_connected()
                delay = 1
                data, event_id = [], None
                async for raw in self._response.content:
                    line = raw.decode("utf-8").rstrip("\r\n")
                    if not line:
                        if data:
                            try:
                                await self._incoming(json.loads("\n".join(data)), event_id)
                            except (ValueError, TypeError, AttributeError) as exc:
                                logger.warning("Invalid agent-comm SSE message left unacknowledged: %s", exc)
                        data, event_id = [], None
                    elif line.startswith("data:"):
                        data.append(line[5:].removeprefix(" "))
                    elif line.startswith("id:"):
                        event_id = line[3:].removeprefix(" ")
                raise ConnectionError("Helper SSE stream closed")
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                logger.warning("agent-comm SSE disconnected: %s", exc)
                self._mark_disconnected()
            finally:
                if self._response is not None:
                    self._response.close()
                    self._response = None
            if self.running:
                await asyncio.sleep(delay)
                delay = min(delay * 2, 30)

    async def _request_json(self, method, endpoint, body=None):
        timeout = aiohttp.ClientTimeout(total=float(self._extra.get("request_timeout", 15)))
        async with self._session.request(method, self._get_api_url(endpoint), json=body,
                                         timeout=timeout, allow_redirects=False) as response:
            result = await response.json(content_type=None)
            if not 200 <= response.status < 300:
                raise RuntimeError(f"Helper {endpoint}: HTTP {response.status}: {result}")
            if not isinstance(result, dict):
                raise ValueError(f"Helper {endpoint} did not return a JSON object")
            return result

    async def _reconcile(self):
        while self.running:
            try:
                result = await self._request_json("GET", "retrieve")
                for message in result.get("messages") or []:
                    try:
                        await self._incoming(message)
                    except (ValueError, TypeError, AttributeError) as exc:
                        logger.warning("Invalid agent-comm inbox message left unacknowledged: %s", exc)
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                logger.warning("Could not reconcile agent-comm inbox: %s", exc)
            await asyncio.sleep(float(self._extra.get("reconcile_interval", 5)))

    async def _incoming(self, message, event_id=None):
        if message.get("event") == "connected":
            return
        for key in ("message_id", "sender_urn", "text"):
            if not isinstance(message.get(key), str) or not message[key]:
                raise ValueError(f"Incoming message is missing {key}; left unacknowledged")
        message_id = message["message_id"]
        if not re.fullmatch(r"[A-Za-z0-9._:-]{1,128}", message_id):
            raise ValueError("Invalid wire message_id")
        if event_id and event_id != message_id:
            raise ValueError("SSE id differs from wire message_id")
        for key in ("conversation_id", "in_reply_to", "task_id", "kind"):
            if key in message and (not isinstance(message[key], str) or not message[key]
                                   or len(message[key].encode()) > 256):
                raise ValueError(f"Invalid {key}")
        hop = message.get("hop_limit", DEFAULT_HOP_LIMIT)
        if type(hop) is not int or not 0 <= hop <= 64:
            raise ValueError("hop_limit must be an integer from 0 to 64")
        _expired(message.get("deadline"))
        if (message_id not in self._pending_ids
                and time.monotonic() >= self._retry_after.get(message_id, 0)):
            self._pending_ids.add(message_id)
            self._queue.put_nowait(message)

    async def _ack(self, message_id):
        result = await self._request_json("POST", "ack", {"message_ids": [message_id]})
        if result.get("success") is not True:
            raise RuntimeError(f"Helper rejected ACK for {message_id}: {result}")

    async def _consume(self):
        while self.running:
            message = await self._queue.get()
            message_id = message["message_id"]
            consumed = False
            try:
                outcome = await asyncio.to_thread(self._receipts.remember, message)
                if outcome is not None:
                    await self._ack(message_id)
                    consumed = True
                    continue
                if _expired(message.get("deadline")):
                    await asyncio.to_thread(self._receipts.complete, message_id, "expired")
                    logger.info("Expired agent-comm message %s was consumed without execution", message_id)
                    await self._ack(message_id)
                    consumed = True
                    continue
                if message.get("kind") in {"control.request", "control.response"}:
                    if message.get("kind") != "control.request" or self._extra.get("remote_enabled") is not True:
                        raise RuntimeError("Control messages require their paired remote processor; never dispatching to a peer agent")
                    await self._ensure_remote_bridge()
                    response = await asyncio.to_thread(self._remote_bridge.handle, message)
                    with self._remote_bridge.delivery(message, response) as response:
                        if response is not None:
                            accepted = await self._request_json("POST", "store", response)
                            if accepted.get("success") is not True or accepted.get("message_id") != response["message_id"]:
                                raise RuntimeError("Helper did not accept the durable control response")
                    await asyncio.to_thread(self._receipts.complete, message_id, "processed")
                    await self._ack(message_id)
                    consumed = True
                    continue
                from .collaboration.hermes import collaboration_enabled, read_settings, state_path
                if collaboration_enabled(self._extra) or self._extra.get("remote_enabled") is True:
                    # Peers are durable input, not private-owner LLM turns. Only
                    # the native owner can inspect these records and act on them.
                    def persist_collaboration_input():
                        from .collaboration.store import Store
                        settings = {**self._extra, **read_settings()}
                        store = Store(state_path(settings), local_urn=settings.get("urn"))
                        try:
                            store.ingest_message(message)
                        finally:
                            store.close()
                    await asyncio.to_thread(persist_collaboration_input)
                    await asyncio.to_thread(self._receipts.complete, message_id, "processed")
                    await self._ack(message_id)
                    consumed = True
                    continue
                future = asyncio.get_running_loop().create_future()
                self._completion[message_id] = future
                event = self._build_event(message)
                self._active_event = event
                await self.handle_message(event)
                if not getattr(event, "_gateway_accepted", False):
                    raise RuntimeError("Hermes did not accept the message; leaving it in the helper inbox")
                # handle_message only starts a task; ONLY the completion hook signals this receipt.
                if await future:
                    await self._ack(message_id)
                    consumed = True
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                logger.warning("agent-comm message %s remains pending: %s", message_id, exc)
            finally:
                if consumed:
                    self._retry_after.pop(message_id, None)
                else:
                    self._retry_after[message_id] = time.monotonic() + float(self._extra.get("retry_delay", 30))
                self._completion.pop(message_id, None)
                self._pending_ids.discard(message_id)
                self._active_event = None
                self._queue.task_done()

    def _build_event(self, message):
        sender = message["sender_urn"]
        # Prefix plus hash is unambiguous even when peer-chosen IDs contain colons/slashes.
        route = ("conversation:" + message["conversation_id"] if message.get("conversation_id")
                 else "task:" + message["task_id"] if message.get("task_id") else None)
        thread_id = "comm-" + hashlib.sha256(route.encode()).hexdigest() if route else None
        source = self.build_source(chat_id=sender, chat_name=sender, chat_type="dm",
                                   user_id=sender, user_name=sender, thread_id=thread_id,
                                   is_bot=True, message_id=message["message_id"])
        return MessageEvent(text=message["text"], message_type=MessageType.TEXT, source=source,
                            message_id=message["message_id"], raw_message=dict(message),
                            reply_to_message_id=message.get("in_reply_to"),
                            metadata={"agent_comm": {k: message[k] for k in WIRE_FIELDS if k in message}},
                            allow_gateway_control=False, internal=False)

    async def on_processing_complete(self, event, outcome):
        remote = self._remote_events.get(event.message_id)
        if remote is not None and remote["event"] is event:
            error = None if outcome == ProcessingOutcome.SUCCESS else "Hermes did not complete this turn successfully"
            await asyncio.to_thread(self._remote_bridge.finish_turn, event.message_id,
                response=remote.get("response"), error=error, interrupted=outcome == ProcessingOutcome.CANCELLED)
            def remote_finished(_task):
                if not remote["future"].done():
                    remote["future"].set_result(True)
            # The host releases its active-session guard in finally, after this
            # hook; do not merge a next queued RPC turn into this completed one.
            asyncio.current_task().add_done_callback(remote_finished)
            return
        future = self._completion.get(event.message_id)
        if future is None or future.done():
            return
        success = outcome == ProcessingOutcome.SUCCESS
        try:
            if success:
                await asyncio.to_thread(self._receipts.complete, event.message_id, "processed")
        except Exception:
            # The base suppresses hook exceptions; always release our worker for safe replay.
            future.set_result(False)
            raise
        # Hermes calls this hook BEFORE releasing the active-session guard. Dispatch the
        # next wire message only after that task's finally block, or Hermes may merge it.
        def finished(_task):
            if not future.done():
                future.set_result(success)
        asyncio.current_task().add_done_callback(finished)

    async def _ensure_remote_bridge(self):
        if self._remote_bridge is not None:
            return
        from agent_comm_runtime.remote import RemoteBridge
        from agent_comm_runtime.store import Store
        from .collaboration.hermes import profile_principal, state_path
        settings = self._extra
        path = settings.get("remote_state_path") or get_hermes_home() / "agent-comm" / "remote.sqlite3"
        self._remote_store = Store(state_path(settings), local_urn=settings.get("urn"))
        self._remote_bridge = RemoteBridge(path, self._remote_store, settings.get("urn"), conversations=True,
            bound_principal=profile_principal())
        await asyncio.to_thread(self._remote_bridge.recover_interrupted_turns)
        self._remote_worker = asyncio.create_task(self._run_remote_turns())

    async def _run_remote_turns(self):
        # Separate from the mailbox consumer so polling/status RPCs remain responsive.
        while self.running:
            job = await asyncio.to_thread(self._remote_bridge.claim_turn)
            if job is None:
                await asyncio.sleep(.2)
                continue
            turn_id = job["turn_id"]
            try:
                route = canonical_remote_route(job)
                source = self.build_source(chat_id=job["console_urn"], chat_name="Paired remote console", chat_type="dm",
                    user_id=job["console_urn"], user_name="Paired remote owner", thread_id=route,
                    is_bot=False, message_id=turn_id)
                event = MessageEvent(text=job["text"], message_type=MessageType.TEXT, source=source,
                    message_id=turn_id, raw_message={"origin": "locally_paired_control_rpc"},
                    metadata={}, allow_gateway_control=False, internal=False)
                future = asyncio.get_running_loop().create_future()
                self._remote_events[turn_id] = {"event": event, "future": future, "response": None}
                await self.handle_message(event)
                if not getattr(event, "_gateway_accepted", False):
                    raise RuntimeError("Hermes did not accept this paired conversation; check Gateway allow_from")
                await future
            except asyncio.CancelledError:
                await asyncio.to_thread(self._remote_bridge.finish_turn, turn_id,
                    error="Hermes adapter stopped during this turn", interrupted=True)
                raise
            except Exception as exc:
                await asyncio.to_thread(self._remote_bridge.finish_turn, turn_id, error=str(exc)[:500])
            finally:
                self._remote_events.pop(turn_id, None)

    async def _send_final_text(self, event, session_key, text_content, metadata,
                               is_ephemeral_response, ephemeral_ttl, record_delivery):
        remote = self._remote_events.get(event.message_id)
        if remote is not None and remote["event"] is event:
            # Host-owned final lifecycle event, never a model-supplied reply anchor.
            # Persist at processing-complete; send no unsolicited plaintext message.
            remote["response"] = text_content
            record_delivery(SendResult(success=True, message_id=event.message_id,
                raw_response={"status": "captured_for_remote_poll"}))
            return
        return await super()._send_final_text(event, session_key, text_content, metadata,
            is_ephemeral_response, ephemeral_ttl, record_delivery)

    async def send(self, chat_id: str, content: str, reply_to: Optional[str] = None,
                   metadata: Optional[Dict[str, Any]] = None):
        try:
            from .collaboration.hermes import collaboration_enabled
            if collaboration_enabled(self._extra) or self._extra.get("remote_enabled") is True:
                return SendResult(success=False, error="个人协作模式禁止直接发送；请在主人原生对话中通过 agent_comm_collaboration prepare_action / dispatch 使用明确委托。")
            if self._session is None or self._session.closed:
                raise RuntimeError("agent-comm adapter is disconnected")
            metadata = metadata or {}
            explicit = dict(metadata.get("agent_comm") or {})
            explicit.update({k: metadata[k] for k in (*WIRE_FIELDS, "message_id") if k in metadata})
            anchor = reply_to or explicit.get("in_reply_to")
            active = self._active_event
            if not anchor and active and active.source.chat_id == chat_id:
                if metadata.get("thread_id") == active.source.thread_id:
                    anchor = active.message_id
            route = await asyncio.to_thread(self._receipts.route, anchor) if anchor else None
            if route and route.get("sender_urn") != chat_id:
                raise ValueError("Reply recipient differs from the incoming message sender")
            body = {"recipient_urn": chat_id, "text": content}
            if route:
                body.update({k: route[k] for k in ("conversation_id", "task_id", "deadline") if k in route})
                remaining = route.get("hop_limit", DEFAULT_HOP_LIMIT)
                if remaining <= 0:
                    return SendResult(success=True, raw_response={"status": "suppressed", "reason": "hop_limit"})
                if _expired(route.get("deadline")):
                    return SendResult(success=True, raw_response={"status": "suppressed", "reason": "deadline"})
                body["hop_limit"] = remaining - 1
                body["kind"] = "result" if route.get("task_id") else "message"
            else:
                body["hop_limit"] = DEFAULT_HOP_LIMIT
            body.update({k: explicit[k] for k in WIRE_FIELDS if k in explicit})
            if route and body["hop_limit"] > remaining - 1:
                raise ValueError("A reply cannot increase the inherited hop limit")
            if anchor:
                body["in_reply_to"] = anchor
            if _expired(body.get("deadline")):
                raise ValueError("Message deadline expired")
            message_id = explicit.get("message_id")
            if not message_id:
                # Repeated sends for the same trigger/content retain a helper idempotency key.
                stable = json.dumps(body, sort_keys=True, ensure_ascii=False)
                message_id = str(uuid.uuid5(uuid.NAMESPACE_URL, stable)) if anchor else str(uuid.uuid4())
            body["message_id"] = message_id
            result = None
            for attempt in range(3):
                try:
                    result = await self._request_json("POST", "store", body)
                    break
                except (aiohttp.ClientError, asyncio.TimeoutError):
                    if attempt == 2:
                        raise
                    await asyncio.sleep(0.25 * 2 ** attempt)
            if result.get("success") is not True or result.get("message_id") != message_id:
                raise RuntimeError(f"Helper did not accept message {message_id}: {result}")
            return SendResult(success=True, message_id=result["message_id"], raw_response=result)
        except Exception as exc:
            logger.warning("agent-comm send failed: %s", exc)
            return SendResult(success=False, error=str(exc))

    async def get_chat_info(self, chat_id: str) -> Dict[str, Any]:
        return {"name": chat_id, "type": "dm"}
