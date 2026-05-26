// Test peer for the live relay: connects with a secret, prints received clips
// (decrypted), and optionally sends one. Acts as the "other device".
//
// Usage: bun scripts/peer.ts <secretHex> [relay] [sendText] [listenSec]

const secretHex = process.argv[2];
const relay = (process.argv[3] ?? "ws://localhost:3000").replace(/\/$/, "");
const sendText = process.argv[4] ?? "";
const listenSec = Number(process.argv[5] ?? 30);
if (!secretHex) {
  console.error("usage: bun scripts/peer.ts <secretHex> [relay] [sendText] [listenSec]");
  process.exit(2);
}

const enc = new TextEncoder();
const dec = new TextDecoder();
const fromHex = (s: string) => new Uint8Array(Buffer.from(s, "hex"));
const toB64 = (b: Uint8Array) => Buffer.from(b).toString("base64");
const fromB64 = (s: string) => new Uint8Array(Buffer.from(s, "base64"));
const toB64url = (b: Uint8Array) => Buffer.from(b).toString("base64url");
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

const secret = fromHex(secretHex);
const sha256 = async (d: Uint8Array) => new Uint8Array(await crypto.subtle.digest("SHA-256", d));
const sha256hex = async (s: string) => Buffer.from(await sha256(enc.encode(s))).toString("hex");

const roomId = toB64url(await sha256(secret)).slice(0, 32);
const ikm = await crypto.subtle.importKey("raw", secret, "HKDF", false, ["deriveBits"]);
const bits = await crypto.subtle.deriveBits(
  { name: "HKDF", hash: "SHA-256", salt: enc.encode("bgnconnect/enc/v1"), info: enc.encode("aes-256-gcm") },
  ikm, 256,
);
const key = await crypto.subtle.importKey("raw", new Uint8Array(bits), "AES-GCM", false, ["encrypt", "decrypt"]);

async function seal(obj: unknown) {
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const ct = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, enc.encode(JSON.stringify(obj))));
  return { v: 1, alg: "AES-256-GCM", iv: toB64(iv), ct: toB64(ct) };
}
async function open(e: { iv: string; ct: string }) {
  return JSON.parse(dec.decode(await crypto.subtle.decrypt({ name: "AES-GCM", iv: fromB64(e.iv) }, key, fromB64(e.ct))));
}

console.log(`peer → ${relay}  room=${roomId}`);
const ws = new WebSocket(`${relay}/ws?room=${roomId}&v=1`);
await new Promise<void>((res, rej) => { ws.onopen = () => res(); ws.onerror = (e) => rej(e); });
ws.send(JSON.stringify({ t: "hello", dev: "peer", ts: Date.now() }));
console.log("connected");

ws.onmessage = async (ev) => {
  const m = JSON.parse(ev.data as string);
  if (m.t === "peers") console.log(`[peers=${m.count}]`);
  if (m.t === "clip" && m.enc) {
    try {
      const p = await open(m.enc);
      console.log(`RECV from=${p.origin}: ${JSON.stringify(p.text)}`);
    } catch {
      console.log("RECV (decrypt failed — wrong secret?)");
    }
  }
};

if (sendText) {
  await sleep(800);
  const payload = { type: "text", text: sendText, ch: await sha256hex(sendText), origin: "peer", ts: Date.now() };
  ws.send(JSON.stringify({ t: "clip", id: "peer-send", ts: Date.now(), dev: "peer", enc: await seal(payload) }));
  console.log(`SENT: ${JSON.stringify(sendText)}`);
}

await sleep(listenSec * 1000);
ws.close();
console.log("done");
