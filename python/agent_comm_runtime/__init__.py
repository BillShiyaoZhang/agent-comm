"""Portable local collaboration runtime. No host SDK is imported here."""

__version__ = "0.1.7"
API_VERSION = "1.0"

from .store import Store
from .ports import AdapterRegistry, Descriptor, HostSession, MemorySnapshot, Unsupported
from .runtime import Runtime

__all__ = ["API_VERSION", "Store", "AdapterRegistry", "Descriptor", "HostSession", "MemorySnapshot", "Unsupported", "Runtime"]
