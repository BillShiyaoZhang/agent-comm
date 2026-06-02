import os
import sys
import json
import time
import threading
import urllib.request
import urllib.parse
from urllib.error import URLError

class PlatformMessage:
    def __init__(self, platform, sender_id, text):
        self.platform = platform
        self.sender_id = sender_id
        self.text = text

class AgentCommPlatform:
    def __init__(self, gateway, config):
        self.gateway = gateway
        self.config = config
        self.running = False
        self.thread = None

    def _get_api_url(self, endpoint):
        base_url = self.config.get("platform_url", "http://localhost:45042").strip()
        if base_url.endswith("/"):
            base_url = base_url[:-1]
        if "/api/v1/mq" not in base_url:
            base_url += "/api/v1/mq"
        return f"{base_url}/{endpoint}"

    def start(self):
        if self.running:
            return
        self.running = True
        print(f"[agent-comm-platform] Starting connection to local daemon: {self.config.get('platform_url')}")
        self.thread = threading.Thread(target=self._run_loop, daemon=True)
        self.thread.start()

    def stop(self):
        self.running = False

    def _run_loop(self):
        while self.running:
            try:
                self._connect_sse()
            except Exception as e:
                print(f"[agent-comm-platform] SSE connection error: {e}", file=sys.stderr)
            if self.running:
                time.sleep(5)

    def _connect_sse(self):
        url = self._get_api_url("subscribe")
        req = urllib.request.Request(url, headers={"Accept": "text/event-stream"})

        with urllib.request.urlopen(req) as response:
            if response.status != 200:
                raise Exception(f"Subscribe returned status code {response.status}")

            buffer = ""
            while self.running:
                chunk = response.read(1024)
                if not chunk:
                    break
                buffer += chunk.decode("utf-8")
                lines = buffer.split("\n")
                buffer = lines.pop()

                for line in lines:
                    trimmed = line.strip()
                    if trimmed.startswith("data: "):
                        self._handle_incoming_event(trimmed[6:])

    def _handle_incoming_event(self, data_str):
        try:
            parsed = json.loads(data_str)
            if parsed.get("event") == "connected":
                return # Connection verification packet

            sender_urn = parsed.get("sender_urn")
            text = parsed.get("text")

            if not sender_urn or not text:
                return

            print(f"[agent-comm-platform] Received incoming plaintext message from {sender_urn}")

            # Inject the message into Hermes platform
            msg = PlatformMessage(
                platform="agent_comm",
                sender_id=sender_urn,
                text=text
            )
            self.gateway.on_message(msg)

        except Exception as e:
            print(f"[agent-comm-platform] Error handling incoming event: {e}", file=sys.stderr)

    def send_message(self, recipient_id, text):
        try:
            url = self._get_api_url("store")
            body_obj = {
                "recipient_urn": recipient_id,
                "text": text
            }
            data = json.dumps(body_obj).encode("utf-8")
            
            req = urllib.request.Request(
                url,
                data=data,
                headers={"Content-Type": "application/json"}
            )
            with urllib.request.urlopen(req) as resp:
                resp.read()

            print(f"[agent-comm-platform] Message successfully sent to local daemon for delivery to {recipient_id}")

        except Exception as e:
            print(f"[agent-comm-platform] Failed to send message: {e}", file=sys.stderr)
            raise e
