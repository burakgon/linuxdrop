# LinuxDrop protocol (v1)

Three components speak this protocol: the **backend** (Bun relay), **linux** (Go daemon),
and **android** (Kotlin). The backend is **zero-knowledge**: it only ever sees the `roomId`
and cannot decrypt clipboard content (the encrypted payload).

## 1. Transport

- WebSocket. `wss://` in production (TLS, terminated by Caddy or your reverse proxy); `ws://` in dev.
- Connect: `GET /ws?room=<roomId>&v=1`
  - `room` = `roomId` (below; a hash of the secret — an opaque routing key for the backend).
  - `v` = protocol version (`1`).
- Frames are **UTF-8 JSON text**.

## 2. Message envelope

```jsonc
{
  "t":   "clip",            // type: hello | peers | roster | clip | ack | ping | pong
  "id":  "01J8...",         // ULID; for ack + dedup
  "ts":  1716580000123,     // sender's unix-ms clock
  "dev": "andr-a1b2",       // sender device id (short, random; UX/logs — NOT routing)
  "enc": {                  // on "clip" + "hello" (hello: sealed {name, platform})
    "v":   1,
    "alg": "AES-256-GCM",
    "iv":  "<base64(12-byte nonce)>",
    "ct":  "<base64(ciphertext || 16-byte GCM tag)>"
  }
}
```

### Message types

| `t`      | Direction      | Extra fields      | Meaning |
|----------|----------------|-------------------|---------|
| `hello`  | client→server  | `enc` (sealed `{name,platform}`) | Opening; the device announces itself (`dev`, `ts`) |
| `peers`  | server→client  | `count`           | Number of devices in the room |
| `roster` | server→client  | `devices`         | Connected devices: `[{dev, enc}]` — `enc` decrypts to `{name,platform}` |
| `clip`   | bidirectional  | `enc`             | Clipboard update (encrypted) |
| `signal` | client→client (relayed) | `to` (recipient `dev`), `enc` | WebRTC setup for direct P2P file transfer (§7) |
| `ack`    | bidirectional  | `ref` (message id) | Delivery acknowledgement (optional) |
| `ping`   | bidirectional  | —                 | App-layer keepalive |
| `pong`   | bidirectional  | `ref`             | Reply to `ping` |

The backend relays `clip`/`ack` to the **other** sockets in the same room (excluding the
sender), replies to `ping` with `pong`, and broadcasts `peers` + `roster` to the room on
open/`hello`/close. The `enc` in roster entries is **opaque** to the backend → device names
are never visible to it (zero-knowledge is preserved); each client decrypts with its own key
and marks "this device" where `dev` == its own id.

## 3. Plaintext payload (once `enc` is decrypted)

```jsonc
// type:"text" — clipboard text directly in the payload
{ "type":"text", "text":"<clipboard text>", "ch":"<sha256hex(text)>", "origin":"<dev>", "ts":1716580000123 }

// type:"image" | "file" — large content travels as a blob (see §6)
{ "type":"image", "name":"screenshot.png", "mime":"image/png", "size":48213,
  "blobId":"<32 hex>", "ch":"<sha256hex(content bytes)>", "origin":"<dev>", "ts":1716580000123 }
```

- `ch`: content hash — for **loop prevention** and dedup. For text it's the SHA-256 of the text;
  for a blob it's the SHA-256 of the raw content bytes.
- `origin`: sender device id — defense-in-depth (ignore our own origin).
- A `type:"text"` payload is self-contained. For `type:"image"|"file"` the content travels
  separately by `blobId` (§6); the payload carries only metadata (name, mime, size, blobId),
  and that metadata is itself inside `enc` → the relay can't see the filename/mime either.

## 4. Cryptography

The secret never reaches the backend. All derivations MUST match **byte-for-byte** on both
ends (Go + Kotlin). Verification: [`crypto-test-vectors.json`](./crypto-test-vectors.json)
(generator: `gen-test-vectors.ts`).

```
secret  = 32-byte CSPRNG (shared on pairing: QR + text)
roomId  = base64url( SHA-256(secret) )            → first 32 characters, no padding
encKey  = HKDF-SHA256(ikm=secret,
                      salt="linuxdrop/enc/v1",
                      info="aes-256-gcm", len=32)
clip.ct = AES-256-GCM(key=encKey, iv=random 12B, plaintext)  → base64(ciphertext || tag16)
```

- A **fresh random 12-byte IV** for every `clip`. IV reuse is catastrophic in GCM.
- AAD is unused in v1 (future: replay hardening with `roomId || ts`).
- Wrong secret → GCM auth-tag verification fails → the payload is rejected (= authentication).

### Pairing

- One device generates the `secret`; it is transferred to the others via **QR** or **text**:
  `linuxdrop://pair?s=<base64url(secret)>&relay=<wss-url>`
- The backend is not involved in pairing (pairing is entirely offline + E2E).

## 5. Loop prevention

The classic bidirectional-sync trap: A writes → B receives → B writes to its own clipboard →
"change" on B → B sends it back → …

Two layers:
1. **Content-hash dedup (primary):** each device keeps a `lastSeenHash`. When a `clip` arrives
   from the network, **set `lastSeenHash = ch` FIRST, then write to the clipboard.** When the
   local clipboard event triggered by that write arrives, the hash matches → it is ignored. When
   something genuinely changes locally and the hash differs, it is sent and `lastSeenHash` updates.
2. **Origin (secondary):** if the payload's `origin == myDev`, ignore it (the backend already
   excludes the sender; defense-in-depth).

