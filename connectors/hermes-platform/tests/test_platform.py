import unittest
import os
import sys

# Add package root to sys.path
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from hermes_platform_agent_comm import AgentCommPlatform

class MockGateway:
    def __init__(self):
        self.received = []
    def on_message(self, msg):
        self.received.append(msg)

class TestAgentCommPlatform(unittest.TestCase):
    def test_helper_integration(self):
        print("Running python platform helper integration test...")
        gateway = MockGateway()
        
        # Test paths
        test_keys_dir = os.path.abspath(os.path.join(
            os.path.dirname(__file__), 
            "../../../agent-comm-platform/agent-comm/test_keys"
        ))
        helper_path = os.path.abspath(os.path.join(
            os.path.dirname(__file__), 
            "../../../agent-comm-platform/agent-comm/cmd/helper/agent-comm-helper"
        ))
        
        os.environ["AGENT_COMM_HELPER_PATH"] = helper_path

        platform = AgentCommPlatform(gateway, {
            "platform_url": "http://localhost:8080",
            "urn": "urn:hermes:agent:VVDkKJJAExLmCgqhLW26AM",
            "keys_path": test_keys_dir
        })

        # Directly invoke helper via private method to check connectivity
        res = platform._invoke_helper(["init", test_keys_dir])
        print("Go helper response to Python invocation:", res)
        self.assertEqual(res["urn"], "urn:hermes:agent:VVDkKJJAExLmCgqhLW26AM")
        print("Python test PASSED successfully!")

if __name__ == "__main__":
    unittest.main()
