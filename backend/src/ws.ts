import type { Server, ServerWebSocket } from "bun";
import { Database } from "bun:sqlite";
import { getLastClip, setLastClip } from "./db.ts";

// Per-connection state attached via server.upgrade(req, { data }).
export type WsData = { room: string; connId: string; dev?: string };

// Presence roster per room: connId -> { dev, enc }. `enc` is the device's
// E2E-encrypted {name, platform} from its hello — opaque to the relay, so device
// names stay private (zero-knowledge). Lives in memory (single instance).
type RosterEntry = { dev: string; enc: unknown };
const rosters = new Map<string, Map<string, RosterEntry>>();

// The relay is a thin pub/sub over Bun's native topics. Each room is a topic;
// `ws.publish(room, ...)` fans out to every OTHER socket in the room (excludes
// the sender) — clip relay. `server.publish` includes everyone — used for
// presence (peers count + roster). The backend never inspects clip `enc`.
export function createWebSocketHandlers(db: Database, getServer: () => Server) {
  function publishPeers(room: string) {
    const server = getServer();
    server.publish(room, JSON.stringify({ t: "peers", count: server.subscriberCount(room), ts: Date.now() }));
  }

  function publishRoster(room: string) {
    const entries = rosters.get(room);
    const devices = entries ? Array.from(entries.values()) : [];
    getServer().publish(room, JSON.stringify({ t: "roster", devices, ts: Date.now() }));
  }

  function removeFromRoster(room: string, connId: string) {
    const entries = rosters.get(room);
    if (entries) {
      entries.delete(connId);
      if (entries.size === 0) rosters.delete(room);
    }
  }

  return {
    open(ws: ServerWebSocket<WsData>) {
      const { room } = ws.data;
      ws.subscribe(room);
      // Catch-up: hand the most recent (encrypted) clip to the new joiner.
      const last = getLastClip(db, room);
      if (last) ws.send(last);
      publishPeers(room);
      publishRoster(room);
    },

    message(ws: ServerWebSocket<WsData>, message: string | Buffer) {
      const text = typeof message === "string" ? message : message.toString("utf8");
      let msg: { t?: string; id?: string; dev?: unknown; enc?: unknown };
      try {
        msg = JSON.parse(text);
      } catch {
        return; // ignore non-JSON frames
      }
      const { room, connId } = ws.data;
      switch (msg.t) {
        case "ping":
          ws.send(JSON.stringify({ t: "pong", ref: msg.id, ts: Date.now() }));
          return;
        case "hello": {
          const dev = typeof msg.dev === "string" ? msg.dev : connId;
          ws.data.dev = dev;
          let entries = rosters.get(room);
          if (!entries) {
            entries = new Map();
            rosters.set(room, entries);
          }
          entries.set(connId, { dev, enc: msg.enc ?? null });
          publishPeers(room);
          publishRoster(room);
          return;
        }
        case "clip":
          setLastClip(db, room, text); // store opaque encrypted frame for catch-up
          ws.publish(room, text); // relay to others (excludes sender)
          return;
        case "ack":
          ws.publish(room, text);
          return;
        default:
          return; // unknown type: drop
      }
    },

    close(ws: ServerWebSocket<WsData>) {
      const { room, connId } = ws.data;
      ws.unsubscribe(room);
      removeFromRoster(room, connId);
      publishPeers(room);
      publishRoster(room);
    },
  };
}