## 6. Blob transfer (images & files)

Instead of carrying large content in a WS frame, a short-lived, **E2E-encrypted** blob store on
the relay is used (the WS frame stays small, catch-up doesn't bloat, memory stays low):

```
PUT  /blob?room=<roomId>           body = blobEnc (binary)   → 200 { "id":"<32 hex>" }
GET  /blob/<id>?room=<roomId>                                 → 200 blobEnc (binary)
```

- `blobEnc = iv(12B) || AES-256-GCM(key=encKey, iv, content) (ciphertext || tag16)` — i.e. the
  blob prepends its own random IV (self-contained). The same `encKey` (HKDF, §4) is used.
- **Flow:** the sender encrypts the content → `PUT /blob` → puts the returned `blobId` into a
  `type:"image"` payload → broadcasts a normal `clip` message. The receiver decrypts the `clip`
  → `GET /blob/<blobId>` → decrypts the downloaded `blobEnc` → writes it to the clipboard/file.
- **Room scope:** a blob is readable only with the same `roomId`; the relay can't decrypt the
  content (zero-knowledge).
- **Limits:** max size `MAX_BLOB_BYTES = 25 MiB`; TTL ≈ 30 min; blobs are wiped on relay restart
  (it's an ephemeral transfer channel). An expired/missing blob → the receiver silently skips (`404`).
- v1.1 scope: **images** (clipboard image/* MIME). Files reuse the blob infrastructure; client
  integration is extended per platform.

## 7. Direct P2P file transfer (WebRTC)

Large **file sharing** does NOT go through the relay — devices connect **directly** via a WebRTC
DataChannel and stream the bytes peer-to-peer (full LAN speed when on the same network; a
hole-punched direct path across networks). The relay carries only the tiny **signaling**.

```
GET /ice                       → { "iceServers": [ {urls:"stun:…"}, {urls:"turn:…",username,credential}? ] }
```

- **ICE config:** clients fetch `/ice` for STUN (always) and TURN (only if the operator set
  `TURN_URL`+`TURN_SECRET`; coturn `use-auth-secret`, time-limited creds). STUN carries no data;
  TURN is an optional fallback relay for strict NATs.
- **Signaling:** exchanged as `signal` envelopes routed to one peer by `to=<dev>`. The `enc` seals one of:
  `{kind:"offer"|"answer", sdp}` or `{kind:"candidate", candidate, sdpMid, sdpMLineIndex}`.
  **Trickle ICE:** offer/answer are sent immediately after `setLocalDescription`; ICE candidates follow
  as they're gathered (candidates arriving before the remote SDP is applied are queued, then flushed).
  The **entire SDP (including the DTLS `a=fingerprint`) is E2E-encrypted with the room key**, so a
  malicious relay cannot MITM the DTLS handshake — the shared secret authenticates the peer.
- **Transfer (over the DataChannel `linuxdrop-file`, DTLS-encrypted by WebRTC):**
  1. `{"t":"head","name","size","mime","sha256"}`
  2. binary chunks (~16 KiB, with backpressure)
  3. `{"t":"done"}` → receiver verifies size + SHA-256, saves to Downloads, notifies. `{"t":"err"}` aborts.
- Receiving is **auto-accept + notify** (devices are already paired by the shared secret). No resume in v1.

## §8. Webcam signaling (v0.4+)

The phone exposes its camera as a virtual webcam on Linux (`/dev/video20` via `v4l2loopback`). Video
runs over a **separate** WebRTC PeerConnection from the file-transfer path; it shares the same
E2E-sealed `signal` envelope on the relay (no relay change). The Linux side initiates, the phone
auto-accepts after a one-time CAMERA permission grant — zero per-session interaction.

`session` is an 8-byte hex string the initiator mints; it lets a webcam session run concurrently with
a file transfer in the same room without crossing wires.

```json
// Linux → phone (initiate)
{"kind":"webcam-request","session":"a1b2c3d4e5f6a7b8","w":1280,"h":720,"fps":30,
 "camera":"back","codec_pref":"h264"}

// Phone → Linux (SDP offer with one sendonly video transceiver)
{"kind":"webcam-offer","session":"a1b2c3d4e5f6a7b8","sdp":"v=0\r\n..."}

// Linux → phone (SDP answer)
{"kind":"webcam-answer","session":"a1b2c3d4e5f6a7b8","sdp":"v=0\r\n..."}

// Either side (trickle ICE — candidates arriving before remote SDP are queued, flushed on remote-set)
{"kind":"webcam-candidate","session":"a1b2c3d4e5f6a7b8",
 "candidate":"candidate:842163049 1 udp 1677729535 ...",
 "sdpMid":"0","sdpMLineIndex":0}

// Either side (teardown)
{"kind":"webcam-stop","session":"a1b2c3d4e5f6a7b8","reason":"user"}
```

- **Codec:** `codec_pref` is advisory ("h264" or "hevc"). The SDP answer is authoritative — the picker
  takes the strongest mutually-supported codec; H.264 is the universal floor, HEVC is opportunistic.
- **Errors:** the phone returns `webcam-stop` with `reason ∈ {"no-permission","no-camera","no-encoder","in-use"}`
  instead of an offer. The Linux side surfaces the reason via `notify-send`.
- **Teardown grace:** 10 s after WebRTC reports `disconnected`, both sides emit/honor `webcam-stop`.
- **No data on the relay:** the video bytes flow peer-to-peer (LAN-direct or hole-punched); the relay
  only forwards the small, sealed signaling frames.
