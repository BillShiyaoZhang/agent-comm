"""Small, read-only contact invitations; platform addresses are supplied, never guessed."""

from ipaddress import ip_address
import re
import unicodedata
from urllib.parse import urlsplit

from .identity import validate_urn


INTRODUCTION_URL = "https://github.com/BillShiyaoZhang/agent-comm#readme"


def validate_platform_url(value):
    """Validate displayable platform metadata without contacting the address."""
    error = ("platform_url must be an absolute HTTP(S) platform address without "
             "credentials, query, fragment, whitespace or control characters")
    if (not isinstance(value, str) or not 1 <= len(value) <= 2048
            or any(char.isspace() or unicodedata.category(char).startswith("C") for char in value)
            or any(char in value for char in "\\?#")):
        raise ValueError(error)
    try:
        parsed = urlsplit(value)
        if (parsed.scheme not in {"http", "https"} or not parsed.hostname
                or parsed.username is not None or parsed.password is not None
                or parsed.query or parsed.fragment
                or (parsed.port is not None and not 1 <= parsed.port <= 65535)):
            raise ValueError(error)
    except ValueError:
        raise ValueError(error) from None
    hostname = parsed.hostname.removesuffix(".")
    if hostname == "localhost" or hostname.endswith(".localhost"):
        raise ValueError("platform_url must be reachable by the recipient, not a loopback helper address")
    try:
        address = ip_address(hostname)
    except ValueError:
        address = None
        # urlsplit is a parser, not hostname validation. Reject malformed or
        # abbreviated numeric hosts rather than exporting ambiguous links.
        try:
            ascii_hostname = hostname.encode("idna").decode("ascii")
        except UnicodeError:
            raise ValueError(error) from None
        if ascii_hostname.casefold() == "localhost" or ascii_hostname.casefold().endswith(".localhost"):
            raise ValueError("platform_url must be reachable by the recipient, not a loopback helper address")
        labels = ascii_hostname.split(".")
        if (len(ascii_hostname) > 253
                or any(not re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?", label) for label in labels)
                or all(re.fullmatch(r"(?:[0-9]+|0[xX][0-9A-Fa-f]+)", label) for label in labels)):
            raise ValueError(error)
    if address is not None:
        address = getattr(address, "ipv4_mapped", None) or address
        if address.is_loopback or address.is_unspecified:
            raise ValueError("platform_url must be reachable by the recipient, not a loopback helper address")
    return value


def render_contact(urn, platform_url, *, is_self):
    """Return only the fields needed to forward an invitation."""
    urn = validate_urn(urn)
    platform_url = validate_platform_url(platform_url)
    subject = "加我为 agent 好友" if is_self else "加这位 agent 为好友"
    return {"status": "exported",
            "text": f"{subject}：{urn}；平台：{platform_url}；了解/接入：{INTRODUCTION_URL}。",
            "urn": urn, "platform_url": platform_url, "introduction_url": INTRODUCTION_URL}
