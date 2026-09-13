import * as http from "http";
import { randomUUID } from "crypto";
import { URL } from "url";

export interface ChannelConfig {
  platform_url: string;
  urn: string;
  keys_dir: string;
  enabled?: boolean;
}

export interface MessageMetadata {
  message_id?: string;
  conversation_id?: string;
  in_reply_to?: string;
  task_id?: string;
  kind?: string;
  deadline?: string;
  hop_limit?: number;
}

export interface StoreResult {
  success: true;
  message_id: string;
  status: string;
}

const WIRE_FIELDS = ["conversation_id", "in_reply_to", "task_id", "kind", "deadline", "hop_limit"] as const;

/** Plaintext local-helper bridge. EventEmitter admission alone never acknowledges the inbox. */
export class AgentCommChannel {
  private sseRequest: http.ClientRequest | null = null;
  private sseResponse: http.IncomingMessage | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private isRunning = false;
  private startPromise: Promise<void> | null = null;
  private seen = new Set<string>();

  constructor(private gateway: any, private config: ChannelConfig) {}

  private getApiUrl(endpoint: string): URL {
    const base = new URL(this.config.platform_url.trim());
    if (base.protocol !== "http:" || !["127.0.0.1", "localhost", "[::1]"].includes(base.hostname)) {
      throw new Error("platform_url must address the local helper over loopback HTTP");
    }
    if (base.username || base.password || base.search || base.hash) {
      throw new Error("platform_url cannot contain credentials, query or fragment");
    }
    const path = base.pathname.replace(/\/$/, "");
    if (path && path !== "/api/v1/mq") throw new Error("Unexpected helper API path");
    base.pathname = `/api/v1/mq/${endpoint}`;
    return base;
  }

  public async start(): Promise<void> {
    if (this.startPromise) return this.startPromise;
    if (this.isRunning) return;
    this.getApiUrl("subscribe");
    this.isRunning = true;
    this.startPromise = this.connectSSE();
    try {
      await this.startPromise;
    } catch (error) {
      this.stop();
      throw error;
    } finally {
      this.startPromise = null;
    }
  }

  public stop(): void {
    this.isRunning = false;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = null;
    const request = this.sseRequest;
    this.sseRequest = null;
    this.sseResponse?.destroy();
    this.sseResponse = null;
    request?.destroy();
    // Keep process-local dedup on reconnect. A new process replays unacknowledged messages.
  }

  private connectSSE(): Promise<void> {
    if (!this.isRunning) return Promise.resolve();
    return new Promise<void>((resolve, reject) => {
      let established = false;
      const req = http.get(this.getApiUrl("subscribe"), { headers: { Accept: "text/event-stream" } }, (res) => {
        if (req !== this.sseRequest || !this.isRunning) {
          res.destroy();
          reject(new Error("Channel stopped before SSE connected"));
          return;
        }
        if (res.statusCode !== 200 || !res.headers["content-type"]?.startsWith("text/event-stream")) {
          const error = new Error(`Helper subscribe failed: HTTP ${res.statusCode}`);
          res.destroy();
          reject(error);
          return;
        }
        established = true;
        this.sseResponse = res;
        req.setTimeout(45000, () => req.destroy(new Error("Helper SSE heartbeat timed out")));
        resolve();
        res.setEncoding("utf8");
        let buffer = "", data: string[] = [], eventId: string | undefined;
        res.on("data", (chunk: string) => {
          buffer += chunk;
          const lines = buffer.split("\n");
          buffer = lines.pop() || "";
          for (const raw of lines) {
            const line = raw.replace(/\r$/, "");
            if (!line) {
              if (data.length) void this.handleIncomingEvent(data.join("\n"), eventId);
              data = [];
              eventId = undefined;
            } else if (line.startsWith("data:")) {
              data.push(line.slice(5).replace(/^ /, ""));
            } else if (line.startsWith("id:")) {
              eventId = line.slice(3).replace(/^ /, "");
            }
          }
        });
        res.on("error", () => req.destroy());
        res.on("close", () => this.scheduleReconnect(req));
      });
      this.sseRequest = req;
      req.setTimeout(10000, () => req.destroy(new Error("Helper connection timed out")));
      req.on("error", (error) => {
        if (!established) reject(error);
        else this.scheduleReconnect(req);
      });
      req.on("close", () => {
        if (!established) reject(new Error("Helper connection closed before SSE connected"));
      });
    });
  }

