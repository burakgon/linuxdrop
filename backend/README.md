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
| GET | `/ice` | ICE servers for the P2P file path (STUN, + TURN if configured) |
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
| `BGN_DOMAIN` | `localhost` | Caddy public hostname (TLS); also coturn's realm |
| `TURN_URL` | _(unset)_ | TURN URL advertised to clients, e.g. `turn:relay.yourdomain.com:3478` (comma-separated for several) |
| `TURN_SECRET` | _(unset)_ | Shared `use-auth-secret` for time-limited TURN credentials — set the same value on the relay and coturn |

## Cross-network file transfer (STUN / TURN)

File bytes go **directly peer-to-peer** (WebRTC); `/ice` hands clients the ICE servers they
use to connect. Public STUN handles direct/hole-punched connections across most networks at
no cost — nothing flows through it.

For the strictest NATs (symmetric NAT, some mobile carriers) where hole-punching fails, the
compose **bundles a TURN relay (coturn)** behind an opt-in profile, so a plain `docker compose
up` stays STUN-only and opens no extra ports:

```bash
export TURN_SECRET=$(openssl rand -hex 32)            # shared by the relay and coturn
export TURN_URL=turn:relay.yourdomain.com:3478
BGN_DOMAIN=relay.yourdomain.com docker compose --profile turn up -d --build
```

- The relay derives short-lived (12 h) HMAC-SHA1 credentials from `TURN_SECRET` and returns
  them via `/ice`; coturn validates them with the matching `--static-auth-secret`. Clients pick
  this up automatically — **no client-side change**.
- Open **UDP 3478** (TURN) and the relay range **UDP 49160–49200** in your firewall.
- On clouds with 1:1 NAT (GCP/AWS/Azure), also add `--external-ip=<public-ip>` to the coturn
  `command` so it advertises the reachable address.
- coturn runs with `network_mode: host`; only `turn:` (plain) is enabled — fine for the native
  clients. Add TLS (`turns:`) only if you also need browser peers.

## Advanced: front with an existing reverse proxy

If you already run nginx/another proxy, use `docker-compose.prod.yml` (binds the relay to
`127.0.0.1:3001`, no Caddy) and terminate TLS yourself. An example nginx vhost is in
[`deploy/nginx-relay.conf.example`](deploy/nginx-relay.conf.example) — note the
`client_max_body_size 30m` needed for blob uploads (must exceed `MAX_BLOB_BYTES = 25 MiB`).

```bash
docker compose -f docker-compose.prod.yml up -d --build
```
