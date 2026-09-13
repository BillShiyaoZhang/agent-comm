import importlib.util


def check_requirements() -> bool:
    return importlib.util.find_spec("aiohttp") is not None


def _apply_yaml_config(yaml_cfg: dict, platform_cfg: dict) -> dict:
    # Both platforms.agent_comm.extra and legacy flat configuration are accepted.
    extra = dict(platform_cfg.get("extra") or {})
    keys = ("platform_url", "urn", "state_path", "connect_timeout", "request_timeout",
            "reconcile_interval", "retry_delay", "allow_from")
    extra.update({key: platform_cfg[key] for key in keys if key in platform_cfg})
    return extra


def register(ctx):
    # Keep plugin discovery possible when an optional dependency is missing.
    def create_adapter(config):
        from .platform import AgentCommAdapter
        return AgentCommAdapter(config)

    ctx.register_platform(
        name="agent_comm",
        label="Agent Comm",
        adapter_factory=create_adapter,
        check_fn=check_requirements,
        apply_yaml_config_fn=_apply_yaml_config,
        allow_update_command=False,
        install_hint="Install this plugin with the same Python environment used by Hermes Gateway.",
        emoji="📞",
    )
