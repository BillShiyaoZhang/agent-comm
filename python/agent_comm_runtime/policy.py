"""Strict, deterministic policy for the deliberately small collaboration surface.

This module does not authenticate people or collect consent. The host must supply
the current trusted scope and atomically enforce usage at execution. In particular,
a model's labels or assertion that the owner approved never constitute authority.
"""
from __future__ import annotations

from collections.abc import Mapping
from datetime import datetime, timedelta, timezone
import hashlib
import json
import re


CAPABILITIES = frozenset({"share_slots", "share_resource", "propose_meeting", "accept_meeting", "send_text"})
_SCOPE_FIELDS = frozenset({"purpose", "topic", "capabilities", "recipient_ids", "participant_ids",
                           "resource_ids", "window_start", "window_end", "max_duration_minutes",
                           "max_candidates", "max_actions", "expires_at", "allowed_windows"})
_PAYLOAD_FIELDS = {
    "share_slots": frozenset({"slots"}),
    "share_resource": frozenset({"resource_id"}),
    "propose_meeting": frozenset({"proposal_id", "version", "topic", "participant_ids", "start", "end"}),
    "accept_meeting": frozenset({"proposal_id", "version", "topic", "participant_ids", "start", "end"}),
    "send_text": frozenset({"text"}),
}
_RFC3339 = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,6})?(?:Z|[+-]\d{2}:\d{2})\Z")
_LABELS = {"share_slots": "分享候选空档", "share_resource": "分享指定资料全文",
           "propose_meeting": "提出会议方案", "accept_meeting": "接受会议方案",
           "send_text": "发送自由文本（每次须确认确切全文）"}


class _ValidationError(ValueError):
    def __init__(self, message, *, missing=False):
        super().__init__(message)
        self.missing = missing


def _object(value, fields, path, *, optional=frozenset()):
    if not isinstance(value, dict):
        raise _ValidationError(f"{path} must be a JSON object")
    if any(not isinstance(key, str) for key in value):
        raise _ValidationError(f"{path} keys must be strings")
    extra = set(value) - fields
    if extra:
        raise _ValidationError(f"{path} has unsupported fields: {', '.join(sorted(extra))}")
    missing = fields - set(value) - optional
    if missing:
        raise _ValidationError(f"{path} is missing: {', '.join(sorted(missing))}", missing=True)


def _string(value, path, maximum=1000, *, identifier=False):
    if not isinstance(value, str) or not value.strip() or len(value) > maximum:
        raise _ValidationError(f"{path} must be a nonempty string of at most {maximum} characters")
    try:
        value.encode("utf-8")
    except UnicodeError as exc:
        raise _ValidationError(f"{path} must be valid Unicode") from exc
    if any(ord(char) < 32 and char not in "\n\t" for char in value):
        raise _ValidationError(f"{path} contains unsupported control characters")
    if identifier and (any(char.isspace() for char in value) or len(value) > 256):
        raise _ValidationError(f"{path} must be an identifier without whitespace")
    return value


def _identifier(value, path):
    return _string(value, path, 256, identifier=True)


def _ids(value, path, *, nonempty=False, capabilities=False):
    if not isinstance(value, list) or len(value) > 128 or (nonempty and not value):
        raise _ValidationError(f"{path} must be {'a nonempty' if nonempty else 'a'} list of at most 128 identifiers")
    result = sorted({_identifier(item, f"{path}[]") for item in value})
    if capabilities and any(item not in CAPABILITIES for item in result):
        raise _ValidationError(f"{path} contains an unsupported capability")
    return result


def _integer(value, path, minimum, maximum):
    if type(value) is not int or not minimum <= value <= maximum:
        raise _ValidationError(f"{path} must be an integer in [{minimum}, {maximum}]")
    return value


def _time(value, path):
    if not isinstance(value, str) or not _RFC3339.fullmatch(value):
        raise _ValidationError(f"{path} must be RFC3339 with an explicit timezone and seconds")
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00")).astimezone(timezone.utc)
    except (ValueError, OverflowError) as exc:
        raise _ValidationError(f"{path} is not a valid datetime") from exc
    return parsed.isoformat().replace("+00:00", "Z")


