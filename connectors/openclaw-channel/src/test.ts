import { test } from "node:test";
import { strict as assert } from "node:assert";
import { EventEmitter } from "node:events";
import { AgentCommChannel } from "./channel";

test("local plaintext API rejects cloud URLs before starting", async () => {
  const channel = new AgentCommChannel(new EventEmitter(), {
    platform_url: "https://agent-communication.online", urn: "local", keys_dir: "unused",
  });
  await assert.rejects(channel.start(), /loopback HTTP/);
});

test("local ACK requires an event admitted by this channel", async () => {
  const channel = new AgentCommChannel(new EventEmitter(), {
    platform_url: "http://127.0.0.1:1", urn: "local", keys_dir: "unused",
  });
  await assert.rejects(channel.acknowledge("foreign-id"), /not admitted/);
});
