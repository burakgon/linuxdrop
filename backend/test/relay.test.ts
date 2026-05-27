import { test, expect, beforeAll, afterAll } from "bun:test";
import type { Server } from "bun";
import { createServer } from "../src/server.ts";

const PORT = 38917;
const ROOM = "Yw3NKWbEM2aRElRIu7JbT_QSpJxzLbLI"; // 32-char base64url (from test vectors)
const WS_URL = `ws://localhost:${PORT}/ws?room=${ROOM}&v=1`;
const HTTP = `http://localhost:${PORT}`;

let server: Server;
beforeAll(() => {
  server = createServer({ port: PORT, dbPath: ":memory:" });
});
afterAll(() => {
  server.stop(true);
});

function open(url = WS_URL): Promise<WebSocket> {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(url);
    ws.onopen = () => resolve(ws);
    ws.onerror = (e) => reject(e);
  });
}

function nextMessage(
  ws: WebSocket,
  pred: (m: any) => boolean,
  timeoutMs = 2000,
): Promise<any> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("timeout waiting for message")), timeoutMs);
    const onMsg = (ev: MessageEvent) => {
      const m = JSON.parse(ev.data as string);
      if (pred(m)) {
        clearTimeout(timer);
        ws.removeEventListener("message", onMsg as EventListener);
        resolve(m);
      }
    };
    ws.addEventListener("message", onMsg as EventListener);
  });
}

const clipFrame = (id: string, dev: string) =>
  JSON.stringify({
    t: "clip",
    id,
    ts: Date.now(),
    dev,
    enc: { v: 1, alg: "AES-256-GCM", iv: "AQIDBAUGBwgJCgsM", ct: "ZHVtbXk=" },
  });

test("relays clip from A to B but not back to sender A", async () => {
  const a = await open();
  const b = await open();

  let aGotClip = false;
  a.addEventListener("message", (ev: MessageEvent) => {
    if (JSON.parse(ev.data as string).t === "clip") aGotClip = true;
  });

  const bGetsClip = nextMessage(b, (m) => m.t === "clip");
  a.send(clipFrame("01ABC", "a"));

  const got = await bGetsClip;
  expect(got.dev).toBe("a");
  expect(got.id).toBe("01ABC");

  await Bun.sleep(150);
  expect(aGotClip).toBe(false); // sender must not receive its own clip

  a.close();
  b.close();
});

test("ping yields pong with matching ref", async () => {
  const a = await open();
  const pong = nextMessage(a, (m) => m.t === "pong");
  a.send(JSON.stringify({ t: "ping", id: "p1" }));
  const m = await pong;
  expect(m.ref).toBe("p1");
  a.close();
});

test("late joiner receives last clip (catch-up)", async () => {
  const a = await open();
  a.send(clipFrame("01XYZ", "a"));
  await Bun.sleep(150); // let server persist last_clip

  const c = await open();
  const got = await nextMessage(c, (m) => m.t === "clip");
  expect(got.id).toBe("01XYZ");

  a.close();
  c.close();
});

test("peers count is broadcast as devices join", async () => {
  const a = await open();
  // joining a second device should produce a peers message with count >= 2
  const peers = nextMessage(a, (m) => m.t === "peers" && m.count >= 2);
  const b = await open();
  const m = await peers;
  expect(m.count).toBeGreaterThanOrEqual(2);
  a.close();
  b.close();
});

test("blob round-trips for the same room", async () => {
  const bytes = new Uint8Array([1, 2, 3, 4, 5, 6, 7, 8, 9, 0]);
  const put = await fetch(`${HTTP}/blob?room=${ROOM}`, { method: "PUT", body: bytes });
  expect(put.status).toBe(200);
  const { id } = (await put.json()) as { id: string };
  expect(id).toMatch(/^[a-f0-9]{32}$/);

  const get = await fetch(`${HTTP}/blob/${id}?room=${ROOM}`);
  expect(get.status).toBe(200);
  const got = new Uint8Array(await get.arrayBuffer());
  expect(Array.from(got)).toEqual(Array.from(bytes));
});

test("blob is not readable from a different room", async () => {
  const put = await fetch(`${HTTP}/blob?room=${ROOM}`, { method: "PUT", body: new Uint8Array([9, 9, 9]) });
  const { id } = (await put.json()) as { id: string };
  const otherRoom = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"; // valid shape, different room
  const get = await fetch(`${HTTP}/blob/${id}?room=${otherRoom}`);
  expect(get.status).toBe(404);
});

test("blob rejects an invalid room", async () => {
  const r = await fetch(`${HTTP}/blob?room=bad`, { method: "PUT", body: new Uint8Array([1]) });
  expect(r.status).toBe(400);
});

const hello = (dev: string) => JSON.stringify({ t: "hello", dev, ts: Date.now() });

test("signal is delivered only to the addressed peer", async () => {
  const a = await open();
  const b = await open();
  const c = await open();
  a.send(hello("devA"));
  b.send(hello("devB"));
  c.send(hello("devC"));
  await Bun.sleep(150); // let hellos register dev ids

  let cGotSignal = false;
  c.addEventListener("message", (ev) => {
    if (JSON.parse(ev.data as string).t === "signal") cGotSignal = true;
  });
  const bGetsSignal = nextMessage(b, (m) => m.t === "signal");

  a.send(JSON.stringify({ t: "signal", id: "s1", ts: Date.now(), dev: "devA", to: "devB", enc: { iv: "x", ct: "y" } }));

  const got = await bGetsSignal;
  expect(got.dev).toBe("devA");
  await Bun.sleep(120);
  expect(cGotSignal).toBe(false); // directed, not broadcast

  a.close();
  b.close();
  c.close();
});

test("/ice returns STUN ice servers", async () => {
  const r = await fetch(`${HTTP}/ice`);
  expect(r.status).toBe(200);
  const j = (await r.json()) as { iceServers: Array<{ urls: string | string[] }> };
  expect(Array.isArray(j.iceServers)).toBe(true);
  expect(j.iceServers.length).toBeGreaterThanOrEqual(1);
  expect(JSON.stringify(j.iceServers)).toContain("stun:");
});
