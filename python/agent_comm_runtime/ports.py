"""Versioned, explicitly registered host extension ports.

Adapters are trusted Python code selected by the owner, never an incoming peer
or a model-provided import path. Structural validation does not certify a host's
authentication implementation; each host must test its actual security seam.
"""
from dataclasses import asdict, dataclass, field
from importlib import metadata
from typing import Any, Protocol

API_VERSION = "1.0"


class Unsupported(ValueError):
    """An unavailable extension operation, without an implicit fallback."""


@dataclass(frozen=True)
class Descriptor:
    name: str
    port: str
    capabilities: tuple[str, ...]
    api_version: str = API_VERSION


@dataclass(frozen=True)
class HostSession:
    principal_id: str
    session_id: str
    turn_id: str
    opaque: Any = field(default=None, repr=False, compare=False)

    def __post_init__(self):
        for name in ("principal_id", "session_id", "turn_id"):
            value = getattr(self, name)
            if (not isinstance(value, str) or not value or len(value) > 240
                    or "|" in value or any(ord(c) < 32 for c in value)):
                raise ValueError(f"Invalid host-generated {name}")

    @property
    def owner_session(self):
        return f"{self.principal_id}|{self.session_id}"


@dataclass(frozen=True)
class MemorySnapshot:
    reference: str
    title: str
    text: str
    source: str
    version: str


class HostPort(Protocol):
    descriptor: Descriptor
    def capture(self, context: Any) -> HostSession: ...
    def revalidate(self, session: HostSession) -> None: ...


class MemoryPort(Protocol):
    descriptor: Descriptor
    def search(self, session: HostSession, query: str, limit: int) -> list[dict]: ...
    def read_snapshot(self, session: HostSession, reference: str, max_chars: int) -> MemorySnapshot: ...


class InteractionPort(Protocol):
    descriptor: Descriptor
    def request_confirmation(self, session: HostSession, question: str) -> str | None: ...


class TransportPort(Protocol):
    descriptor: Descriptor
    def store(self, body: dict) -> dict: ...
    def retrieve(self) -> list[dict]: ...
    def ack(self, message_ids: list[str]) -> dict: ...


# Every advertised capability must implement all its methods. Unknown names are
# rejected at registration, rather than creating unchecked execution surfaces.
_CONTRACTS = {
    "host": {"owner_context": ("capture", "revalidate"), "wake": ("wake",)},
    "memory": {"search": ("search",), "read_snapshot": ("read_snapshot",)},
    "interaction": {"confirmation": ("request_confirmation",), "notification": ("notify",)},
    "transport": {"durable_mailbox": ("store", "retrieve", "ack")},
}


class AdapterRegistry:
    def __init__(self):
        self._adapters = {}

    def register(self, adapter):
        descriptor = getattr(adapter, "descriptor", None)
        if not isinstance(descriptor, Descriptor):
            raise ValueError("An adapter must provide a Descriptor")
        if descriptor.api_version != API_VERSION:
            raise ValueError(f"Unsupported adapter API version {descriptor.api_version}; expected {API_VERSION}")
        if descriptor.port not in _CONTRACTS:
            raise ValueError("Unknown adapter port")
        if (not isinstance(descriptor.name, str) or not descriptor.name.strip() or len(descriptor.name) > 100
                or not isinstance(descriptor.capabilities, tuple)
                or len(set(descriptor.capabilities)) != len(descriptor.capabilities)):
            raise ValueError("Invalid adapter descriptor")
        if descriptor.port in self._adapters:
            raise ValueError(f"Port {descriptor.port} is already registered; construct another registry to change adapters")
        for capability in descriptor.capabilities:
            if capability not in _CONTRACTS[descriptor.port]:
                raise ValueError(f"Unknown {descriptor.port} capability {capability}")
            for method in _CONTRACTS[descriptor.port][capability]:
                if not callable(getattr(adapter, method, None)):
                    raise ValueError(f"Advertised {capability} requires callable {method}")
        if descriptor.port == "host" and "owner_context" not in descriptor.capabilities:
            raise ValueError("Host adapters must implement owner_context")
        self._adapters[descriptor.port] = adapter
        return self

    def require(self, port, capability):
        adapter = self._adapters.get(port)
        if adapter is None or capability not in adapter.descriptor.capabilities:
            raise Unsupported(f"{port}.{capability} is not supported by this agent")
        return adapter

    def load_entry_point(self, name, *, options=None, expected_port=None):
        """Load one owner-configured factory, never automatically all packages.

        The group is agent_comm_runtime.adapters. Loading executes trusted local
        plugin code; names/options belong in owner configuration, never tool or
        incoming message arguments. Factories receive one options dict.
        """
        if not isinstance(name, str) or not name.strip() or (options is not None and not isinstance(options, dict)):
            raise ValueError("Provide an explicit adapter name and options object")
        if expected_port is not None and expected_port not in _CONTRACTS:
            raise ValueError("Unknown expected adapter port")
        matches = [entry for entry in metadata.entry_points(group="agent_comm_runtime.adapters") if entry.name == name]
        if len(matches) != 1:
            raise Unsupported(f"Adapter {name!r} is not uniquely installed")
        factory = matches[0].load()
        if not callable(factory):
            raise ValueError("Adapter entry point must export a factory")
        adapter = factory(dict(options or {}))
        if expected_port is not None and getattr(getattr(adapter, "descriptor", None), "port", None) != expected_port:
            raise ValueError("Configured adapter implements the wrong port")
        return self.register(adapter)

    def describe(self):
        return {"api_version": API_VERSION,
                "adapters": [asdict(a.descriptor) for _, a in sorted(self._adapters.items())],
                "available_ports": sorted(self._adapters),
                "memory_export": "explicit_query_and_bounded_snapshot_only"}

    def invoke(self, port, capability, method, *args, **kwargs):
        """Host-code-only extension dispatch; never expose method names to peers.

        Optional host wake and interaction notification implementations use this
        entry point. Runtime model actions intentionally do not auto-wake or
        notify a user just because an incoming message arrived.
        """
        adapter = self.require(port, capability)
        if method not in _CONTRACTS[port][capability]:
            raise Unsupported(f"Method {method} is not part of {port}.{capability}")
        return getattr(adapter, method)(*args, **kwargs)
