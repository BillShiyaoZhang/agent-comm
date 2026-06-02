import * as http from "http";
import * as https from "https";
import { URL } from "url";

export interface ChannelConfig {
  platform_url: string; // 本地 Daemon 监听地址，如 http://127.0.0.1:45042
  urn: string;          // 智能体的 URN 标识
  keys_dir: string;     // 本地密钥存放目录
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

  // Resolves API URLs compatible with localhost daemon endpoints
  private getApiUrl(endpoint: string): string {
    let baseUrl = this.config.platform_url.trim();
    if (baseUrl.endsWith("/")) {
      baseUrl = baseUrl.slice(0, -1);
    }
    // Auto-append mq namespace if not present (supports both http://127.0.0.1:45042 and http://127.0.0.1:45042/api/v1/mq)
    if (!baseUrl.includes("/api/v1/mq")) {
      baseUrl += "/api/v1/mq";
    }
    return `${baseUrl}/${endpoint}`;
  }

  // Starts the channel receiver
  public async start(): Promise<void> {
    if (this.isRunning) return;
    this.isRunning = true;
    console.log(`[agent-comm-channel] Starting subscriber connection to local daemon: ${this.config.platform_url}`);
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

  // Establishes a persistent SSE connection to the local daemon
  private async connectSSE(): Promise<void> {
    if (!this.isRunning) return;

    try {
      const sseUrl = this.getApiUrl("subscribe");
      const parsedUrl = new URL(sseUrl);
      const client = parsedUrl.protocol === "https:" ? https : http;

      const options = {
        hostname: parsedUrl.hostname,
        port: parsedUrl.port || (parsedUrl.protocol === "https:" ? 443 : 80),
        path: parsedUrl.pathname + parsedUrl.search,
        method: "GET",
        headers: {
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

  // Processes raw SSE event (already decrypted plaintext JSON from Go Daemon)
  private async handleIncomingEvent(dataStr: string): Promise<void> {
    try {
      const parsed = JSON.parse(dataStr);
      if (parsed.event === "connected") {
        return; // Connection setup payload
      }

      const senderURN = parsed.sender_urn;
      const text = parsed.text;

      if (!senderURN || !text) return;

      console.log(`[agent-comm-channel] Received incoming plaintext message from ${senderURN}`);

      // Emit incoming message to the OpenClaw gateway
      this.gateway.emit("message", {
        channel: "agent-comm",
        sender: senderURN,
        content: text
      });

    } catch (err: any) {
      console.error(`[agent-comm-channel] Error handling incoming event: ${err.message}`);
    }
  }

  // Standard OpenClaw sendMessage outbound handler, POST plaintext directly to local daemon
  public async sendMessage(recipientUrn: string, text: string): Promise<void> {
    try {
      const storeUrl = this.getApiUrl("store");
      const parsedUrl = new URL(storeUrl);
      const client = parsedUrl.protocol === "https:" ? https : http;

      const bodyObj = {
        recipient_urn: recipientUrn,
        text: text
      };

      const bodyBytes = Buffer.from(JSON.stringify(bodyObj), "utf8");

      const options = {
        hostname: parsedUrl.hostname,
        port: parsedUrl.port || (parsedUrl.protocol === "https:" ? 443 : 80),
        path: parsedUrl.pathname + parsedUrl.search,
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Content-Length": bodyBytes.length
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

      console.log(`[agent-comm-channel] Message successfully sent to local daemon for delivery to ${recipientUrn}`);

    } catch (err: any) {
      console.error(`[agent-comm-channel] Failed to send message: ${err.message}`);
      throw err;
    }
  }
}
