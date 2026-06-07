import unittest
import os
import sys

# Add package root to sys.path
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

# Register agent_comm platform entry before importing/instantiating adapter
from gateway.platform_registry import PlatformEntry, platform_registry
entry = PlatformEntry(
    name="agent_comm",
    label="Agent Comm",
    adapter_factory=lambda cfg: None,
    check_fn=lambda: True,
)
platform_registry.register(entry)

from hermes_platform_agent_comm.platform import AgentCommAdapter
from gateway.config import PlatformConfig

class MockGateway:
    def __init__(self):
        self.received = []
    def on_message(self, msg):
        self.received.append(msg)

class TestAgentCommPlatform(unittest.TestCase):
    def test_adapter_initialization(self):
        print("Running python platform adapter initialization test...")
        gateway = MockGateway()
        
        test_keys_dir = os.path.abspath(os.path.join(
            os.path.dirname(__file__), 
            "../../../agent-comm-platform/agent-comm/test_keys"
        ))

        config = PlatformConfig(enabled=True, extra={
            "platform_url": "http://localhost:8080",
            "urn": "urn:hermes:agent:VVDkKJJAExLmCgqhLW26AM",
            "keys_path": test_keys_dir
        })

        adapter = AgentCommAdapter(config)

        # Verify configuration extraction and API URL resolution
        self.assertEqual(adapter.config.extra.get("platform_url"), "http://localhost:8080")
        self.assertEqual(adapter._get_api_url("subscribe"), "http://localhost:8080/api/v1/mq/subscribe")
        self.assertEqual(adapter._get_api_url("store"), "http://localhost:8080/api/v1/mq/store")
        
        # Test default/slash fallback URLs
        config_default = PlatformConfig(enabled=True)
        adapter_default = AgentCommAdapter(config_default)
        self.assertEqual(adapter_default._get_api_url("subscribe"), "http://localhost:45042/api/v1/mq/subscribe")

        print("Python test PASSED successfully!")

if __name__ == "__main__":
    unittest.main()
