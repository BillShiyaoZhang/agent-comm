"""Shared network identity syntax; namespace does not determine authority.

Existing signed Web identities use urn:hermes:agent, and the Go SDK supports
owner-selected namespaces. Actual key-to-URN and envelope authentication belongs
to the helper/transport; this function only validates a bounded identity string.
"""
import re

_URN = re.compile(r"urn:[A-Za-z0-9][A-Za-z0-9._:-]*:[A-Za-z0-9][A-Za-z0-9._-]*")


def validate_urn(value):
    """Accept namespace-independent URNs while retaining local API compatibility.

    The final component cannot be empty. Existing API callers may use readable
    IDs containing ._-; authenticated production identities end in their key
    fingerprint, which the transport verifies independently.
    """
    if not isinstance(value, str) or len(value) > 256 or not _URN.fullmatch(value):
        raise ValueError("Provide an explicit network URN with a nonempty identity; a display name is not an identity")
    return value
