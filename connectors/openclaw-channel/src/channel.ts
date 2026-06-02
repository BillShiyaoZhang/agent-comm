import { execFile } from "child_process";
import * as path from "path";
import * as os from "os";
import * as fs from "fs";
import * as http from "http";
import * as https from "https";
import { URL } from "url";

export interface ChannelConfig {
  platform_url: string;
  urn: string;
  keys_dir: string;
  enabled?: boolean;
}

export class AgentCommChannel {
  private gateway: any;
  private config: ChannelConfig;
  private sseRequest: http.ClientRequest | null = null;
  private isRunning: boolean = false;

  constructor(gateway: any, config: ChannelConfig) {
    this.gateway = gateway;
    this.config = config;
  }

  // Resolves the full path of the keys directory (expanding ~)
  private getKeysDir(): string {
    let dir = this.config.keys_dir;
    if (dir.startsWith("~")) {
      dir = path.join(os.homedir(), dir.substring(1));
    }
    return path.resolve(dir);
  }

  // Resolves the Go helper path
  private getHelperPath(): string {
    const home = os.homedir();
    const binaryName = os.platform() === "win32" ? "agent-comm-helper.exe" : "agent-comm-helper";
    return process.env.AGENT_COMM_HELPER_PATH || path.join(home, ".agent-comm", "bin", binaryName);
  }

  // Invokes the Go helper subprocess
  private invokeHelper(args: string[]): Promise<any> {
    return new Promise((resolve, reject) => {
      const helperPath = this.getHelperPath();
      execFile(helperPath, args, (err, stdout, stderr) => {
        if (err) {
          return reject(new Error(`Helper execution failed: ${err.message}. Stderr: ${stderr}`));
        }
        try {
          const parsed = JSON.parse(stdout.trim());
          if (parsed.error) {
            return reject(new Error(`Helper error: ${parsed.error}`));
          }
          resolve(parsed);
        } catch (parseErr) {
          reject(new Error(`Failed to parse helper output: ${stdout}. Err: ${parseErr}`));
        }
      });
    });
  }

  // Resolves API URLs
  private getApiUrl(endpoint: string): string {
    let baseUrl = this.config.platform_url.trim();
    if (baseUrl.endsWith("/")) {
      baseUrl = baseUrl.slice(0, -1);
    }
    if (!baseUrl.includes("/api/v1/mq")) {
      baseUrl += "/api/v1/mq";
    }
    return `${baseUrl}/${endpoint}`;
  }

  // Starts the channel receiver
  public async start(): Promise<void> {
    if (this.isRunning) return;
    this.isRunning = true;
    this.connectSSE();
  }

  // Stops the channel receiver
  public stop(): void {
    this.isRunning = false;
    if (this.sseRequest) {
      this.sseRequest.destroy();
      this.sseRequest = null;
    }
  }