def _dt(value):
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def _interval(value, path):
    _object(value, frozenset({"start", "end"}), path)
    result = {key: _time(value[key], f"{path}.{key}") for key in ("start", "end")}
    if _dt(result["start"]) >= _dt(result["end"]):
        raise _ValidationError(f"{path}.end must be later than start")
    return result


def validate_scope(scope):
    """Return an independent normalized scope; unknown fields/capabilities fail."""
    _object(scope, _SCOPE_FIELDS, "scope", optional=frozenset({"allowed_windows"}))
    result = {"purpose": _string(scope["purpose"], "scope.purpose"),
              "topic": _string(scope["topic"], "scope.topic", 500)}
    for key in ("capabilities", "recipient_ids", "participant_ids", "resource_ids"):
        result[key] = _ids(scope[key], f"scope.{key}",
                           nonempty=key in {"capabilities", "recipient_ids"}, capabilities=key == "capabilities")
    for key in ("window_start", "window_end", "expires_at"):
        result[key] = _time(scope[key], f"scope.{key}")
    if _dt(result["window_start"]) >= _dt(result["window_end"]):
        raise _ValidationError("scope.window_end must be later than window_start")
    # Absence preserves the original outer-window contract and its normalized
    # JSON. An explicit empty list is invalid, never a silently unlimited scope.
    if "allowed_windows" in scope:
        windows = scope["allowed_windows"]
        if not isinstance(windows, list) or not 1 <= len(windows) <= 128:
            raise _ValidationError("scope.allowed_windows must contain 1 to 128 intervals")
        normalized = [_interval(window, f"scope.allowed_windows[{index}]")
                      for index, window in enumerate(windows)]
        if any(_dt(window["start"]) < _dt(result["window_start"])
               or _dt(window["end"]) > _dt(result["window_end"]) for window in normalized):
            raise _ValidationError("scope.allowed_windows must be entirely inside window_start/window_end")
        result["allowed_windows"] = normalized
    for key, maximum in (("max_duration_minutes", 10080), ("max_candidates", 128), ("max_actions", 100000)):
        result[key] = _integer(scope[key], f"scope.{key}", 1, maximum)
    return result


def validate_action(action):
    """Validate typed payloads only. An absent required field is distinguishable."""
    # Unsupported capabilities never become approvable merely by omitting a field.
    if isinstance(action, dict) and "capability" in action:
        candidate = _identifier(action["capability"], "action.capability")
        if candidate not in CAPABILITIES:
            raise _ValidationError("action.capability is unsupported")
    _object(action, frozenset({"capability", "recipient_ids", "payload"}), "action")
    capability = _identifier(action["capability"], "action.capability")
    if capability not in CAPABILITIES:
        raise _ValidationError("action.capability is unsupported")
    recipients = _ids(action["recipient_ids"], "action.recipient_ids", nonempty=True)
    payload = action["payload"]
    _object(payload, _PAYLOAD_FIELDS[capability], "action.payload")
    if capability == "share_slots":
        slots = payload["slots"]
        if not isinstance(slots, list) or not 1 <= len(slots) <= 128:
            raise _ValidationError("action.payload.slots must contain 1 to 128 intervals")
        normalized = [_interval(slot, f"action.payload.slots[{index}]") for index, slot in enumerate(slots)]
        if len({(slot["start"], slot["end"]) for slot in normalized}) != len(normalized):
            raise _ValidationError("action.payload.slots contains duplicate intervals")
        payload = {"slots": normalized}
    elif capability == "share_resource":
        payload = {"resource_id": _identifier(payload["resource_id"], "action.payload.resource_id")}
    elif capability in {"propose_meeting", "accept_meeting"}:
        interval = _interval({key: payload[key] for key in ("start", "end")}, "action.payload.interval")
        payload = {"proposal_id": _identifier(payload["proposal_id"], "action.payload.proposal_id"),
                   "version": _integer(payload["version"], "action.payload.version", 1, 2147483647),
                   "topic": _string(payload["topic"], "action.payload.topic", 500),
                   "participant_ids": _ids(payload["participant_ids"], "action.payload.participant_ids", nonempty=True),
                   **interval}
    else:
        payload = {"text": _string(payload["text"], "action.payload.text", 8000)}
    return {"capability": capability, "recipient_ids": recipients, "payload": payload}


