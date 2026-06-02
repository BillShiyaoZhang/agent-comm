import os
import sys
import json
import time
import struct
import threading
import subprocess
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

    def _get_keys_dir(self):
        keys_dir = self.config.get("keys_path", "~/.hermes/keys/agent-comm")
        if keys_dir.startswith("~"):
            keys_dir = os.path.expanduser(keys_dir)
        return os.path.abspath(keys_dir)

    def _get_helper_path(self):
        home = os.path.expanduser("~")
        binary_name = "agent-comm-helper.exe" if sys.platform == "win32" else "agent-comm-helper"
        return os.environ.get("AGENT_COMM_HELPER_PATH", os.path.join(home, ".agent-comm", "bin", binary_name))

    def _invoke_helper(self, args):
        helper_path = self._get_helper_path()
        try:
            res = subprocess.run([helper_path] + args, capture_output=True, text=True, check=True)
            parsed = json.loads(res.stdout.strip())
            if "error" in parsed:
                raise Exception(f"Helper error: {parsed['error']}")
            return parsed
        except subprocess.CalledProcessError as e:
            raise Exception(f"Helper failed: {e}. Stderr: {e.stderr}")
        except Exception as e:
            raise Exception(f"Failed to invoke helper: {e}")

    def _get_api_url(self, endpoint):
        base_url = self.config.get("platform_url", "http://localhost:8080").strip()
        if base_url.endswith("/"):
            base_url = base_url[:-1]
        if "/api/v1/mq" not in base_url:
            base_url += "/api/v1/mq"
        return f"{base_url}/{endpoint}"

    def start(self):
        if self.running:
            return
        self.running = True
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
        keys_dir = self._get_keys_dir()
        urn = self.config.get("urn")
        timestamp = int(time.time())

        # Call Go helper to sign the retrieve request
        sign_res = self._invoke_helper([
            "sign-retrieve",
            keys_dir,
            urn,
            str(timestamp)
        ])

        headers = {
            "X-URN": urn,
            "X-Timestamp": str(timestamp),
            "X-Pubkey": sign_res["pubkey"],
            "X-Signature": sign_res["signature"],
            "Accept": "text/event-stream"
        }

        url = self._get_api_url("subscribe")
        req = urllib.request.Request(url, headers=headers)

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
            payload_proto_b64 = parsed.get("payload_proto")
            message_id = parsed.get("message_id")

            if not payload_proto_b64:
                return

            import base64
            envelope_bytes = base64.b64decode(payload_proto_b64)
            envelope = self._decode_envelope_proto(envelope_bytes)

            # Decrypt using Go helper
            keys_dir = self._get_keys_dir()
            decrypt_res = self._invoke_helper([
                "decrypt-envelope",
                keys_dir,
                envelope["sender_static_pubkey"].hex(),
                envelope["ephemeral_pubkey"].hex(),
                envelope["nonce"].hex(),
                envelope["ciphertext"].hex(),
                envelope["tag"].hex()
            ])

            # Inject the message to Hermes platform
            msg = PlatformMessage(
                platform="agent_comm",
                sender_id=envelope["sender_urn"],
                text=decrypt_res["plaintext"]
            )
            self.gateway.on_message(msg)

            # Ack to remove from queue
            self._ack_message(message_id)

        except Exception as e:
            print(f"[agent-comm-platform] Error handling incoming event: {e}", file=sys.stderr)

    def _ack_message(self, message_id):
        try:
            url = self._get_api_url("ack")
            data = json.dumps({"message_ids": [message_id]}).encode("utf-8")
            req = urllib.request.Request(
                url,
                data=data,
                headers={"Content-Type": "application/json"}
            )
            with urllib.request.urlopen(req) as resp:
                resp.read()
        except Exception as e:
            print(f"[agent-comm-platform] Ack failed: {e}", file=sys.stderr)

    def send_message(self, recipient_id, text):
        try:
            keys_dir = self._get_keys_dir()

            # 1. Resolve recipient static X25519 key via platform registry
            recipient_pubkey_hex = self._resolve_registry_x25519(recipient_id)

            # 2. Encrypt message using helper
            text_hex = text.encode("utf-8").hex()
            enc_res = self._invoke_helper([
                "encrypt-envelope",
                keys_dir,
                recipient_pubkey_hex,
                text_hex
            ])

            # 3. Construct EncryptedEnvelope protobuf manually
            envelope = {
                "sender_urn": enc_res["sender_urn"],
                "sender_static_pubkey": bytes.fromhex(enc_res["sender_static_pubkey"]),
                "ephemeral_pubkey": bytes.fromhex(enc_res["ephemeral_pubkey"]),
                "nonce": bytes.fromhex(enc_res["nonce"]),
                "ciphertext": bytes.fromhex(enc_res["ciphertext"]),
                "tag": bytes.fromhex(enc_res["tag"]),
                "message_id": enc_res["message_id"]
            }

            envelope_bytes = self._encode_envelope_proto(envelope)

            # 4. Construct POST MQ store body
            import base64
            store_req = {
                "recipient_urn": recipient_id,
                "expiry_unix": int(time.time()) + 7 * 24 * 3600,
                "payload_proto": base64.b64encode(envelope_bytes).decode("utf-8")
            }

            body_str = json.dumps(store_req)
            body_bytes = body_str.encode("utf-8")

            # 5. Sign POST body using helper
            sign_res = self._invoke_helper([
                "sign-store",
                keys_dir,
                body_bytes.hex()
            ])

            # 6. Deliver to platform
            url = self._get_api_url("store")
            req = urllib.request.Request(
                url,
                data=body_bytes,
                headers={
                    "Content-Type": "application/json",
                    "Authorization": f"Ed25519 {sign_res['signature']}:{sign_res['pubkey']}"
                }
            )
            with urllib.request.urlopen(req) as resp:
                resp.read()

        except Exception as e:
            print(f"[agent-comm-platform] Failed to send message: {e}", file=sys.stderr)
            raise e

    def _resolve_registry_x25519(self, urn):
        base_url = self.config.get("platform_url", "http://localhost:8080").strip()
        if base_url.endswith("/"):
            base_url = base_url[:-1]
        
        registry_url = base_url.replace("/api/v1/mq", "/api/v1/registry/resolve") if "/api/v1/mq" in base_url else f"{base_url}/api/v1/registry/resolve"
        full_url = f"{registry_url}?urn={urllib.parse.quote(urn)}"

        req = urllib.request.Request(full_url)
        with urllib.request.urlopen(req) as resp:
            data = json.loads(resp.read().decode("utf-8"))

        if not data.get("x25519_pubkey"):
            raise Exception(f"Registry did not return x25519_pubkey for URN {urn}")

        import base64
        return base64.b64decode(data["x25519_pubkey"]).hex()

    # --- Protobuf Varint Parser ---

    def _read_varint(self, data, offset):
        result = 0
        shift = 0
        while offset < len(data):
            byte = data[offset]
            offset += 1
            result |= (byte & 0x7f) << shift
            if (byte & 0x80) == 0:
                return result, offset
            shift += 7
        raise Exception("Varint overflow or EOF")

    def _write_varint(self, value):
        bytes_list = []
        while value >= 128:
            bytes_list.append((value & 0x7f) | 0x80)
            value >>= 7
        bytes_list.append(value)
        return bytes(bytes_list)

    def _decode_envelope_proto(self, data):
        env = {}
        offset = 0
        while offset < len(data):
            tag, offset = self._read_varint(data, offset)
            field_num = tag >> 3
            wire_type = tag & 7

            if wire_type == 2:
                length, offset = self._read_varint(data, offset)
                val = data[offset:offset+length]
                offset += length

                if field_num == 1:
                    env["sender_urn"] = val.decode("utf-8")
                elif field_num == 2:
                    env["sender_static_pubkey"] = val
                elif field_num == 3:
                    env["ephemeral_pubkey"] = val
                elif field_num == 4:
                    env["nonce"] = val
                elif field_num == 5:
                    env["ciphertext"] = val
                elif field_num == 6:
                    env["tag"] = val
                elif field_num == 7:
                    env["message_id"] = val.decode("utf-8")
            else:
                if wire_type == 0:
                    _, offset = self._read_varint(data, offset)
                elif wire_type == 5:
                    offset += 4
                elif wire_type == 1:
                    offset += 8
                else:
                    raise Exception(f"Unsupported wire type: {wire_type}")
        return env

    def _encode_envelope_proto(self, env):
        def encode_len_delimited(field_num, val):
            tag = (field_num << 3) | 2
            return self._write_varint(tag) + self._write_varint(len(val)) + val

        return (
            encode_len_delimited(1, env["sender_urn"].encode("utf-8")) +
            encode_len_delimited(2, env["sender_static_pubkey"]) +
            encode_len_delimited(3, env["ephemeral_pubkey"]) +
            encode_len_delimited(4, env["nonce"]) +
            encode_len_delimited(5, env["ciphertext"]) +
            encode_len_delimited(6, env["tag"]) +
            encode_len_delimited(7, env["message_id"].encode("utf-8"))
        )
