import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Connection } from "./socket";

// FakeSocket stands in for the browser's WebSocket.
class FakeSocket {
  static OPEN = 1;
  static all: FakeSocket[] = [];
  readyState = 0;
  sent: Record<string, unknown>[] = [];
  onmessage: ((e: { data: string }) => void) | null = null;
  onclose: ((e: { code: number }) => void) | null = null;
  constructor(public url: string) {
    FakeSocket.all.push(this);
  }
  send(data: string) {
    this.sent.push(JSON.parse(data));
  }
  close() {}
  open() {
    this.readyState = FakeSocket.OPEN;
    this.onmessage?.({ data: JSON.stringify({ type: "welcome" }) });
  }
  receive(msg: unknown) {
    this.onmessage?.({ data: JSON.stringify(msg) });
  }
  drop() {
    this.readyState = 3;
    this.onclose?.({ code: 1006 });
  }
}

beforeEach(() => {
  FakeSocket.all = [];
  vi.useFakeTimers();
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("location", { protocol: "http:", host: "table.test" });
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("Connection", () => {
  it("syncs from the last seq and resends unanswered commands after a reconnect", () => {
    let seq = 0;
    const conn = new Connection("ses_1", "tok", { onMessage() {}, onStatus() {}, lastSeq: () => seq });
    conn.start();
    const first = FakeSocket.all[0]!;
    first.open();
    expect(first.sent).toEqual([{ type: "sync", last_seq: 0 }]);

    const answered = conn.command("roll_dice", { text: "1d20" })!;
    const lost = conn.command("move_token", { token: "t", to: { x: 1, y: 1 } })!;
    first.receive({ type: "ack", id: answered, seq: 8 });
    seq = 8;

    first.drop();
    vi.runAllTimers(); // reconnect after the backoff
    const second = FakeSocket.all[1]!;
    second.open();
    expect(second.sent).toEqual([
      { type: "sync", last_seq: 8 },
      { type: "command", id: lost, name: "move_token", args: { token: "t", to: { x: 1, y: 1 } } },
    ]);

    // Once answered, it isn't sent again.
    second.receive({ type: "reject", id: lost, code: "not_your_turn", message: "" });
    second.drop();
    vi.runAllTimers();
    FakeSocket.all[2]!.open();
    expect(FakeSocket.all[2]!.sent).toEqual([{ type: "sync", last_seq: 8 }]);
    conn.stop();
  });

  it("does not queue commands while disconnected", () => {
    const conn = new Connection("ses_1", "tok", { onMessage() {}, onStatus() {}, lastSeq: () => 0 });
    conn.start();
    expect(conn.command("roll_dice", { text: "1d20" })).toBeNull();
    conn.stop();
  });
});
