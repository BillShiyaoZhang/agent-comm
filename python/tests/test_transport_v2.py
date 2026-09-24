import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from agent_comm_runtime.transport import HelperDisclosureError, HelperTransport


class _Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def _send(self, status, value):
        data = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == "/api/v2/disclosure" and self.server.disclosure is not None:
            self._send(200, self.server.disclosure)
        else:
            self._send(404, {"error": "not found"})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.stores.append((self.path, body))
        self._send(202, {"success": True, "message_id": body["message_id"], "status": "accepted"})


class HelperTransportV2Tests(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
        self.server.disclosure = None
        self.server.stores = []
        self.worker = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.worker.start()
        self.transport = HelperTransport(f"http://127.0.0.1:{self.server.server_port}")

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.worker.join(timeout=2)

    def test_ready_routes_v2_and_consent_never_downgrades(self):
        body = {"message_id": "message-1", "recipient_urn": "urn:agent-comm:agent:peer", "text": "hello"}
        self.server.disclosure = {"state": "ready", "policy_verified": True, "v2_send_ready": True}
        self.assertEqual(self.transport.store(body)["message_id"], "message-1")
        self.assertEqual(self.server.stores[-1][0], "/api/v2/mq/store")
        self.server.disclosure = {"state": "consent_required", "policy_verified": True,
                                  "v2_send_ready": False, "legacy_send_code": "consent_required"}
        with self.assertRaises(HelperDisclosureError) as caught:
            self.transport.store(body)
        self.assertEqual(caught.exception.code, "consent_required")
        self.assertEqual(len(self.server.stores), 1)

    def test_legacy_and_managed_routes_remain_explicit(self):
        body = {"message_id": "message-1", "text": "hello"}
        self.transport.store(body)  # Older helper has no disclosure endpoint.
        self.assertEqual(self.server.stores[-1][0], "/api/v1/mq/store")
        self.server.disclosure = {"state": "legacy_unconfigured", "legacy_send_code": "policy_root_required"}
        with self.assertRaises(HelperDisclosureError) as caught:
            self.transport.store(body)
        self.assertEqual(caught.exception.code, "policy_root_required")
        self.assertEqual(len(self.server.stores), 1)
        response = {"message_id": "control-response-1", "kind": "control.response"}
        self.transport.store_managed_control(response)
        self.assertEqual(self.server.stores[-1][0], "/api/v1/managed/mq/store")
        with self.assertRaises(ValueError):
            self.transport.store_managed_control(body)
