// Quick relay check: two WS clients in one room; A sends, B must receive.
// Proves the TLS/proxy chain + backend pub/sub routing of your relay.
// Usage: bun scripts/relay-check.ts [wss://relay.yourdomain.com]   (defaults to localhost)

const relay = (process.argv[2] ?? "ws://localhost:3000").replace(/\/$/, "");
const room = "Yw3NKWbEM2aRElRIu7JbT_QSpJxzLbLI"; // test-vector roomId
const url = `${relay}/ws?room=${room}&v=1`;
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function open(): Promise<WebSocket> {
  return new Promise((res, rej) => {
    const ws = new WebSocket(url);
    ws.onopen = () => res(ws);
    ws.onerror = (e) => rej(e);
  });
}

const a = await open();
const b = await open();

const received: string[] = [];
b.onmessage = (ev) => {
  const m = JSON.parse(ev.data as string);
  if (m.t === "clip" && m.enc?.ct) received.push(Buffer.from(m.enc.ct, "base64").toString());
};

await sleep(400);
const tok = "RELAY_CHECK_" + Math.random().toString(36).slice(2, 8);
a.send(JSON.stringify({
  t: "clip", id: "rc", ts: Date.now(), dev: "A",
  enc: { v: 1, alg: "AES-256-GCM", iv: "AQIDBAUGBwgJCgsM", ct: Buffer.from(tok).toString("base64") },
}));

for (let i = 0; i < 50 && !received.includes(tok); i++) await sleep(100);
const ok = received.includes(tok);
console.log(ok ? `RELAY OK — ${relay} relayed the message (${tok})` : `RELAY FAIL — ${relay}`);
a.close();
b.close();
process.exit(ok ? 0 : 1);