def evaluate(scope, action, *, used_count=0, now=None):
    """Judge current scope only; ``ask`` is never an execution permission.

    Purpose is displayed for review, not semantically interpreted as a license.
    Expiry and count are hard limits: an exact-action approval cannot override them.
    """
    try:
        scope = validate_scope(scope)
    except ValueError as exc:
        return {"decision": "deny", "reasons": ["invalid_scope"], "delta": {"error": str(exc)}}
    if type(used_count) is not int or used_count < 0:
        return {"decision": "deny", "reasons": ["invalid_usage"], "delta": {}}
    if now is None:
        now = datetime.now(timezone.utc)
    if not isinstance(now, datetime) or now.tzinfo is None or now.utcoffset() is None:
        return {"decision": "deny", "reasons": ["invalid_time"], "delta": {}}
    if now >= _dt(scope["expires_at"]):
        return {"decision": "deny", "reasons": ["expired"], "delta": {"expires_at": scope["expires_at"]}}
    if used_count >= scope["max_actions"]:
        return {"decision": "deny", "reasons": ["action_limit_reached"],
                "delta": {"max_actions": {"allowed": scope["max_actions"], "requested": used_count + 1}}}
    try:
        action = validate_action(action)
    except _ValidationError as exc:
        return {"decision": "clarify" if exc.missing else "deny",
                "reasons": ["missing_action_fields" if exc.missing else "invalid_action"],
                "delta": {"error": str(exc)}}
    delta = {}

    def subset(key, requested):
        outside = sorted(set(requested) - set(scope[key]))
        if outside:
            delta[key] = {"allowed": scope[key], "requested": requested, "outside": outside}

    subset("capabilities", [action["capability"]])
    subset("recipient_ids", action["recipient_ids"])
    capability, payload = action["capability"], action["payload"]
    intervals = []
    if capability == "share_resource":
        subset("resource_ids", [payload["resource_id"]])
    elif capability == "share_slots":
        intervals = payload["slots"]
        if len(intervals) > scope["max_candidates"]:
            delta["max_candidates"] = {"allowed": scope["max_candidates"], "requested": len(intervals)}
    elif capability in {"propose_meeting", "accept_meeting"}:
        subset("participant_ids", payload["participant_ids"])
        if payload["topic"] != scope["topic"]:
            delta["topic"] = {"allowed": scope["topic"], "requested": payload["topic"]}
        intervals = [{key: payload[key] for key in ("start", "end")}]
    outside_window = [item for item in intervals if _dt(item["start"]) < _dt(scope["window_start"])
                      or _dt(item["end"]) > _dt(scope["window_end"])]
    if outside_window:
        delta["window"] = {"allowed": {"start": scope["window_start"], "end": scope["window_end"]},
                           "requested": outside_window}
    if "allowed_windows" in scope:
        # Do not merge intervals: spanning a gap (or multiple adjacent/overlapping
        # windows) is outside a mandate that permits each listed window separately.
        outside_allowed = [item for item in intervals if not any(
            _dt(window["start"]) <= _dt(item["start"]) and _dt(item["end"]) <= _dt(window["end"])
            for window in scope["allowed_windows"])]
        if outside_allowed:
            delta["allowed_windows"] = {"allowed": scope["allowed_windows"], "requested": outside_allowed}
    # Use timedeltas, not rounded minutes, so 30m + 1 microsecond exceeds 30m.
    too_long = [item for item in intervals if _dt(item["end"]) - _dt(item["start"])
                > timedelta(minutes=scope["max_duration_minutes"])]
    if too_long:
        delta["max_duration_minutes"] = {"allowed": scope["max_duration_minutes"], "requested": too_long}
    reasons = ["scope_exceeded"] if delta else []
    if capability == "send_text":
        reasons.append("free_text_requires_approval")
        delta["exact_text"] = {"requested": payload["text"]}
    return {"decision": "ask" if reasons else "allow", "reasons": reasons or ["within_scope"], "delta": delta}


