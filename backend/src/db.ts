import { Database } from "bun:sqlite";

// The backend is zero-knowledge: `last_clip.envelope` is the raw (E2E-encrypted)
// WebSocket frame, stored verbatim so a reconnecting device can catch up on the
// most recent clipboard. The backend never decrypts it.

export function openDb(path: string): Database {
  const db = new Database(path, { create: true });
  db.run("PRAGMA journal_mode = WAL");
  db.run("PRAGMA busy_timeout = 5000");
  db.run(`CREATE TABLE IF NOT EXISTS last_clip (
    room     TEXT PRIMARY KEY,
    envelope TEXT NOT NULL,
    ts       INTEGER NOT NULL
  )`);
  return db;
}

export function getLastClip(db: Database, room: string): string | null {
  const row = db
    .query<{ envelope: string }, [string]>("SELECT envelope FROM last_clip WHERE room = ?")
    .get(room);
  return row?.envelope ?? null;
}

export function setLastClip(db: Database, room: string, envelope: string): void {
  db.query("INSERT OR REPLACE INTO last_clip (room, envelope, ts) VALUES (?, ?, ?)").run(
    room,
    envelope,
    Date.now(),
  );
}