  // Establishes a persistent SSE connection with authentication headers
  private async connectSSE(): Promise<void> {
    if (!this.isRunning) return;

    try {
      const keysDir = this.getKeysDir();
      const urn = this.config.urn;
      const timestamp = Math.floor(Date.now() / 1000);

      // Call Go helper to sign the retrieve request
      const signRes = await this.invokeHelper([
        "sign-retrieve",
        keysDir,
        urn,
        String(timestamp)
      ]);

      const headers = {
        "X-URN": urn,
        "X-Timestamp": String(timestamp),
        "X-Pubkey": signRes.pubkey,
        "X-Signature": signRes.signature
      };

      const sseUrl = this.getApiUrl("subscribe");
      const parsedUrl = new URL(sseUrl);
      const client = parsedUrl.protocol === "https:" ? https : http;

      const options = {
        hostname: parsedUrl.hostname,
        port: parsedUrl.port || (parsedUrl.protocol === "https:" ? 443 : 80),
        path: parsedUrl.pathname + parsedUrl.search,
        method: "GET",
        headers: {
          ...headers,
          "Accept": "text/event-stream",
          "Cache-Control": "no-cache",
          "Connection": "keep-alive"
        }
      };

      this.sseRequest = client.request(options, (res) => {
        if (res.statusCode !== 200) {
          console.error(`[agent-comm-channel] SSE returned status ${res.statusCode}`);
          this.reconnect();
          return;
        }

        let buffer = "";
        res.on("data", (chunk) => {
          buffer += chunk.toString("utf8");
          const lines = buffer.split("\n");
          buffer = lines.pop() || "";

          for (const line of lines) {
            const trimmed = line.trim();
            if (trimmed.startsWith("data: ")) {
              this.handleIncomingEvent(trimmed.slice(6));
            }
          }
        });

        res.on("end", () => {
          console.log("[agent-comm-channel] SSE stream ended by server");
          this.reconnect();
        });
      });

      this.sseRequest.on("error", (err) => {
        console.error(`[agent-comm-channel] SSE error: ${err.message}`);
        this.reconnect();
      });

      this.sseRequest.end();

    } catch (err: any) {
      console.error(`[agent-comm-channel] Failed to connect SSE: ${err.message}`);
      this.reconnect();
    }
  }

  // Scheduled reconnect in case of failures
  private reconnect(): void {
    if (!this.isRunning) return;
    this.sseRequest = null;
    setTimeout(() => this.connectSSE(), 5000);
  }

  // Processes raw SSE event, calls Go helper to decrypt, and forwards to gateway
  private async handleIncomingEvent(dataStr: string): Promise<void> {
    try {
      const parsed = JSON.parse(dataStr);
      const payloadProtoBase64 = parsed.payload_proto;
      const messageId = parsed.message_id;

      if (!payloadProtoBase64) return;

      const envelopeBuf = Buffer.from(payloadProtoBase64, "base64");
      
      // We need to parse fields of EncryptedEnvelope to pass them to Go helper.
      // We can use a simple protobuf decoder matching envelope.proto.
      const envelope = this.decodeEnvelopeProto(envelopeBuf);

      // Decrypt using Go helper
      const keysDir = this.getKeysDir();
      const decryptRes = await this.invokeHelper([
        "decrypt-envelope",
        keysDir,
        envelope.senderStaticPubkey.toString("hex"),
        envelope.ephemeralPubkey.toString("hex"),
        envelope.nonce.toString("hex"),
        envelope.ciphertext.toString("hex"),
        envelope.tag.toString("hex")
      ]);

      // Emit incoming message to the OpenClaw gateway
      this.gateway.emit("message", {
        channel: "agent-comm",
        sender: envelope.senderUrn,
        content: decryptRes.plaintext,
        rawEnvelope: envelope
      });

      // Acknowledge the message to platform MQ so it gets deleted
      await this.ackMessage(messageId);

    } catch (err: any) {
      console.error(`[agent-comm-channel] Error handling incoming event: ${err.message}`);
    }
  }

