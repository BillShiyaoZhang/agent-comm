from setuptools import setup, find_packages

setup(
    name="hermes-platform-agent-comm",
    version="1.0.0",
    description="Unified Agent Comm platform adaptor for Hermes framework",
    packages=find_packages(),
    python_requires=">=3.8",
    install_requires=[],
    entry_points={
        "hermes_agent.plugins": [
            "agent_comm = hermes_platform_agent_comm.plugin",
        ]
    }
)
