import { mkdirSync, readdirSync, rmSync } from "node:fs";
import { join } from "node:path";

// Short-lived store for large clipboard payloads (images/files). The bytes are
// E2E-encrypted by the client (iv||ciphertext||tag), so the relay stores opaque
// blobs and stays zero-knowledge — it only knows the room + size. Blobs live on
// disk with an in-memory index; they expire fast (transfer is near-immediate) and
// orphans are cleared on startup, keeping the SQLite layer purely for catch-up.

export const MAX_BLOB_BYTES = 25 * 1024 * 1024; // 25 MiB
const TTL_MS = 30 * 60 * 1000; // 30 min
const ID_RE = /^[a-f0-9]{32}$/;

type Meta = { room: string; size: number; expires: number };

export class BlobStore {
  private dir: string;
  private index = new Map<string, Meta>();

  constructor(dataDir: string) {
    this.dir = join(dataDir, "blobs");
    // Wipe any orphans from a previous run — blobs are ephemeral by design.
    rmSync(this.dir, { recursive: true, force: true });
    mkdirSync(this.dir, { recursive: true });
  }

  async put(room: string, bytes: Uint8Array): Promise<string> {
    const id = crypto.randomUUID().replace(/-/g, "");
    await Bun.write(join(this.dir, id), bytes);
    this.index.set(id, { room, size: bytes.byteLength, expires: Date.now() + TTL_MS });
    return id;
  }

  /** Returns the blob bytes only if the id is valid, unexpired, and room matches. */
  async get(id: string, room: string): Promise<Uint8Array | null> {
    if (!ID_RE.test(id)) return null;
    const meta = this.index.get(id);
    if (!meta || meta.room !== room) return null;
    if (meta.expires < Date.now()) {
      this.delete(id);
      return null;
    }
    const file = Bun.file(join(this.dir, id));
    if (!(await file.exists())) return null;
    return new Uint8Array(await file.arrayBuffer());
  }

  private delete(id: string) {
    this.index.delete(id);
    rmSync(join(this.dir, id), { force: true });
  }

  /** Drop expired blobs (call periodically). */
  cleanup() {
    const now = Date.now();
    for (const [id, meta] of this.index) {
      if (meta.expires < now) this.delete(id);
    }
    // Sweep any on-disk files with no index entry (defensive).
    for (const name of readdirSync(this.dir)) {
      if (!this.index.has(name)) rmSync(join(this.dir, name), { force: true });
    }
  }
}
