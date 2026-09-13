from setuptools import find_packages, setup

setup(
    name="hermes-platform-agent-comm",
    version="1.1.0",
    description="Durable agent-comm messaging adapter for Hermes Gateway",
    packages=find_packages(),
    package_data={"hermes_platform_agent_comm": ["plugin.yaml"]},
    python_requires=">=3.11",
    install_requires=["aiohttp>=3.14.3,<4"],
    entry_points={"hermes_agent.plugins": ["agent_comm = hermes_platform_agent_comm.plugin"]},
)
