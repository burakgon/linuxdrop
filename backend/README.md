# bgnconnect — relay backend

Hono + Bun WebSocket relay. **Zero-knowledge**: it only sees the `roomId` (a hash of the
shared secret) and routes opaque, E2E-encrypted frames between a room's devices. It never
decrypts clipboard content. SQLite stores only the last (encrypted) frame per room for
reconnect catch-up, plus a short-lived on-disk blob store for image/file transfer.

## Development
```bash
bun install
bun run dev          # ws://localhost:3000 ; GET /health, /version
bun test             # relay + catch-up + ping + peers + blob tests
```

## Endpoints
| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | `{ ok, uptime }` |
| GET | `/version` | `{ version, protocol }` |
| GET | `/ws?room=<roomId>&v=1` | WebSocket relay |
| PUT | `/blob?room=<roomId>` | Upload an E2E-encrypted blob → `{ id }` |
| GET | `/blob/<id>?room=<roomId>` | Download an E2E-encrypted blob |

Message format: [`../proto/PROTOCOL.md`](../proto/PROTOCOL.md).

## Deploy (Docker + automatic TLS)

The bundled compose runs the relay behind **Caddy**, which auto-provisions a Let's Encrypt
certificate — `wss://` works out of the box. You only need a domain pointing at the host.

```bash
# With your public domain (automatic Let's Encrypt TLS → wss://):
BGN_DOMAIN=relay.yourdomain.com docker compose up -d --build

# Local/test (Caddy serves a locally-trusted self-signed cert):
docker compose up -d --build
```
- Devices then connect to `wss://relay.yourdomain.com` (used as the relay URL when pairing).
- No public domain? You can also reach it over a Tailscale/WireGuard network
  (then `ws://<tailscale-ip>:3000`, without Caddy).
- SQLite data persists in the `bgn-data` volume; blobs live under `/data/blobs` (ephemeral).

## Environment variables
| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `3000` | Listen port |
| `DB_PATH` | `/data/bgnconnect.db` | SQLite path (`:memory:` for tests) |
| `BGN_DOMAIN` | `localhost` | Caddy public hostname (TLS) |

## Advanced: front with an existing reverse proxy

If you already run nginx/another proxy, use `docker-compose.prod.yml` (binds the relay to
`127.0.0.1:3001`, no Caddy) and terminate TLS yourself. An example nginx vhost is in
[`deploy/nginx-relay.conf.example`](deploy/nginx-relay.conf.example) — note the
`client_max_body_size 30m` needed for blob uploads (must exceed `MAX_BLOB_BYTES = 25 MiB`).

```bash
docker compose -f docker-compose.prod.yml up -d --build
```
