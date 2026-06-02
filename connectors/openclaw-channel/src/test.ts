import { AgentCommChannel } from "./channel";
import { EventEmitter } from "events";
import * as path from "path";
import * as os from "os";

async function runTests() {
  console.log("Running `@agent-comm/openclaw-channel` tests...");

  const gateway = new EventEmitter();
  gateway.on("message", (msg) => {
    console.log("Gateway received message:", msg);
  });

  const testKeysDir = path.resolve(__dirname, "../../../agent-comm-platform/agent-comm/test_keys");
  const helperPath = path.resolve(__dirname, "../../../agent-comm-platform/agent-comm/cmd/helper/agent-comm-helper");

  // Set helper path env
  process.env.AGENT_COMM_HELPER_PATH = helperPath;

  const channel = new AgentCommChannel(gateway, {
    platform_url: "http://localhost:8080/api/v1/mq",
    urn: "urn:hermes:agent:VVDkKJJAExLmCgqhLW26AM", // matches test_keys URN
    keys_dir: testKeysDir
  });

  console.log("Invoking helper via channel for sign-store test...");
  // Use private method/invoker directly for testing helper communication
  try {
    const res = await (channel as any).invokeHelper(["init", testKeysDir]);
    console.log("Helper init command output:", res);
    if (res.urn !== "urn:hermes:agent:VVDkKJJAExLmCgqhLW26AM") {
      throw new Error(`URN mismatch: expected urn:hermes:agent:VVDkKJJAExLmCgqhLW26AM, got ${res.urn}`);
    }
    console.log("Tests PASSED successfully!");
  } catch (err: any) {
    console.error("Test failed:", err.message);
    process.exit(1);
  }
}

runTests();
