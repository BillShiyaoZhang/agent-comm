import { test, TestContext } from "node:test";
import { strict as assert } from "node:assert";
import { EventEmitter } from "node:events";
import * as http from "node:http";
import { AddressInfo } from "node:net";
import { AgentCommChannel } from "./channel";

const wait = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));
async function until(condition: () => boolean) {
  const deadline = Date.now() + 5000;
  while (!condition()) {
    if (Date.now() >= deadline) throw new Error("Timed out waiting for helper event");
    await wait(10);
  }
}

async function helper(t: TestContext) {
  const inbox = new Map<string, any>();
  const subscribers = new Set<http.ServerResponse>();
  const stored: any[] = [], acknowledgements: string[] = [], channels: AgentCommChannel[] = [];
  const storePaths: string[] = [];
  let storeReply: any = null;
  let disclosureReply: any = null;
  let rejectAck = false;
  function write(res: http.ServerResponse, message: any) {
    res.write(`id: ${message.message_id}\ndata: ${JSON.stringify(message)}\n\n`);
  }
  const server = http.createServer((req, res) => {
    if (req.url === "/api/v1/mq/subscribe") {
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write('data: {"event":"connected"}\n\n');
      subscribers.add(res);
      for (const message of inbox.values()) write(res, message);
      res.on("close", () => subscribers.delete(res));
      return;
    }
    let body = "";
    req.setEncoding("utf8");
    req.on("data", chunk => { body += chunk; });
    req.on("end", () => {
      const parsed = body ? JSON.parse(body) : {};
      res.setHeader("Content-Type", "application/json");
      if (req.url === "/api/v2/disclosure") {
        if (disclosureReply === null) res.writeHead(404);
        res.end(JSON.stringify(disclosureReply || {}));
      } else if (req.url === "/api/v1/mq/store" || req.url === "/api/v2/mq/store") {
        stored.push(parsed);
        storePaths.push(req.url);
        res.writeHead(202);
        res.end(JSON.stringify(storeReply || { success: true, message_id: parsed.message_id, status: "accepted" }));
      } else if (req.url === "/api/v1/mq/ack") {
        if (rejectAck) {
          res.end(JSON.stringify({ success: false }));
          return;
        }
        for (const id of parsed.message_ids) {
          acknowledgements.push(id);
          inbox.delete(id);
        }
        res.end(JSON.stringify({ success: true }));
      } else { res.writeHead(404); res.end("{}"); }
    });
  });
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  const timer = setInterval(() => {
    for (const res of subscribers) {
      res.write(": heartbeat\n\n");
      for (const message of inbox.values()) write(res, message);
    }
  }, 50);
  t.after(async () => {
    clearInterval(timer);
    for (const channel of channels) channel.stop();
    for (const response of subscribers) response.destroy();
    server.closeAllConnections();
    await new Promise<void>(resolve => server.close(() => resolve()));
  });
  return {
    stored, storePaths, acknowledgements, inbox, subscribers,
    setStoreReply: (reply: any) => { storeReply = reply; },
    setDisclosure: (reply: any) => { disclosureReply = reply; },
    setRejectAck: (reject: boolean) => { rejectAck = reject; },
    channel(gateway = new EventEmitter()) {
      const channel = new AgentCommChannel(gateway, { platform_url: url, urn: "local", keys_dir: "unused" });
      channels.push(channel);
      return channel;
    },
    publish(message: any) {
      inbox.set(message.message_id, message);
      for (const response of subscribers) write(response, message);
    },
  };
}

test("202 send verifies success/id and retains caller retry key/metadata", async (t) => {
  const h = await helper(t), channel = h.channel();
  const metadata = { message_id: "retry-key", conversation_id: "conv", task_id: "task", in_reply_to: "parent", hop_limit: 2 };
  const first = await channel.sendMessage("peer", "hello", metadata);
  assert.equal(first.message_id, "retry-key");
  assert.equal(first.status, "accepted");
  await channel.sendMessage("peer", "hello", metadata);
  assert.deepEqual(h.stored[0], h.stored[1]);
  assert.equal(h.stored[0].conversation_id, "conv");
  h.setStoreReply({ success: false, message_id: "retry-key", status: "accepted" });
  await assert.rejects(channel.sendMessage("peer", "hello", metadata), /did not accept/);
  h.setStoreReply({ success: true, status: "accepted" });
  await assert.rejects(channel.sendMessage("peer", "hello", metadata), /did not accept/);
});

test("signed local disclosure selects v2 and consent stops ordinary sends", async (t) => {
  const h = await helper(t), channel = h.channel();
  h.setDisclosure({ state: "ready", policy_verified: true, v2_send_ready: true, mode: "private" });
  await channel.sendMessage("peer", "v2 body", { message_id: "v2-1" });
  assert.deepEqual(h.storePaths, ["/api/v2/mq/store"]);
  h.setDisclosure({ state: "consent_required", policy_verified: true, v2_send_ready: false,
    legacy_send_code: "consent_required", mode: "compliance", platform_can_decrypt: true });
  await assert.rejects(channel.sendMessage("peer", "blocked", { message_id: "v2-2" }),
    (error: any) => error.code === "consent_required");
  assert.equal(h.storePaths.length, 1);
});

test("SSE repeat emits once and ACK waits for explicit completion callback", async (t) => {
  const h = await helper(t), gateway = new EventEmitter(), events: any[] = [];
  gateway.on("message", message => events.push(message));
  const channel = h.channel(gateway);
  await channel.start();
  h.publish({ message_id: "wire-1", sender_urn: "peer", text: "你好", conversation_id: "conv", task_id: "task" });
  await until(() => events.length === 1);
  await wait(150);
  assert.equal(events.length, 1);
  assert.deepEqual(h.acknowledgements, []);
  assert.equal(events[0].message_id, "wire-1");
  assert.equal(events[0].metadata.task_id, "task");
  assert.equal(events[0].is_bot, true);
  assert.equal(events[0].allow_gateway_control, false);
  h.setRejectAck(true);
  await assert.rejects(events[0].acknowledge(), /rejected ACK/);
  assert.ok(h.inbox.has("wire-1"));
  h.setRejectAck(false);
  await events[0].acknowledge();
  assert.deepEqual(h.acknowledgements, ["wire-1"]);
});

test("no listeners retain pending; a new consumer replays unacknowledged messages", async (t) => {
  const h = await helper(t), gateway = new EventEmitter(), events: any[] = [];
  const first = h.channel(gateway);
  await first.start();
  h.publish({ message_id: "wire-1", sender_urn: "peer", text: "hello" });
  await wait(100);
  assert.deepEqual(h.acknowledgements, []);
  gateway.on("message", message => events.push(message));
  await until(() => events.length === 1);
  first.stop();
  const second = h.channel(gateway);
  await second.start();
  await until(() => events.length === 2);
  assert.deepEqual(h.acknowledgements, []);
  await events[1].acknowledge();
});

test("dropped SSE reconnects without a second emit, stop closes subscribers", async (t) => {
  const h = await helper(t), gateway = new EventEmitter(), events: any[] = [];
  gateway.on("message", message => events.push(message));
  const channel = h.channel(gateway);
  await channel.start();
  h.publish({ message_id: "wire-1", sender_urn: "peer", text: "hello" });
  await until(() => events.length === 1);
  for (const response of h.subscribers) response.end();
  await until(() => h.subscribers.size === 0);
  await until(() => h.subscribers.size === 1);
  await wait(100);
  assert.equal(events.length, 1);
  channel.stop();
  await until(() => h.subscribers.size === 0);
});
