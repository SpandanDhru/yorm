// One WebSocket to a session, kept open: reconnects with backoff and asks
// for a snapshot after every (re)connect.
import type { ServerMsg } from "./types";

export type Status = "connecting" | "open" | "reconnecting" | "gone";

export interface Handlers {
  onMessage(msg: ServerMsg): void;
  onStatus(status: Status): void;
}

const MIN_BACKOFF_MS = 500;
const MAX_BACKOFF_MS = 10_000;
const SESSION_NOT_FOUND = 4404; // ws.StatusSessionNotFound

export function backoff(attempt: number, random = Math.random): number {
  const base = Math.min(MAX_BACKOFF_MS, MIN_BACKOFF_MS * 2 ** attempt);
  return base / 2 + random() * (base / 2); // jitter, so clients don't reconnect in lockstep
}

export class Connection {
  private ws: WebSocket | null = null;
  private attempt = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private stopped = false;

  constructor(
    private readonly sessionId: string,
    private readonly token: string,
    private readonly handlers: Handlers,
  ) {}

  start(): void {
    this.stopped = false;
    this.open();
  }

  stop(): void {
    this.stopped = true;
    clearTimeout(this.timer);
    this.ws?.close(1000);
    this.ws = null;
  }

  sync(): void {
    this.send({ type: "sync" });
  }

  // command sends a command and returns its ID, which the ack or reject
  // echoes, or null if not connected.
  command(name: string, args: unknown): string | null {
    const id = crypto.randomUUID();
    return this.send({ type: "command", id, name, args }) ? id : null;
  }

  private send(msg: unknown): boolean {
    if (this.ws?.readyState !== WebSocket.OPEN) return false;
    this.ws.send(JSON.stringify(msg));
    return true;
  }

  private open(): void {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const url = `${proto}//${location.host}/ws/sessions/${encodeURIComponent(this.sessionId)}?token=${encodeURIComponent(this.token)}`;
    this.handlers.onStatus(this.attempt === 0 ? "connecting" : "reconnecting");
    const ws = new WebSocket(url);
    this.ws = ws;

    ws.onmessage = (e) => {
      const msg = JSON.parse(e.data as string) as ServerMsg;
      if (msg.type === "welcome") {
        this.attempt = 0;
        this.handlers.onStatus("open");
        this.sync();
      }
      this.handlers.onMessage(msg);
    };
    ws.onclose = (e) => {
      if (this.ws !== ws || this.stopped) return;
      this.ws = null;
      if (e.code === SESSION_NOT_FOUND) {
        this.handlers.onStatus("gone");
        return;
      }
      this.handlers.onStatus("reconnecting");
      this.timer = setTimeout(() => this.open(), backoff(this.attempt++));
    };
  }
}
