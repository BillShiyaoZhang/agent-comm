"""Behavior tests using the real Hermes base class and an isolated loopback helper.

Run with Hermes' Python and HERMES_SOURCE on PYTHONPATH; never starts a gateway/model.
"""
import asyncio
import atexit
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone

_TEST_HOME = tempfile.TemporaryDirectory(prefix="agent-comm-hermes-tests-")
atexit.register(_TEST_HOME.cleanup)
os.environ["HERMES_HOME"] = _TEST_HOME.name
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from aiohttp import web
from gateway.config import PlatformConfig
from gateway.platform_registry import PlatformEntry, platform_registry
from gateway.run_adapters import GatewayAdapterLifecycleMixin

platform_registry.register(PlatformEntry(name="agent_comm", label="Agent Comm",
                                         adapter_factory=lambda cfg: None, check_fn=lambda: True))
from hermes_platform_agent_comm.platform import AgentCommAdapter
from hermes_platform_agent_comm.plugin import _apply_yaml_config


class Helper:
    def __init__(self):
        self.inbox = {}
        self.subscribers = set()
        self.stored = []
        self.acked = []
        self.store_response = None
        self.reject_ack = False
        self.stop = False

    async def start(self):
        app = web.Application()
        app.router.add_get("/api/v1/mq/subscribe", self.subscribe)
        app.router.add_get("/api/v1/mq/retrieve", self.retrieve)
        app.router.add_post("/api/v1/mq/store", self.store)
        app.router.add_post("/api/v1/mq/ack", self.ack)
        self.runner = web.AppRunner(app, shutdown_timeout=1)
        await self.runner.setup()
        site = web.TCPSite(self.runner, "127.0.0.1", 0)
        await site.start()
        self.url = f"http://127.0.0.1:{site._server.sockets[0].getsockname()[1]}"

    async def close(self):
        self.stop = True
        for queue in self.subscribers:
            queue.put_nowait(None)
        await self.runner.cleanup()

    async def subscribe(self, request):
        response = web.StreamResponse(headers={"Content-Type": "text/event-stream"})
        await response.prepare(request)
        await response.write(b'data: {"event":"connected"}\n\n')
        queue = asyncio.Queue()
        self.subscribers.add(queue)
        for message in self.inbox.values():
            queue.put_nowait(message)
        try:
            while not self.stop:
                try:
                    message = await asyncio.wait_for(queue.get(), .1)
                except asyncio.TimeoutError:
                    await response.write(b": heartbeat\n\n")
                    continue
                if message is None:
                    break
                await response.write(f'id: {message["message_id"]}\ndata: {json.dumps(message)}\n\n'.encode())
        except (ConnectionError, asyncio.CancelledError):
            pass
        finally:
            self.subscribers.discard(queue)
        return response

    async def retrieve(self, request):
        return web.json_response({"messages": list(self.inbox.values())})

    async def store(self, request):
        body = await request.json()
        self.stored.append(body)
        return web.json_response(self.store_response or {"success": True, "message_id": body["message_id"],
                                                         "status": "accepted"}, status=202)

    async def ack(self, request):
        body = await request.json()
        if self.reject_ack:
            return web.json_response({"success": False})
        self.acked.extend(body["message_ids"])
        for message_id in body["message_ids"]:
            self.inbox.pop(message_id, None)
        return web.json_response({"success": True})

    def publish(self, message):
        self.inbox[message["message_id"]] = message
        for queue in self.subscribers:
            queue.put_nowait(message)


def envelope(message_id="wire-1", **kwargs):
    return {"message_id": message_id, "sender_urn": "urn:agent-comm:agent:peer",
            "text": "hello", **kwargs}