  // Sends HTTP POST ack request to delete processed messages
  private async ackMessage(messageId: string): Promise<void> {
    try {
      const ackUrl = this.getApiUrl("ack");
      const parsedUrl = new URL(ackUrl);
      const client = parsedUrl.protocol === "https:" ? https : http;

      const body = JSON.stringify({ message_ids: [messageId] });

      const options = {
        hostname: parsedUrl.hostname,
        port: parsedUrl.port || (parsedUrl.protocol === "https:" ? 443 : 80),
        path: parsedUrl.pathname,
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Content-Length": Buffer.byteLength(body)
        }
      };

      const req = client.request(options, (res) => {
        res.on("data", () => {});
      });
      req.on("error", () => {});
      req.write(body);
      req.end();
    } catch (err: any) {
      console.error(`[agent-comm-channel] Ack request failed: ${err.message}`);
    }
  }

  // Standard OpenClaw sendMessage outbound handler
  public async sendMessage(recipientUrn: string, text: string): Promise<void> {
    try {
      const keysDir = this.getKeysDir();

      // 1. Resolve recipient's static X25519 public key via Platform Registry
      const recipientPubKeyHex = await this.resolveRegistryX25519(recipientUrn);

      // 2. Encrypt message using Go helper
      const textHex = Buffer.from(text, "utf8").toString("hex");
      const encRes = await this.invokeHelper([
        "encrypt-envelope",
        keysDir,
        recipientPubKeyHex,
        textHex
      ]);

      // 3. Construct EncryptedEnvelope protobuf manually matching envelope.proto
      const envelope = {
        senderUrn: encRes.sender_urn,
        senderStaticPubkey: Buffer.from(encRes.sender_static_pubkey, "hex"),
        ephemeralPubkey: Buffer.from(encRes.ephemeral_pubkey, "hex"),
        nonce: Buffer.from(encRes.nonce, "hex"),
        ciphertext: Buffer.from(encRes.ciphertext, "hex"),
        tag: Buffer.from(encRes.tag, "hex"),
        messageId: encRes.message_id
      };

      const envelopeBytes = this.encodeEnvelopeProto(envelope);

      // 4. Build HTTP POST MQ store request body
      const storeReqObj = {
        recipient_urn: recipientUrn,
        expiry_unix: Math.floor(Date.now() / 1000) + 7 * 24 * 3600, // 7 days TTL
        payload_proto: envelopeBytes.toString("base64")
      };

      const bodyStr = JSON.stringify(storeReqObj);
      const bodyBytes = Buffer.from(bodyStr, "utf8");

      // 5. Sign the HTTP body using Go helper (Ed25519 signature)
      const signRes = await this.invokeHelper([
        "sign-store",
        keysDir,
        bodyBytes.toString("hex")
      ]);

      // 6. Deliver to platform POST /api/v1/mq/store
      const storeUrl = this.getApiUrl("store");
      const parsedUrl = new URL(storeUrl);
      const client = parsedUrl.protocol === "https:" ? https : http;

      const options = {
        hostname: parsedUrl.hostname,
        port: parsedUrl.port || (parsedUrl.protocol === "https:" ? 443 : 80),
        path: parsedUrl.pathname,
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Content-Length": bodyBytes.length,
          "Authorization": `Ed25519 ${signRes.signature}:${signRes.pubkey}`
        }
      };

      await new Promise<void>((resolve, reject) => {
        const req = client.request(options, (res) => {
          let data = "";
          res.on("data", (chunk) => { data += chunk.toString(); });
          res.on("end", () => {
            if (res.statusCode === 200) {
              resolve();
            } else {
              reject(new Error(`Store request failed with status ${res.statusCode}: ${data}`));
            }
          });
        });

        req.on("error", (err) => { reject(err); });
        req.write(bodyBytes);
        req.end();
      });

    } catch (err: any) {
      console.error(`[agent-comm-channel] Failed to send message: ${err.message}`);
      throw err;
    }
  }

  // Queries Platform Registry to resolve URN to X25519 key
  private async resolveRegistryX25519(urn: string): Promise<string> {
    let baseUrl = this.config.platform_url.trim();
    if (baseUrl.endsWith("/")) {
      baseUrl = baseUrl.slice(0, -1);
    }
    // Registry resolution endpoint: GET /api/v1/registry/resolve?urn=...
    const registryUrl = baseUrl.includes("/api/v1/mq")
      ? baseUrl.replace("/api/v1/mq", "/api/v1/registry/resolve")
      : `${baseUrl}/api/v1/registry/resolve`;

    const fullUrl = `${registryUrl}?urn=${encodeURIComponent(urn)}`;
    const parsedUrl = new URL(fullUrl);
    const client = parsedUrl.protocol === "https:" ? https : http;

    const resObj = await new Promise<any>((resolve, reject) => {
      client.get(parsedUrl, (res) => {
        let data = "";
        res.on("data", (chunk) => { data += chunk; });
        res.on("end", () => {
          if (res.statusCode === 200) {
            try {
              resolve(JSON.parse(data));
            } catch (e) {
              reject(e);
            }
          } else {
            reject(new Error(`Registry resolve returned code ${res.statusCode}: ${data}`));
          }
        });
      }).on("error", (err) => { reject(err); });
    });

    if (!resObj.x25519_pubkey) {
      throw new Error(`Registry did not return x25519_pubkey for URN ${urn}`);
    }

    return Buffer.from(resObj.x25519_pubkey, "base64").toString("hex");
  }

  // --- Protobuf Manual Encoding/Decoding (matching envelope.proto) ---

  private decodeEnvelopeProto(buffer: Buffer): {
    senderUrn: string;
    senderStaticPubkey: Buffer;
    ephemeralPubkey: Buffer;
    nonce: Buffer;
    ciphertext: Buffer;
    tag: Buffer;
    messageId: string;
  } {
    const env: any = {};
    let offset = 0;

    const readVarint = (): bigint => {
      let result = BigInt(0);
      let shift = BigInt(0);
      while (offset < buffer.length) {
        const byte = buffer[offset++];
        result |= BigInt(byte & 0x7f) << shift;
        if ((byte & 0x80) === 0) {
          return result;
        }
        shift += BigInt(7);
      }
      throw new Error("Varint overflow or EOF");
    };

    while (offset < buffer.length) {
      const tag = readVarint();
      const fieldNum = Number(tag >> BigInt(3));
      const wireType = Number(tag & BigInt(7));

      if (wireType === 2) {
        const len = Number(readVarint());
        const data = buffer.subarray(offset, offset + len);
        offset += len;

        switch (fieldNum) {
          case 1: env.senderUrn = data.toString("utf8"); break;
          case 2: env.senderStaticPubkey = Buffer.from(data); break;
          case 3: env.ephemeralPubkey = Buffer.from(data); break;
          case 4: env.nonce = Buffer.from(data); break;
          case 5: env.ciphertext = Buffer.from(data); break;
          case 6: env.tag = Buffer.from(data); break;
          case 7: env.messageId = data.toString("utf8"); break;
        }
      } else {
        if (wireType === 0) {
          readVarint();
        } else if (wireType === 5) {
          offset += 4;
        } else if (wireType === 1) {
          offset += 8;
        } else {
          throw new Error(`Unsupported wire type: ${wireType}`);
        }
      }
    }

    return env;
  }

  private encodeEnvelopeProto(env: {
    senderUrn: string;
    senderStaticPubkey: Buffer;
    ephemeralPubkey: Buffer;
    nonce: Buffer;
    ciphertext: Buffer;
    tag: Buffer;
    messageId: string;
  }): Buffer {
    const encodeVarint = (value: number | bigint): Buffer => {
      const bytes: number[] = [];
      let v = BigInt(value);
      while (v >= BigInt(128)) {
        bytes.push(Number((v & BigInt(0x7f)) | BigInt(0x80)));
        v >>= BigInt(7);
      }
      bytes.push(Number(v));
      return Buffer.from(bytes);
    };

    const encodeLengthDelimited = (fieldNumber: number, data: Buffer): Buffer => {
      const tag = (fieldNumber << 3) | 2;
      return Buffer.concat([
        encodeVarint(tag),
        encodeVarint(data.length),
        data
      ]);
    };

    return Buffer.concat([
      encodeLengthDelimited(1, Buffer.from(env.senderUrn, "utf8")),
      encodeLengthDelimited(2, env.senderStaticPubkey),
      encodeLengthDelimited(3, env.ephemeralPubkey),
      encodeLengthDelimited(4, env.nonce),
      encodeLengthDelimited(5, env.ciphertext),
      encodeLengthDelimited(6, env.tag),
      encodeLengthDelimited(7, Buffer.from(env.messageId, "utf8"))
    ]);
  }
}