  private scheduleReconnect(request: http.ClientRequest): void {
    if (!this.isRunning || request !== this.sseRequest || this.reconnectTimer) return;
    this.sseRequest = null;
    this.sseResponse?.destroy();
    this.sseResponse = null;
    request.destroy();
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      void this.connectSSE().catch((error: Error) => {
        console.error(`[agent-comm-channel] Reconnect failed: ${error.message}`);
        if (this.sseRequest) this.scheduleReconnect(this.sseRequest);
      });
    }, 1000);
  }

  private async handleIncomingEvent(data: string, eventId?: string): Promise<void> {
    try {
      const message = JSON.parse(data);
      if (message.event === "connected") return;
      const id = message.message_id;
      if (typeof id !== "string" || !/^[A-Za-z0-9._:-]{1,128}$/.test(id)
          || (eventId && eventId !== id) || typeof message.sender_urn !== "string"
          || !message.sender_urn || typeof message.text !== "string" || !message.text) {
        throw new Error("Invalid helper message; left unacknowledged");
      }
      if (this.seen.has(id)) return;
      const metadata: MessageMetadata = {};
      for (const key of WIRE_FIELDS) {
        if (message[key] !== undefined) (metadata as any)[key] = message[key];
      }
      this.seen.add(id);
      try {
        const admitted = this.gateway.emit("message", {
          channel: "agent-comm", sender: message.sender_urn, content: message.text,
          message_id: id, metadata, is_bot: true, allow_gateway_control: false,
          // The host calls this only after durable admission or successful processing.
          acknowledge: () => this.acknowledge(id),
        });
        if (admitted === false) this.seen.delete(id); // EventEmitter had no listeners.
      } catch (error) {
        this.seen.delete(id);
        throw error;
      }
    } catch (error: any) {
      console.error(`[agent-comm-channel] Incoming event error: ${error.message}`);
    }
  }

  /** Explicit completion boundary for hosts; an EventEmitter return value is not durable acceptance. */
  public async acknowledge(messageId: string): Promise<void> {
    if (!this.seen.has(messageId)) throw new Error("Cannot acknowledge a message not admitted by this channel");
    const result = await this.requestJson("ack", { message_ids: [messageId] });
    if (result.success !== true) throw new Error(`Helper rejected ACK for ${messageId}`);
  }

  private requestJson(endpoint: string, body: unknown): Promise<any> {
    const bytes = Buffer.from(JSON.stringify(body), "utf8");
    return new Promise((resolve, reject) => {
      const req = http.request(this.getApiUrl(endpoint), {
        method: "POST", headers: { "Content-Type": "application/json", "Content-Length": bytes.length },
      }, (res) => {
        res.setEncoding("utf8");
        let text = "";
        res.on("data", (chunk: string) => { text += chunk; });
        res.on("error", reject);
        res.on("end", () => {
          try {
            const result = JSON.parse(text);
            if (!res.statusCode || res.statusCode < 200 || res.statusCode >= 300) {
              throw new Error(`Helper ${endpoint}: HTTP ${res.statusCode}: ${text}`);
            }
            if (!result || typeof result !== "object") throw new Error("Expected helper JSON object");
            resolve(result);
          } catch (error) { reject(error); }
        });
      });
      req.setTimeout(15000, () => req.destroy(Object.assign(new Error("Helper request timed out"), { code: "ETIMEDOUT" })));
      req.on("error", reject);
      req.end(bytes);
    });
  }

  /** accepted means durable helper outbox admission, not peer delivery or task completion. */
  public async sendMessage(recipientUrn: string, text: string, metadata: MessageMetadata = {}): Promise<StoreResult> {
    const id = metadata.message_id || randomUUID();
    const body: Record<string, unknown> = { recipient_urn: recipientUrn, text, message_id: id };
    for (const key of WIRE_FIELDS) {
      if (metadata[key] !== undefined) body[key] = metadata[key];
    }
    let result: any;
    for (let attempt = 0; attempt < 3; attempt++) {
      try {
        result = await this.requestJson("store", body);
        break;
      } catch (error: any) {
        if (attempt === 2 || !["ECONNRESET", "ECONNREFUSED", "ETIMEDOUT", "EPIPE"].includes(error.code)) throw error;
        await new Promise(resolve => setTimeout(resolve, 250 * 2 ** attempt));
      }
    }
    if (result.success !== true || result.message_id !== id || typeof result.status !== "string") {
      throw new Error(`Helper did not accept message ${id}: ${JSON.stringify(result)}`);
    }
    return result as StoreResult;
  }
}
