"""Owner tools for locally paired Hermes turns and authenticated control RPC.

The binding is installed by the platform adapter, never reconstructed from
tool arguments, source labels or a peer's claimed owner identity. Hermes copies
ContextVars into its turn and tool workers. A stale worker loses authority when
its durable turn finishes or the local pairing is revoked.
"""
from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import dataclass
import hashlib

from agent_comm_runtime import AdapterRegistry, Descriptor, HostSession, Runtime
from agent_comm_runtime.runtime import validate_args


_current_turn = ContextVar("agent_comm_paired_turn", default=None)
READ_ACTIONS = {
    "describe": "capabilities", "state": "collaboration.state", "attention": "attention.list",
    "inbox": "inbox.list", "resolve_contact": "contacts.list", "contact_requests": "contacts.requests",
    "collaborations": "collaboration.state", "export_contact": "contacts.list",
}


@dataclass(frozen=True)
class PairedTurn:
    bridge: object
    turn_id: str
    console_urn: str
    owner_principal: str
    settings: dict

    def validate(self, method):
        from agent.delegation_context import is_delegated_child_context
        if is_delegated_child_context():
            raise ValueError("Delegated agents cannot inherit the paired owner's collaboration authority")
        if _current_turn.get() is not self:
            raise ValueError("No host-bound paired conversation is active")
        pairing = self.bridge._authorized(self.console_urn, method)
        turn = self.bridge._get("turn", self.turn_id)
        if (pairing["owner_principal"] != self.owner_principal or not turn or turn["status"] != "running"
                or turn["console_urn"] != self.console_urn or turn["owner_principal"] != self.owner_principal):
            raise ValueError("The paired conversation has finished or its owner has changed")
        return pairing


@contextmanager
def bind_paired_turn(bridge, job, settings):
    token = _current_turn.set(PairedTurn(bridge, job["turn_id"], job["console_urn"], job["owner_principal"], dict(settings)))
    try:
        yield
    finally:
        _current_turn.reset(token)


class _OwnerHost:
    descriptor = Descriptor("hermes-paired-owner", "host", ("owner_context",))

    def __init__(self, owner, validate):
        principal, session = owner.split("|", 1)
        self.session = HostSession(principal, session, "paired-control")
        self.validate = validate

    def capture(self, context):
        self.validate()
        return self.session

    def revalidate(self, session):
        if session is not self.session:
            raise ValueError("Owner session changed")
        self.validate()


def execute_owner_action(store, owner, args, settings, *, validate=lambda: None):
    """Called only under the bridge transaction after its method authorization."""
    from .hermes import register_optional_ports
    validate_args(args)
    validate()
    # A model cannot answer its own approval. The paired UI's approval.respond
    # records an explicit owner decision; confirm can subsequently read it.
    if args["action"] == "confirm":
        result = store.confirmation_result(args["approval_id"], owner)
        if result is not None:
            return result
        if not any(item["approval_id"] == args["approval_id"] for item in store.state(owner)["pending_confirmations"]):
            raise ValueError("No visible pending approval exists")
        return {"status": "approval_required", "approval_id": args["approval_id"],
                "instruction": "Review and answer this pending approval in the paired web UI or the local Hermes conversation, then continue."}
    registry = AdapterRegistry().register(_OwnerHost(owner, validate))
    register_optional_ports(registry, settings)
    return Runtime(store, registry, platform_url=settings.get("public_platform_url")).dispatch(args)


def handle_paired_tool(args):
    binding = _current_turn.get()
    if binding is None:
        return None
    action = validate_args(args)
    method = READ_ACTIONS.get(action, "collaboration.execute")
    # Hold the same durable grant lock through execution, so revoke/re-pair is
    # serialized against writes and against disclosure of private state.
    with binding.bridge._transaction():
        binding.validate(method)
        owner = binding.owner_principal + "|remote:" + hashlib.sha256(binding.console_urn.encode()).hexdigest()[:24]
        return execute_owner_action(binding.bridge.store, owner, args, binding.settings,
                                    validate=lambda: binding.validate(method))


def paired_turn_available():
    return _current_turn.get() is not None


def register_remote_actions(bridge, settings):
    def execute(params, owner, **request_context):
        return bridge.store.execute_owner_once(owner,
            lambda: execute_owner_action(bridge.store, owner, params, settings), **request_context)
    bridge.register_handler("collaboration.execute", execute)
