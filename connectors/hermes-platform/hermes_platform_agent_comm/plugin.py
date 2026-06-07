import os
from gateway.platforms.base import BasePlatformAdapter
from gateway.config import Platform
from .platform import AgentCommAdapter

def check_requirements() -> bool:
    return True

def _apply_yaml_config(yaml_cfg: dict, platform_cfg: dict) -> dict | None:
    # Merges this platform's config block into PlatformConfig.extra
    return platform_cfg

def register(ctx):
    ctx.register_platform(
        name="agent_comm",
        label="Agent Comm",
        adapter_factory=lambda cfg: AgentCommAdapter(cfg),
        check_fn=check_requirements,
        apply_yaml_config_fn=_apply_yaml_config,
        emoji="📞",
    )
