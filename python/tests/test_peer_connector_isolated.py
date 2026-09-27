"""Import the real connector with stub host/dependency seams; no Hermes host claim.

Runs real adapter consumption, receipt persistence and Runtime ACL/review code.
The host base/aiohttp seams are stubs because Hermes is not installed here.
"""
import asyncio
from contextlib import suppress
import importlib.util
from pathlib import Path
import sys
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import AsyncMock, Mock, patch

import test_social as fixture


class PeerConnectorIsolatedTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.f = fixture.TestSocial()
        self.f.setUp()
        self.addCleanup(self.f.doCleanups)
        self.f.connect()
        root = Path(__file__).resolve().parents[2] / "connectors/hermes-platform/hermes_platform_agent_comm"
        package = ModuleType("peer_connector_isolated")
        package.__path__ = [str(root)]
        gateway = ModuleType("gateway")
        gateway.__path__ = []
        config = ModuleType("gateway.config")
        config.Platform = lambda value: value
        base = ModuleType("gateway.platforms.base")
        class BaseAdapter:
            def __init__(self, config, **_):
                self.config = config
        base.BasePlatformAdapter = BaseAdapter
        base.MessageEvent = SimpleNamespace
        base.MessageType = SimpleNamespace(TEXT="text")
        base.SendResult = SimpleNamespace
        event = ModuleType("gateway.platforms.event")
        event.ProcessingOutcome = SimpleNamespace(SUCCESS="success", CANCELLED="cancelled")
        aiohttp = ModuleType("aiohttp")
        aiohttp.ClientError = OSError
        aiohttp.ContentTypeError = ValueError
        constants = ModuleType("hermes_constants")
        constants.get_hermes_home = lambda: Path(self.f.temp.name)
        hermes = ModuleType("peer_connector_isolated.collaboration.hermes")
        hermes.profile_principal = lambda: "bob"
        hermes.state_path = lambda settings: Path(self.f.temp.name) / "b.sqlite3"
        hermes.collaboration_enabled = lambda settings: settings.get("collaboration_enabled", False)
        self.modules = patch.dict(sys.modules, {"peer_connector_isolated":package, "gateway":gateway,
            "gateway.config":config, "gateway.platforms.base":base, "gateway.platforms.event":event,
            "aiohttp":aiohttp, "hermes_constants":constants, "peer_connector_isolated.collaboration.hermes":hermes})
        self.modules.start()
        self.addCleanup(self.modules.stop)
        spec = importlib.util.spec_from_file_location("peer_connector_isolated.platform", root / "platform.py")
        self.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.module)
        self.adapter = self.module.AgentCommAdapter(SimpleNamespace(extra={"urn":fixture.B}))
        self.adapter._receipts = self.module.ReceiptStore(Path(self.f.temp.name) / "isolated-receipts.sqlite3")
        self.addCleanup(self.adapter._receipts.close)
        self.adapter._flush_social_outbox = Mock(return_value={"accepted":0})
        self.adapter._ack = AsyncMock()
        self.adapter.handle_message = AsyncMock(side_effect=AssertionError("peer content must not start a model turn"))

    async def consume(self, message):
        self.adapter.running = True
        self.adapter._pending_ids.add(message["message_id"])
        await self.adapter._queue.put(message)
        task = asyncio.create_task(self.adapter._consume())
        try:
            await asyncio.wait_for(self.adapter._queue.join(), 2)
        finally:
            self.adapter.running = False
            task.cancel()
            with suppress(asyncio.CancelledError):
                await task

    async def test_pending_content_is_acknowledged_without_model_in_both_modes(self):
        for enabled in (False, True):
            self.adapter.config.extra["collaboration_enabled"] = enabled
            message = {"message_id": "pending-" + str(enabled), "sender_urn":fixture.A, "text":"unsafe private peer content"}
            await self.consume(message)
            self.adapter._ack.assert_awaited_with(message["message_id"])
        self.adapter.handle_message.assert_not_awaited()
        self.assertEqual(len(self.f.b.inbox("bob")["pending_review"]), 2)

    async def test_blocked_and_duplicate_after_unblock_never_reach_model(self):
        self.f.b.set_peer_block(fixture.A, True, "bob")
        message = {"message_id":"blocked-1", "sender_urn":fixture.A, "text":"blocked body"}
        await self.consume(message)
        self.f.b.set_peer_block(fixture.A, False, "bob")
        await self.consume(message)
        self.adapter.handle_message.assert_not_awaited()
        self.assertEqual(self.f.b.inbox("bob")["pending_review"], [])
        self.assertEqual(self.adapter._ack.await_count, 2)

    async def test_approved_history_can_be_read_but_does_not_auto_replay_as_model_turn(self):
        message = {"message_id":"approved-1", "sender_urn":fixture.A, "text":"owner approved body"}
        self.f.b.ingest_message(message)
        self.f.b.review_preview("approved-1", "bob|review")
        self.f.b.review_peer("approved-1", "approve", "bob|review")
        await self.consume(message)
        self.adapter.handle_message.assert_not_awaited()
        self.adapter._ack.assert_awaited_once_with("approved-1")
        self.assertTrue(any(m["message_id"] == "approved-1" for m in self.f.b.inbox("bob")["messages"]))

    async def test_legacy_send_and_historical_reply_check_real_persisted_acl(self):
        self.adapter._disclosure = AsyncMock(return_value={"state":"legacy_helper"})
        self.adapter._request_json = AsyncMock(return_value={"success":True, "message_id":"out-1"})
        self.f.b.ingest_message({"message_id":"old-trigger", "sender_urn":fixture.A, "text":"old peer body"})
        self.f.b.set_peer_block(fixture.A, True, "bob")
        body = {"message_id":"out-1", "recipient_urn":fixture.A, "text":"reply"}
        with self.assertRaises(ValueError):
            await self.adapter._store_business(body)
        self.adapter._request_json.assert_not_awaited()
        self.f.b.set_peer_block(fixture.A, False, "bob")
        with self.assertRaises(ValueError):
            await self.adapter._store_business({**body,"in_reply_to":"old-trigger"})
        self.adapter._request_json.assert_not_awaited()
        self.assertTrue((await self.adapter._store_business(body))["success"])
