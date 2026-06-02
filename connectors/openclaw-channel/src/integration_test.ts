import { AgentCommChannel } from "./channel";
import { EventEmitter } from "events";
import * as path from "path";
import * as http from "http";
import * as crypto from "crypto";
import { execFileSync } from "child_process";

const keysDir = path.resolve(__dirname, "../../../agent-comm-platform/agent-comm/test_keys");
const helperPath = path.resolve(__dirname, "../../../agent-comm-platform/agent-comm/cmd/helper/agent-comm-helper");
process.env.AGENT_COMM_HELPER_PATH = helperPath;

async function runIntegrationTest() {
  console.log("Starting E2E integration test with mock platform...");


  let sseResponse: any = null;
  let receivedStore: any = null;
  let storeSignatureValid = false;
  let decryptedStoreText = "";

  // 1. Start Mock HTTP Server
  const server = http.createServer((req, res) => {
    const url = new URL(req.url || "", `http://${req.headers.host}`);

    if (url.pathname === "/api/v1/mq/subscribe") {
      // Handle SSE subscription
      console.log("Mock Server: Received SSE subscription request");
      
      // Verify signature headers
      const urn = req.headers["x-urn"];
      const ts = req.headers["x-timestamp"];
      const pubkey = req.headers["x-pubkey"];
      const sig = req.headers["x-signature"];
      if (!urn || !ts || !pubkey || !sig) {
        res.writeHead(400);
        res.end("Missing auth headers");
        return;
      }

      res.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache",
        "Connection": "keep-alive"
      });
      res.write(": ok\n\n");
      sseResponse = res;

    } else if (url.pathname === "/api/v1/registry/resolve") {
      // Mock Registry Resolve
      console.log("Mock Server: Registry resolving URN:", url.searchParams.get("urn"));
      // Return hardcoded recipient public key (matches our test X25519 PK)
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({
        x25519_pubkey: Buffer.from("c474fe12ad8c71b3f76d4bb2c9994ee30daffff2f9b19d87095b0f983dd8df5a", "hex").toString("base64")
      }));

    } else if (url.pathname === "/api/v1/mq/store") {
      // Handle Message Store
      console.log("Mock Server: Received store message POST request");
      let body = "";
      req.on("data", chunk => { body += chunk; });
      req.on("end", () => {
        try {
          receivedStore = JSON.parse(body);
          
          // Verify Ed25519 signature
          const auth = req.headers["authorization"] || "";
          const parts = auth.split(" ");
          const token = parts[1] || parts[0];
          const tokenParts = token.split(":");
          const sigHex = tokenParts[0];
          const pubkeyHex = tokenParts[1];

          const ed25519SpkiPrefix = Buffer.from("302a300506032b6570032100", "hex");
          const spki = Buffer.concat([ed25519SpkiPrefix, Buffer.from(pubkeyHex, "hex")]);
          const edPubKeyObj = crypto.createPublicKey({
            key: spki,
            format: "der",
            type: "spki"
          });
          storeSignatureValid = crypto.verify(null, Buffer.from(body, "utf8"), edPubKeyObj, Buffer.from(sigHex, "hex"));

          const envelopeB64 = receivedStore.payload_proto;
          const envelopeBuf = Buffer.from(envelopeB64, "base64");
          
          // Decode proto envelope manually
          const decodeVarint = (buf: Buffer, offset: { v: number }) => {
            let res = BigInt(0), shift = BigInt(0);
            while (offset.v < buf.length) {
              const byte = buf[offset.v++];
              res |= BigInt(byte & 0x7f) << shift;
              if ((byte & 0x80) === 0) return res;
              shift += BigInt(7);
            }
            throw new Error("EOF");
          };

          const env: any = {};
          let offset = { v: 0 };
          while (offset.v < envelopeBuf.length) {
            const tag = decodeVarint(envelopeBuf, offset);
            const fieldNum = Number(tag >> BigInt(3));
            const wireType = Number(tag & BigInt(7));
            if (wireType === 2) {
              const len = Number(decodeVarint(envelopeBuf, offset));
              const val = envelopeBuf.subarray(offset.v, offset.v + len);
              offset.v += len;
              if (fieldNum === 1) env.senderUrn = val.toString("utf8");
              else if (fieldNum === 2) env.senderStaticPubkey = val;
              else if (fieldNum === 3) env.ephemeralPubkey = val;
              else if (fieldNum === 4) env.nonce = val;
              else if (fieldNum === 5) env.ciphertext = val;
              else if (fieldNum === 6) env.tag = val;
            }
          }

          // Call helper to decrypt
          const decryptOutput = execFileSync(helperPath, [
            "decrypt-envelope",
            keysDir,
            env.senderStaticPubkey.toString("hex"),
            env.ephemeralPubkey.toString("hex"),
            env.nonce.toString("hex"),
            env.ciphertext.toString("hex"),
            env.tag.toString("hex")
          ]).toString().trim();
          
          const decryptRes = JSON.parse(decryptOutput);
          decryptedStoreText = decryptRes.plaintext;

          res.writeHead(200, { "Content-Type": "application/json" });
          res.end(JSON.stringify({ ok: true, message_id: "msg-stored-ok-123" }));
        } catch (e: any) {
          console.error("Mock Server store parsing error:", e.message);
          res.writeHead(500);
          res.end(e.message);
        }
      });
    } else {
      res.writeHead(404);
      res.end();
    }
  });

  server.listen(9090);
  console.log("Mock server listening on port 9090");

  // 2. Instantiate and start Channel
  const gateway = new EventEmitter();
  let receivedIncomingMessage: any = null;

  gateway.on("message", (msg) => {
    console.log("Gateway callback: Received incoming message:", msg.content);
    receivedIncomingMessage = msg;
  });

  const channel = new AgentCommChannel(gateway, {
    platform_url: "http://localhost:9090/api/v1/mq",
    urn: "urn:hermes:agent:VVDkKJJAExLmCgqhLW26AM",
    keys_dir: keysDir
  });

  await channel.start();
  
  // Wait for SSE connection to establish
  await new Promise(r => setTimeout(r, 1000));

  // 3. Simulate Incoming Message (Platform -> Agent)
  console.log("\nSimulating incoming message from platform...");
  if (!sseResponse) {
    throw new Error("SSE connection was not established!");
  }

  // Encrypt "hello from server" using helper
  const recipientPubkeyHex = "c474fe12ad8c71b3f76d4bb2c9994ee30daffff2f9b19d87095b0f983dd8df5a";
  const plaintextHex = Buffer.from("hello from server", "utf8").toString("hex");
  const encOutput = execFileSync(helperPath, [
    "encrypt-envelope",
    keysDir,
    recipientPubkeyHex,
    plaintextHex
  ]).toString().trim();
  const encRes = JSON.parse(encOutput);

  // Encode to protobuf envelope bytes
  const encodeVarint = (v: number) => {
    const bytes = [];
    let val = v;
    while (val >= 128) {
      bytes.push((val & 0x7f) | 0x80);
      val >>= 7;
    }
    bytes.push(val);
    return Buffer.from(bytes);
  };
  const encodeDelimited = (f: number, d: Buffer) => {
    return Buffer.concat([encodeVarint((f << 3) | 2), encodeVarint(d.length), d]);
  };

  const envBytes = Buffer.concat([
    encodeDelimited(1, Buffer.from(encRes.sender_urn, "utf8")),
    encodeDelimited(2, Buffer.from(encRes.sender_static_pubkey, "hex")),
    encodeDelimited(3, Buffer.from(encRes.ephemeral_pubkey, "hex")),
    encodeDelimited(4, Buffer.from(encRes.nonce, "hex")),
    encodeDelimited(5, Buffer.from(encRes.ciphertext, "hex")),
    encodeDelimited(6, Buffer.from(encRes.tag, "hex")),
    encodeDelimited(7, Buffer.from(encRes.message_id, "utf8"))
  ]);

  // Send SSE event
  const sseData = JSON.stringify({
    message_id: "msg-sse-111",
    payload_proto: envBytes.toString("base64")
  });
  sseResponse.write(`data: ${sseData}\n\n`);

  // Wait for processing
  await new Promise(r => setTimeout(r, 1000));

  if (!receivedIncomingMessage || receivedIncomingMessage.content !== "hello from server") {
    throw new Error("Failed to receive or decrypt incoming SSE message!");
  }
  console.log("Success: Incoming message verified successfully.");

  // 4. Simulate Outgoing Message (Agent -> Platform)
  console.log("\nSimulating outgoing message from agent...");
  await channel.sendMessage("urn:hermes:agent:VVDkKJJAExLmCgqhLW26AM", "hello back");

  if (!receivedStore) {
    throw new Error("Failed to receive store POST on server!");
  }
  if (!storeSignatureValid) {
    throw new Error("HTTP POST signature verification failed!");
  }
  if (decryptedStoreText !== "hello back") {
    throw new Error(`Plaintext mismatch: expected 'hello back', got '${decryptedStoreText}'`);
  }
  console.log("Success: Outgoing message signature and plaintext verified successfully.");

  console.log("\n=== ALL E2E INTEGRATION TESTS PASSED SUCCESSFULLY! ===");
  channel.stop();
  server.close();
  process.exit(0);
}

runIntegrationTest().catch(err => {
  console.error("E2E Integration Test FAILED:", err);
  process.exit(1);
});
