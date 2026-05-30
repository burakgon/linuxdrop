import { mkdirSync } from "node:fs";
import { dirname } from "node:path";
import { createServer } from "./server.ts";

const PORT = Number(process.env.PORT ?? 3000);
const DB_PATH = process.env.DB_PATH ?? "./data/linuxdrop.db";

if (DB_PATH !== ":memory:") mkdirSync(dirname(DB_PATH), { recursive: true });

const server = createServer({ port: PORT, dbPath: DB_PATH });
console.log(`LinuxDrop backend listening on :${server.port} (db=${DB_PATH})`);
