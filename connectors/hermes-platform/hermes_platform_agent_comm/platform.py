import os
import sys
import json
import time
import threading
import asyncio
import urllib.request
import urllib.parse
from typing import Any, Dict, List, Optional

from gateway.platforms.base import (
    BasePlatformAdapter,
    SendResult,
    MessageEvent,
    MessageType,
)
from gateway.config import Platform

class AgentCommAdapter(BasePlatformAdapter):
    def __init__(self, config, **kwargs):
        platform = Platform("agent_comm")
        super().__init__(config=config, platform=platform)
        self.running = False
        self.thread = None
        self.loop = None

    def _log_debug(self, event_type: str, data: Dict[str, Any]):
        log_file = "/Users/shiyaozhang/Developer/agent-collaboration-deploy/agent-comm-debug.log"
        log_entry = {
            "timestamp": time.strftime("%Y-%m-%d %H:%M:%S"),
            "event_type": event_type,
            **data
        }
        try:
            with open(log_file, "a") as f:
                f.write(json.dumps(log_entry, ensure_ascii=False) + "\n")
        except Exception as e:
            print(f"[agent-comm-platform] Failed to write debug log: {e}", file=sys.stderr)

    def _get_api_url(self, endpoint):
        # Configuration keys are retrieved from self.config.extra
        base_url = self.config.extra.get("platform_url", "http://localhost:45042").strip()
        if base_url.endswith("/"):
            base_url = base_url[:-1]
        if "/api/v1/mq" not in base_url:
            base_url += "/api/v1/mq"
        return f"{base_url}/{endpoint}"

    async def connect(self) -> bool:
        if self.running:
            return True
        self.running = True
        self.loop = asyncio.get_running_loop()
        print(f"[agent-comm-platform] Starting connection to local daemon: {self.config.extra.get('platform_url')}")
        self._log_debug("connect_start", {"platform_url": self.config.extra.get('platform_url')})
        self.thread = threading.Thread(target=self._run_loop, daemon=True)
        self.thread.start()
        return True

    async def disconnect(self) -> None:
        self.running = False
        self._log_debug("disconnect", {})

    def _run_loop(self):
        while self.running:
            try:
                self._connect_sse()
            except Exception as e:
                print(f"[agent-comm-platform] SSE connection error: {e}", file=sys.stderr)
                self._log_debug("sse_connection_error", {"error": str(e)})
            if self.running:
                time.sleep(5)

    def _connect_sse(self):
        url = self._get_api_url("subscribe")
        req = urllib.request.Request(url, headers={"Accept": "text/event-stream"})

        with urllib.request.urlopen(req) as response:
            if response.status != 200:
                raise Exception(f"Subscribe returned status code {response.status}")

            print(f"[agent-comm-platform] SSE connection successfully established to daemon: {url}")
            self._log_debug("sse_connected", {"url": url})
            while self.running:
                line = response.readline()
                if not line:
                    break
                trimmed = line.decode("utf-8").strip()
                if trimmed.startswith("data: "):
                    self._handle_incoming_event(trimmed[6:])

    def _handle_incoming_event(self, data_str):
        try:
            self._log_debug("incoming_event_raw", {"data": data_str})
            parsed = json.loads(data_str)
            if parsed.get("event") == "connected":
                return # Connection verification packet

            sender_urn = parsed.get("sender_urn")
            text = parsed.get("text")

            if not sender_urn or not text:
                return

            print(f"[agent-comm-platform] Received incoming plaintext message from {sender_urn}")
            self._log_debug("incoming_event_parsed", {"sender_urn": sender_urn, "text": text})

            # Build a MessageEvent and hand it to the base class handler on the main loop
            source = self.build_source(
                chat_id=sender_urn,
                chat_name=sender_urn,
                chat_type="dm",
                user_id=sender_urn,
                user_name=sender_urn,
            )

            event = MessageEvent(
                text=text,
                message_type=MessageType.TEXT,
                source=source,
                message_id=str(int(time.time() * 1000)),
                timestamp=__import__("datetime").datetime.now(),
            )

            if self.loop and self.loop.is_running():
                asyncio.run_coroutine_threadsafe(self.handle_message(event), self.loop)

        except Exception as e:
            print(f"[agent-comm-platform] Error handling incoming event: {e}", file=sys.stderr)
            self._log_debug("incoming_event_error", {"error": str(e)})

    async def send(
        self,
        chat_id: str,
        content: str,
        reply_to: Optional[str] = None,
        metadata: Optional[Dict[str, Any]] = None,
    ):
        try:
            self._log_debug("send_message_start", {"recipient_urn": chat_id, "text": content})
            url = self._get_api_url("store")
            body_obj = {
                "recipient_urn": chat_id,
                "text": content
            }
            data = json.dumps(body_obj).encode("utf-8")
            
            req = urllib.request.Request(
                url,
                data=data,
                headers={"Content-Type": "application/json"}
            )
            
            # Perform blocking request in the default loop executor
            loop = asyncio.get_running_loop()
            await loop.run_in_executor(None, self._send_request, req)

            print(f"[agent-comm-platform] Message successfully sent to local daemon for delivery to {chat_id}")
            self._log_debug("send_message_success", {"recipient_urn": chat_id})
            return SendResult(success=True)

        except Exception as e:
            print(f"[agent-comm-platform] Failed to send message: {e}", file=sys.stderr)
            self._log_debug("send_message_error", {"recipient_urn": chat_id, "error": str(e)})
            return SendResult(success=False, error=str(e))

    def _send_request(self, req):
        with urllib.request.urlopen(req) as resp:
            resp.read()

    async def get_chat_info(self, chat_id: str) -> Dict[str, Any]:
        return {
            "name": chat_id,
            "type": "dm",
        }