def _quoted(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True)


def render_scope(scope):
    """Render every boundary for the owner; IDs are never resolved by inference."""
    scope = validate_scope(scope)
    if "allowed_windows" in scope:
        actual_windows = ("实际允许时段（UTC；每个候选/会议须完整落在下列某一个时段内）：\n" +
                          "\n".join(f"{index}. {window['start']} 至 {window['end']}"
                                    for index, window in enumerate(scope["allowed_windows"], 1)))
    else:
        actual_windows = "未指定分段允许时段：以上时间范围内的每一天、每个小时均可使用，不自动排除夜间或周末。"
    return "\n".join([
        f"委托目的：{_quoted(scope['purpose'])}",
        "目的文字只是说明，不构成机器可检查的限制；例如工作日下午须明确列出实际允许时段。",
        f"会议主题（须精确相同）：{_quoted(scope['topic'])}",
        "允许能力：" + "、".join(f"{_LABELS[item]} [{item}]" for item in scope["capabilities"]),
        f"收件人 ID：{_quoted(scope['recipient_ids'])}",
        f"参与人 ID：{_quoted(scope['participant_ids'])}",
        f"可分享资料 ID（确切全文由宿主附上）：{_quoted(scope['resource_ids'])}",
        f"候选/会议时间范围（UTC）：{scope['window_start']} 至 {scope['window_end']}",
        actual_windows,
        f"单次时长上限：{scope['max_duration_minutes']} 分钟；每位收件人的累计候选上限：{scope['max_candidates']}",
        f"累计动作上限：{scope['max_actions']}；委托到期（UTC）：{scope['expires_at']}",
        "自由文本每次另行确认确切全文；本委托不包括支付、日历写入或其它未列出的执行能力。",
    ])


def render_action(action):
    """Render the exact typed action. The host adds the compiled message/resources."""
    action = validate_action(action)
    return "\n".join([f"动作：{_LABELS[action['capability']]} [{action['capability']}]",
                       f"收件人 ID：{_quoted(action['recipient_ids'])}",
                       f"确切参数：{_quoted(action['payload'])}"])


def compile_message(action, resources):
    """Construct the complete outgoing text, with no model-added pre/postamble.

    Resource text is an immutable, explicitly reviewed host snapshot. The digest
    check detects accidental substitution; this function does not grant access.
    """
    action = validate_action(action)
    capability, payload = action["capability"], action["payload"]
    if capability == "send_text":
        return payload["text"]
    if capability == "share_resource":
        if not isinstance(resources, Mapping):
            raise ValueError("resources must be a mapping of registered snapshots")
        resource_id = payload["resource_id"]
        resource = resources.get(resource_id)
        if not isinstance(resource, Mapping) or resource.get("resource_id") != resource_id:
            raise ValueError("resource snapshot is missing or has a different ID")
        title = _string(resource.get("title"), "resource.title", 200)
        text = _string(resource.get("text"), "resource.text", 8000)
        digest = resource.get("sha256")
        if not isinstance(digest, str) or not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise ValueError("resource.sha256 must be a lowercase SHA256 digest")
        if hashlib.sha256(text.encode("utf-8")).hexdigest() != digest:
            raise ValueError("resource snapshot content does not match its digest")
        return f"{title}\n\n{text}"
    if capability == "share_slots":
        return "可选时段（UTC）：\n" + "\n".join(
            f"{index}. {item['start']} 至 {item['end']}"
            for index, item in enumerate(payload["slots"], 1))
    verb = "提出会议方案" if capability == "propose_meeting" else "接受以下会议方案"
    return "\n".join([f"{verb}：{_quoted(payload['proposal_id'])}，版本 {payload['version']}",
                       f"主题：{_quoted(payload['topic'])}",
                       f"参与人 ID：{_quoted(payload['participant_ids'])}",
                       f"时间（UTC）：{payload['start']} 至 {payload['end']}"])
