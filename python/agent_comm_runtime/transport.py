"""Bounded loopback-only client of the existing durable helper mailbox API."""

import json
from urllib.parse import urlsplit, quote
from urllib.error import HTTPError
from urllib.request import (HTTPRedirectHandler, ProxyHandler, Request, build_opener)

from .ports import Descriptor


class _NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError("The plaintext helper API must not redirect")


class HelperDisclosureError(RuntimeError):
    def __init__(self, code, disclosure):
        self.code = code
        self.disclosure = disclosure
        super().__init__(f"{code}: inspect GET /api/v2/disclosure and use the v2 helper route")


class HelperTransport:
    descriptor = Descriptor("loopback-helper", "transport", ("durable_mailbox",))

    def __init__(self, helper_url="http://127.0.0.1:45042", timeout=10):
        if not isinstance(helper_url, str):
            raise ValueError("helper_url must be a loopback HTTP URL")
        parsed = urlsplit(helper_url)
        if (parsed.scheme != "http" or parsed.hostname not in {"localhost", "127.0.0.1", "::1"}
                or parsed.username or parsed.password or parsed.query or parsed.fragment
                or parsed.path.rstrip("/") not in {"", "/api/v1/mq"}):
            raise ValueError("helper_url must address the local plaintext helper, never the cloud platform")
        if isinstance(timeout, bool) or not isinstance(timeout, (int, float)) or not 0 < timeout <= 15:
            raise ValueError("Helper timeout must be greater than zero and at most 15 seconds")
        # Resolve localhost to the literal loopback address rather than ambient DNS.
        host = "[::1]" if parsed.hostname == "::1" else "127.0.0.1"
        self.base = f"http://{host}" + (f":{parsed.port}" if parsed.port is not None else "") + "/api/v1/mq"
        self.origin = self.base.removesuffix("/api/v1/mq")
        self.timeout = timeout
        self._opener = build_opener(ProxyHandler({}), _NoRedirect())

    def _request(self, endpoint, body=None):
        return self._request_url(f"{self.base}/{endpoint}", body)

    def _request_url(self, target, body=None):
        data = None if body is None else json.dumps(body, ensure_ascii=False, allow_nan=False).encode("utf-8")
        request = Request(target, data=data, headers={"Content-Type": "application/json"})
        with self._opener.open(request, timeout=self.timeout) as response:
            payload = response.read(2_000_001)
            if len(payload) > 2_000_000:
                raise ValueError("Helper response exceeded the bounded response size")
            result = json.loads(payload)
        if not isinstance(result, dict):
            raise ValueError("Helper response must be a JSON object")
        return result

    def presence(self, urn):
        # Presence shares the helper origin, outside the mailbox route.
        from .identity import validate_urn
        endpoint = self.base.removesuffix("/mq") + "/presence?urn=" + quote(validate_urn(urn), safe="")
        with self._opener.open(Request(endpoint), timeout=self.timeout) as response:
            payload = response.read(16001)
        if len(payload) > 16000:
            raise ValueError("Helper presence response exceeded its size bound")
        return json.loads(payload)

    def store(self, body):
        try:
            disclosure = self._request_url(f"{self.origin}/api/v2/disclosure")
        except HTTPError as exc:
            if exc.code == 404:  # Helper predates the v2 source upgrade.
                return self._request("store", body)
            raise
        if disclosure.get("policy_verified") is True and disclosure.get("v2_send_ready") is True:
            return self._request_url(f"{self.origin}/api/v2/mq/store", body)
        raise HelperDisclosureError(disclosure.get("legacy_send_code") or "policy_unavailable", disclosure)

    def store_managed_control(self, body):
        if body.get("kind") != "control.response":
            raise ValueError("Managed helper route is only for control.response")
        try:
            return self._request_url(f"{self.origin}/api/v1/managed/mq/store", body)
        except HTTPError as exc:
            if exc.code != 404:
                raise
            try:
                self._request_url(f"{self.origin}/api/v2/disclosure")
            except HTTPError as state_exc:
                if state_exc.code == 404:  # Compatible with an older helper.
                    return self._request("store", body)
            raise

    def retrieve(self):
        result = self._request("retrieve")
        messages = result.get("messages")
        if not isinstance(messages, list) or len(messages) > 1000:
            raise ValueError("Helper returned an invalid or oversized inbox")
        return messages

    def ack(self, message_ids):
        result = self._request("ack", {"message_ids": message_ids})
        if result.get("success") is not True:
            raise ValueError("Helper did not accept the local inbox acknowledgment")
        return result
