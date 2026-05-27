import { Hono } from "hono";
import type { Server } from "bun";
import { dirname } from "node:path";
import { createHmac } from "node:crypto";
import { openDb } from "./db.ts";
import { BlobStore, MAX_BLOB_BYTES } from "./blob.ts";
import { createWebSocketHandlers, type WsData } from "./ws.ts";
import { isValidRoomId, PROTOCOL_VERSION } from "./protocol.ts";

const VERSION = "0.1.0";

// ICE servers handed to clients for the direct P2P (WebRTC) file path. Public STUN
// is enough for direct/hole-punched connections (no data flows through it). If the
// operator sets TURN_URL + TURN_SECRET (coturn `use-auth-secret`), a TURN relay is
// added as a fallback for strict NATs, with short-lived time-limited credentials.
function iceServers() {
  const servers: Array<{ urls: string | string[]; username?: string; credential?: string }> = [
    { urls: ["stun:stun.l.google.com:19302", "stun:stun1.l.google.com:19302"] },
  ];
  const turnUrl = process.env.TURN_URL;
  const turnSecret = process.env.TURN_SECRET;
  if (turnUrl && turnSecret) {
    const expiry = Math.floor(Date.now() / 1000) + 12 * 3600; // 12h TTL
    const username = `${expiry}:bgnconnect`;
    const credential = createHmac("sha1", turnSecret).update(username).digest("base64");
    servers.push({ urls: turnUrl.split(",").map((s) => s.trim()), username, credential });
  }
  return servers;
}

export function createServer(opts: { port: number; dbPath: string }): Server {
  const db = openDb(opts.dbPath);
  const dataDir = opts.dbPath === ":memory:" ? "./data" : dirname(opts.dbPath);
  const blobs = new BlobStore(dataDir);
  const sweep = setInterval(() => blobs.cleanup(), 5 * 60 * 1000);
  sweep.unref?.();

  const app = new Hono();
  app.get("/health", (c) => c.json({ ok: true, uptime: process.uptime() }));
  app.get("/version", (c) => c.json({ version: VERSION, protocol: PROTOCOL_VERSION }));
  // ICE config for the direct P2P file path (STUN, + TURN fallback if configured).
  app.get("/ice", (c) => c.json({ iceServers: iceServers() }));

  // Blob transfer for images/files: client uploads E2E-encrypted bytes, peers fetch
  // by id. Room-scoped so only network members can read; relay never decrypts.
  app.put("/blob", async (c) => {
    const room = c.req.query("room") ?? "";
    if (!isValidRoomId(room)) return c.json({ error: "invalid room" }, 400);
    if (Number(c.req.header("content-length") ?? 0) > MAX_BLOB_BYTES) {
      return c.json({ error: "too large" }, 413);
    }
    const bytes = new Uint8Array(await c.req.arrayBuffer());
    if (bytes.byteLength === 0) return c.json({ error: "empty" }, 400);
    if (bytes.byteLength > MAX_BLOB_BYTES) return c.json({ error: "too large" }, 413);
    const id = await blobs.put(room, bytes);
    return c.json({ id });
  });

  app.get("/blob/:id", async (c) => {
    const room = c.req.query("room") ?? "";
    if (!isValidRoomId(room)) return c.json({ error: "invalid room" }, 400);
    const bytes = await blobs.get(c.req.param("id"), room);
    if (!bytes) return c.json({ error: "not found" }, 404);
    return new Response(bytes, { headers: { "content-type": "application/octet-stream" } });
  });

  // `server` is referenced inside the websocket handlers (for publish /
  // subscriberCount). Handlers fire after Bun.serve() returns, so the lazy
  // getter always sees the assigned value.
  let server: Server;
  const handlers = createWebSocketHandlers(db, () => server);

  server = Bun.serve<WsData, {}>({
    port: opts.port,
    fetch(req, srv) {
      const url = new URL(req.url);
      if (url.pathname === "/ws") {
        const room = url.searchParams.get("room") ?? "";
        const v = url.searchParams.get("v") ?? "";
        if (!isValidRoomId(room)) return new Response("invalid room", { status: 400 });
        if (v !== String(PROTOCOL_VERSION)) {
          return new Response("unsupported protocol version", { status: 426 });
        }
        const ok = srv.upgrade(req, { data: { room, connId: crypto.randomUUID() } });
        return ok ? undefined : new Response("upgrade failed", { status: 500 });
      }
      return app.fetch(req, srv);
    },
    websocket: {
      idleTimeout: 240, // seconds (Bun max 255); clients app-ping well under this
      open: handlers.open,
      message: handlers.message,
      close: handlers.close,
    },
  });

  return server;
}
