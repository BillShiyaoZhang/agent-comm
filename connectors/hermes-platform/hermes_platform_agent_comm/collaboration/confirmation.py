"""Lease-bounded native questions using Hermes's request-scoped cancellation.

The generic clarify callback has no per-request timeout. This verified host seam
uses the same native request router, with a handle that withdraws only this card.
The shared runtime still owns every authorization decision and consumes its own
private token; neither that token nor its digest enters a renderer request.
"""
from __future__ import annotations

import math
import threading
import time


def request_confirmation(store, approval_id, session, question, *, revalidate):
    native = session.opaque
    revalidate(session)
    from tui_gateway import server_requests
    send = getattr(server_requests, "send_async", None)
    timeout_reader = getattr(native.server, "_clarify_timeout_seconds", None)
    requests = getattr(server_requests, "_open", None)
    request_lock = getattr(server_requests, "_lock", None)
    emit_cancel = getattr(server_requests, "_emit_cancel", None)
    if (not callable(send) or not callable(timeout_reader) or not isinstance(requests, dict)
            or not callable(getattr(request_lock, "__enter__", None)) or not callable(emit_cancel)):
        raise ValueError("此 Hermes 版本没有可按原生授权期限关闭的问题接口；没有取得授权。")

    # Runtime 0.1.2 persists the actual presentation lease but its portable
    # InteractionPort passes only the question. Read that existing private lease
    # from this invocation's Store; never mint or extend one in the connector.
    approval = store._get("approval", approval_id)
    if (not approval or approval.get("status") != "presenting"
            or approval.get("owner_session") != session.owner_session
            or approval.get("question") != question
            or not isinstance(approval.get("token_hash"), str)):
        raise ValueError("当前问题没有匹配的原生授权租约。")
    fingerprint = approval["token_hash"]
    deadline = min(approval.get("lease_until", 0), approval.get("expires_at", 0))
    if not isinstance(deadline, (int, float)) or not math.isfinite(deadline):
        raise ValueError("原生授权租约期限无效。")
    remaining = deadline - store.clock()
    host_timeout = timeout_reader()
    if host_timeout is not None:
        if not isinstance(host_timeout, (int, float)) or not math.isfinite(host_timeout):
            raise ValueError("Hermes 原生问题期限无效。")
        if host_timeout > 0:
            remaining = min(remaining, host_timeout)
    if remaining <= 0 or not store._approval_current(approval):
        return None
    # A wall-clock adjustment cannot lengthen this particular UI wait.
    monotonic_deadline = time.monotonic() + remaining
    question += ("\n\n请在这条 Hermes 原生问题的回答框中输入“同意”或“拒绝”。"
                 "条件性回答不会视为授权；关闭、超时或在主聊天框发送文字均不会自动批准。"
                 f"\n本次问题最多等待 {math.ceil(remaining)} 秒，到期自动关闭；"
                 "事项仍有效时可以重新展示问题，原委托期限不会延长。")
    ready = threading.Event()
    response = [None]

    def on_result(result):
        response[0] = result.get("answer") if isinstance(result, dict) else None
        ready.set()

    def still_current():
        revalidate(session)
        current = store._get("approval", approval_id)
        return bool(current and current.get("status") == "presenting"
                    and current.get("owner_session") == session.owner_session
                    and current.get("token_hash") == fingerprint
                    and current.get("lease_until", 0) > store.clock()
                    and current.get("expires_at", 0) > store.clock()
                    and store._approval_current(current))

    if not still_current():
        return None
    settle = None
    reason = "interrupted"
    try:
        settle = send("clarify", native.ui_session_id,
                      {"question": question, "choices": None}, on_result)
        if not callable(settle):
            raise ValueError("Hermes 没有返回原生问题取消句柄。")
        while True:
            if not still_current():
                reason = "expired_or_superseded"
                return None
            remaining = monotonic_deadline - time.monotonic()
            if remaining <= 0:
                reason = "timeout"
                return None
            if ready.wait(min(remaining, 0.25)):
                # Native cancellation also resolves the wait with None. The
                # runtime revalidates once more before consuming its lease.
                if not still_current() or time.monotonic() >= monotonic_deadline:
                    reason = "expired_or_superseded"
                    return None
                reason = "answered"
                return response[0]
    finally:
        if callable(settle):
            settle(reason)
        else:
            # Hermes registers before writing the frame/returning settle. If
            # that write (or a tool interrupt during it) raises, withdraw only
            # the request carrying this invocation's callback object. A broad
            # cancel(sid) could incorrectly close another native question.
            with request_lock:
                stranded = [req for req in requests.values()
                            if getattr(req, "on_result", None) is on_result
                            and getattr(req, "sid", None) == native.ui_session_id]
                for req in stranded:
                    requests.pop(req.id, None)
            for req in stranded:
                try:
                    emit_cancel(req, reason)
                except Exception:
                    # A disconnected renderer cannot receive cancellation, but
                    # the removed request will not be replayed or accept a late
                    # response. Preserve the original registration failure.
                    pass
