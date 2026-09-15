"""Gateway timer integration; no private-owner model or native context is created."""
from .hermes import profile_principal, read_settings, state_path
from .store import Store
from .transport import HelperTransport


def tick_profile_worker(extra=None):
    settings = {**(extra or {}), **read_settings()}
    path = state_path(settings)
    if settings.get("collaboration_enabled") is not True or not path.exists():
        return {"status": "disabled_or_empty"}
    from agent_comm_runtime.worker import run_worker_tick
    store = Store(path, local_urn=settings.get("urn"))
    try:
        transport = HelperTransport(settings.get("platform_url", "http://127.0.0.1:45042"), timeout=10)
        # One finite task step per reconcile pass keeps the inbound loop bounded.
        return run_worker_tick(store, profile_principal(), transport, max_tasks=1)
    finally:
        store.close()
