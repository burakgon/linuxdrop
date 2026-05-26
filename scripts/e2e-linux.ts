// Real end-to-end smoke test for the Linux path:
//   Bun backend  +  Go daemon (real wl-clipboard)  +  in-process encrypted peer.
//
// Proves: cross-language crypto interop (WebCrypto peer ↔ Go daemon), the real
// Wayland clipboard read/write, the WS relay round-trip, and loop prevention.
// The user's clipboard is saved up front and ALWAYS restored (try/finally).
//
// Run from repo root:  bun scripts/e2e-linux.ts

const PORT = 39411;
const SECRET_HEX = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f";
const RELAY = `ws://localhost:${PORT}`;

const encoder = new TextEncoder();
const decoder = new TextDecoder();
const fromHex = (s: string) => new Uint8Array(Buffer.from(s, "hex"));
const toB64 = (b: Uint8Array) => Buffer.from(b).toString("base64");
const fromB64 = (s: string) => new Uint8Array(Buffer.from(s, "base64"));
const toB64url = (b: Uint8Array) => Buffer.from(b).toString("base64url");

const secret = fromHex(SECRET_HEX);
const sha256 = async (d: Uint8Array) => new Uint8Array(await crypto.subtle.digest("SHA-256", d));
const sha256hex = async (s: string) => Buffer.from(await sha256(encoder.encode(s))).toString("hex");

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function clipRead(): Promise<string> {
  const p = Bun.spawn(["wl-paste", "-n", "-t", "text/plain"], { stderr: "ignore" });
  const t = await new Response(p.stdout).text();
  await p.exited;
  return t;
}
async function clipWrite(text: string): Promise<void> {
  const p = Bun.spawn(["wl-copy", "-t", "text/plain"], { stdin: Buffer.from(text), stderr: "ignore" });
  await p.exited;
}

let pass = true;
const check = (name: string, ok: boolean) => {
  console.log(`${ok ? "✓" : "✗"} ${name}`);
  if (!ok) pass = false;
};

async function main() {
  const roomId = toB64url(await sha256(secret)).slice(0, 32);
  console.log(`roomId=${roomId}`);

  const ikm = await crypto.subtle.importKey("raw", secret, "HKDF", false, ["deriveBits"]);
  const bits = await crypto.subtle.deriveBits(
    { name: "HKDF", hash: "SHA-256", salt: encoder.encode("bgnconnect/enc/v1"), info: encoder.encode("aes-256-gcm") },
    ikm,
    256,
  );
  const key = await crypto.subtle.importKey("raw", new Uint8Array(bits), "AES-GCM", false, ["encrypt", "decrypt"]);

  const seal = async (obj: unknown) => {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const ct = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, encoder.encode(JSON.stringify(obj))));
    return { v: 1, alg: "AES-256-GCM", iv: toB64(iv), ct: toB64(ct) };
  };
  const open = async (enc: { iv: string; ct: string }) => {
    const pt = await crypto.subtle.decrypt({ name: "AES-GCM", iv: fromB64(enc.iv) }, key, fromB64(enc.ct));
    return JSON.parse(decoder.decode(pt));
  };

  const saved = await clipRead();
  console.log(`(saved clipboard: ${saved.length} chars)`);

  const backend = Bun.spawn(["bun", "src/index.ts"], {
    cwd: import.meta.dir + "/../backend",
    env: { ...process.env, PORT: String(PORT), DB_PATH: ":memory:" },
    stdout: "inherit", stderr: "inherit",
  });
  const daemon = Bun.spawn(
    [import.meta.dir + "/../linux/bin/bgnconnectd", "--no-tray", "--relay", RELAY, "--dev-secret", SECRET_HEX],
    { stdout: "inherit", stderr: "inherit" },
  );
  let ws: WebSocket | undefined;

  try {
    let healthy = false;
    for (let i = 0; i < 50; i++) {
      try { if ((await fetch(`http://localhost:${PORT}/health`)).ok) { healthy = true; break; } } catch {}
      await sleep(100);
    }
    check("backend healthy", healthy);

    const received: string[] = [];
    let peers = 0;
    ws = new WebSocket(`${RELAY}/ws?room=${roomId}&v=1`);
    await new Promise<void>((res, rej) => {
      ws!.onopen = () => res();
      ws!.onerror = (e) => rej(e);
    });
    ws.onmessage = async (ev) => {
      const m = JSON.parse(ev.data as string);
      if (m.t === "peers") peers = m.count;
      if (m.t === "clip" && m.enc) { try { received.push((await open(m.enc)).text); } catch {} }
    };
    ws.send(JSON.stringify({ t: "hello", dev: "peer", ts: Date.now() }));

    for (let i = 0; i < 80 && peers < 2; i++) await sleep(100);
    check("daemon connected to relay (peers >= 2)", peers >= 2);

    // Test 1: inbound peer -> relay -> daemon -> system clipboard
    const tok1 = "PEER_TO_DAEMON_" + Math.random().toString(36).slice(2, 8);
    ws.send(JSON.stringify({
      t: "clip", id: "e1", ts: Date.now(), dev: "peer",
      enc: await seal({ type: "text", text: tok1, ch: await sha256hex(tok1), origin: "peer", ts: Date.now() }),
    }));
    let got1 = "";
    for (let i = 0; i < 50; i++) { got1 = await clipRead(); if (got1 === tok1) break; await sleep(100); }
    check("inbound: system clipboard updated from peer", got1 === tok1);

    // Test 2: outbound system clipboard -> daemon -> relay -> peer
    const tok2 = "DAEMON_TO_PEER_" + Math.random().toString(36).slice(2, 8);
    await clipWrite(tok2);
    for (let i = 0; i < 50 && !received.includes(tok2); i++) await sleep(100);
    check("outbound: peer received local clipboard change", received.includes(tok2));
    check("no echo storm (tok2 received exactly once)", received.filter((x) => x === tok2).length === 1);
  } finally {
    ws?.close();
    daemon.kill();
    backend.kill();
    await sleep(300);
    await clipWrite(saved);
    console.log("(restored clipboard)");
  }
}

main()
  .then(() => { console.log(pass ? "\nE2E PASS ✅" : "\nE2E FAIL ❌"); process.exit(pass ? 0 : 1); })
  .catch((e) => { console.error("E2E ERROR:", e); process.exit(2); });
