from setuptools import find_packages, setup

setup(
    name="hermes-platform-agent-comm",
    version="1.5.4",
    description="Durable agent-comm messaging adapter for Hermes Gateway",
    packages=find_packages(),
    package_data={"hermes_platform_agent_comm": ["plugin.yaml", "skills/personal-collaboration/SKILL.md"],
                  "hermes_platform_agent_comm.companion": ["plugin.yaml", "dashboard/*.json", "dashboard/*.py", "dashboard/*.js", "desktop/*.js"]},
    python_requires=">=3.11",
    install_requires=["aiohttp>=3.14.3,<4", "agent-comm-runtime>=0.1.3,<0.2"],
    entry_points={"hermes_agent.plugins": ["agent_comm = hermes_platform_agent_comm.plugin"],
                  "console_scripts": ["agent-comm-hermes-companion = hermes_platform_agent_comm.companion_export:main"]},
)