class TestAdapter(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="receipts-", dir=_TEST_HOME.name)
        self.helper = Helper()
        await self.helper.start()
        self.adapters = []
        self.received = []

    def adapter(self):
        adapter = AgentCommAdapter(PlatformConfig(enabled=True, extra={
            "platform_url": self.helper.url, "state_path": str(Path(self.temp.name) / "receipts.sqlite3"),
            "reconcile_interval": .05, "retry_delay": .1, "request_timeout": 1,
        }))
        adapter.set_message_handler(self.handler)
        self.adapters.append(adapter)
        return adapter

    async def handler(self, event):
        self.received.append(event)
        return None

    async def until(self, predicate, timeout=3):
        async with asyncio.timeout(timeout):
            while not predicate():
                await asyncio.sleep(.01)

    async def asyncTearDown(self):
        for adapter in self.adapters:
            await adapter.disconnect()
        await self.helper.close()
        self.temp.cleanup()

    async def test_real_lifecycle_connection_and_reader_shutdown(self):
        adapter = self.adapter()
        class Lifecycle(GatewayAdapterLifecycleMixin):
            def _platform_connect_timeout_secs(self, platform=None, *, initial=False):
                return 3
        lifecycle = Lifecycle()
        self.assertTrue(await lifecycle._connect_adapter_with_timeout(adapter, adapter.platform, initial=True))
        self.assertTrue(adapter.is_connected)
        reader = adapter._reader
        session = adapter._session
        await adapter.disconnect()
        self.assertFalse(adapter.is_connected)
        self.assertTrue(reader.done())
        self.assertTrue(session.closed)
        self.assertTrue(await lifecycle._connect_adapter_with_timeout(adapter, adapter.platform, is_reconnect=True))
        self.assertTrue(adapter.is_connected)

    async def test_failed_connection_never_reports_connected(self):
        adapter = self.adapter()
        adapter.config.extra["platform_url"] = "http://127.0.0.1:1"
        self.assertFalse(await adapter.connect(is_reconnect=True))
        self.assertFalse(adapter.is_connected)
        self.assertIsNone(adapter._reader)
        self.assertIsNone(adapter._session)

    async def test_dropped_sse_reconnects_and_pending_messages_are_consumed(self):
        adapter = self.adapter()
        await adapter.connect()
        for queue in self.helper.subscribers:
            queue.put_nowait(None)
        await self.until(lambda: not adapter.is_connected)
        self.helper.publish(envelope())
        await self.until(lambda: adapter.is_connected)
        await self.until(lambda: "wire-1" in self.helper.acked)
        self.assertEqual(len(self.received), 1)

    async def test_malformed_pending_message_does_not_block_valid_message(self):
        adapter = self.adapter()
        await adapter.connect()
        self.helper.publish(envelope("bad", hop_limit=100))
        self.helper.publish(envelope("good"))
        await self.until(lambda: "good" in self.helper.acked)
        self.assertIn("bad", self.helper.inbox)
        self.assertEqual([event.message_id for event in self.received], ["good"])

    async def test_wire_id_dedup_and_completion_before_ack(self):
        adapter = self.adapter()
        started, finish = asyncio.Event(), asyncio.Event()
        async def slow_handler(event):
            self.received.append(event)
            started.set()
            await finish.wait()
        adapter.set_message_handler(slow_handler)
        await adapter.connect()
        message = envelope(conversation_id="room", task_id="task", hop_limit=3)
        self.helper.publish(message)
        await asyncio.wait_for(started.wait(), 2)
        self.helper.publish(message)
        await asyncio.sleep(.15)
        self.assertEqual(len(self.received), 1)
        self.assertEqual(self.helper.acked, [])
        self.assertEqual(self.received[0].message_id, "wire-1")
        finish.set()
        await self.until(lambda: "wire-1" in self.helper.acked)
        await adapter.disconnect()
        # Lost/replayed local ACK after restart must not run Hermes again.
        self.helper.publish(message)
        replacement = self.adapter()
        await replacement.connect()
        await self.until(lambda: self.helper.acked.count("wire-1") == 2)
        self.assertEqual(len(self.received), 1)

    async def test_cancelled_processing_survives_restart(self):
        first = self.adapter()
        started = asyncio.Event()
        async def interrupted_handler(event):
            self.received.append(event)
            started.set()
            await asyncio.Event().wait()
        first.set_message_handler(interrupted_handler)
        await first.connect()
        self.helper.publish(envelope())
        await asyncio.wait_for(started.wait(), 2)
        await first.disconnect()
        self.assertIn("wire-1", self.helper.inbox)
        self.assertEqual(self.helper.acked, [])
        replacement = self.adapter()
        await replacement.connect()
        await self.until(lambda: "wire-1" in self.helper.acked)
        self.assertEqual(len(self.received), 2)

    async def test_completed_receipt_survives_failed_ack(self):
        first = self.adapter()
        self.helper.reject_ack = True
        await first.connect()
        self.helper.publish(envelope())
        await self.until(lambda: len(self.received) == 1)
        await asyncio.sleep(.15)
        self.assertEqual(len(self.received), 1)
        await first.disconnect()
        self.helper.reject_ack = False
        await self.adapter().connect()
        await self.until(lambda: "wire-1" in self.helper.acked)
        self.assertEqual(len(self.received), 1)

    async def test_three_messages_are_not_merged_by_busy_hermes_session(self):
        adapter = self.adapter()
        async def handler(event):
            self.received.append(event)
            await asyncio.sleep(.08)
        adapter.set_message_handler(handler)
        await adapter.connect()
        for index in range(3):
            self.helper.publish(envelope(f"wire-{index}"))
        await self.until(lambda: len(self.helper.acked) == 3)
        self.assertEqual([e.message_id for e in self.received], ["wire-0", "wire-1", "wire-2"])

    async def test_no_handler_means_no_ack(self):
        adapter = self.adapter()
        adapter.set_message_handler(None)
        await adapter.connect()
        self.helper.publish(envelope())
        await asyncio.sleep(.15)
        self.assertEqual(self.helper.acked, [])
        adapter.set_message_handler(self.handler)
        await self.until(lambda: "wire-1" in self.helper.acked)

    async def test_permissions_and_conversation_isolation(self):
        adapter = self.adapter()
        event = adapter._build_event(envelope(text="/approve", conversation_id="a:b", task_id="task"))
        other = adapter._build_event(envelope("wire-2", conversation_id="a", task_id="task"))
        self.assertTrue(event.source.is_bot)
        self.assertFalse(event.allow_gateway_control)
        self.assertFalse(event.internal)
        self.assertFalse(event.source.role_authorized)
        self.assertIsNone(event.get_command())
        self.assertNotEqual(adapter._event_session_key(event), adapter._event_session_key(other))
        same = adapter._build_event(envelope("wire-3", conversation_id="a:b", task_id="different"))
        self.assertEqual(adapter._event_session_key(event), adapter._event_session_key(same))

    async def test_send_checks_json_and_preserves_reply_metadata(self):
        adapter = self.adapter()
        await adapter.connect()
        incoming = envelope(conversation_id="conv", task_id="task", kind="task", hop_limit=3)
        await asyncio.to_thread(adapter._receipts.remember, incoming)
        result = await adapter.send(incoming["sender_urn"], "reply", reply_to="wire-1")
        self.assertTrue(result.success)
        self.assertTrue(result.message_id)
        stored = self.helper.stored[-1]
        self.assertEqual(stored["conversation_id"], "conv")
        self.assertEqual(stored["task_id"], "task")
        self.assertEqual(stored["in_reply_to"], "wire-1")
        self.assertEqual(stored["hop_limit"], 2)
        self.assertEqual(stored["kind"], "result")
        again = await adapter.send(incoming["sender_urn"], "reply", reply_to="wire-1")
        self.assertEqual(result.message_id, again.message_id)
        self.helper.store_response = {"success": False, "error": "rejected"}
        self.assertFalse((await adapter.send(incoming["sender_urn"], "no")).success)
        self.helper.store_response = {"success": True}
        self.assertFalse((await adapter.send(incoming["sender_urn"], "missing-id")).success)

    async def test_expired_message_consumed_without_model_and_zero_hop_no_reply(self):
        adapter = self.adapter()
        async def reply(event):
            self.received.append(event)
            return "automatic reply"
        adapter.set_message_handler(reply)
        await adapter.connect()
        self.helper.publish(envelope("expired", deadline=(datetime.now(timezone.utc) - timedelta(seconds=1)).isoformat()))
        self.helper.publish(envelope("last-hop", hop_limit=0))
        await self.until(lambda: len(self.helper.acked) == 2)
        self.assertEqual([event.message_id for event in self.received], ["last-hop"])
        self.assertEqual(self.helper.stored, [])

    async def test_failed_completion_retries_without_ack(self):
        adapter = self.adapter()
        async def failing(event):
            self.received.append(event)
            raise RuntimeError("temporary handler failure")
        adapter.set_message_handler(failing)
        await adapter.connect()
        self.helper.publish(envelope())
        await self.until(lambda: bool(self.received))
        await asyncio.sleep(.03)
        self.assertEqual(self.helper.acked, [])
        adapter.set_message_handler(self.handler)
        await self.until(lambda: "wire-1" in self.helper.acked)
        self.assertEqual(len(self.received), 2)

    async def test_config_nested_extra_and_local_endpoint_boundary(self):
        self.assertEqual(_apply_yaml_config({}, {"extra": {"state_path": "test"}, "platform_url": self.helper.url}),
                         {"state_path": "test", "platform_url": self.helper.url})
        adapter = self.adapter()
        adapter.config.extra["platform_url"] = "https://agent-communication.online"
        with self.assertRaises(ValueError):
            adapter._get_api_url("subscribe")


if __name__ == "__main__":
    unittest.main()
