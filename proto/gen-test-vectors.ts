// bgnconnect — cross-language crypto test vector generator.
//
// Run:  bun run proto/gen-test-vectors.ts
// Out:  proto/crypto-test-vectors.json
//
// These vectors pin the exact crypto derivations. The Go (linux) and Kotlin
// (android) implementations MUST reproduce `expected` byte-for-byte. The
// backend is zero-knowledge and performs no crypto — it only sees `roomId`.
//
// Derivations:
//   roomId = base64url(SHA-256(secret))         -> first 32 chars, no padding
//   encKey = HKDF-SHA256(ikm=secret, salt="bgnconnect/enc/v1", info="aes-256-gcm", len=32)
//   ct     = AES-256-GCM(key=encKey, iv, plaintext)  -> base64(ciphertext || 16-byte tag)

import { writeFileSync } from "node:fs";

const enc = new TextEncoder();
const b64 = (b: Uint8Array) => Buffer.from(b).toString("base64");
const b64url = (b: Uint8Array) => Buffer.from(b).toString("base64url");
const hex = (b: Uint8Array) => Buffer.from(b).toString("hex");
const fromHex = (s: string) => new Uint8Array(Buffer.from(s, "hex"));

const sha256 = async (d: Uint8Array) =>
  new Uint8Array(await crypto.subtle.digest("SHA-256", d));

async function hkdfSha256(ikm: Uint8Array, salt: Uint8Array, info: Uint8Array, len: number) {
  const key = await crypto.subtle.importKey("raw", ikm, "HKDF", false, ["deriveBits"]);
  const bits = await crypto.subtle.deriveBits({ name: "HKDF", hash: "SHA-256", salt, info }, key, len * 8);
  return new Uint8Array(bits);
}

async function aesGcmEncrypt(key: Uint8Array, iv: Uint8Array, plaintext: Uint8Array) {
  const k = await crypto.subtle.importKey("raw", key, "AES-GCM", false, ["encrypt"]);
  const ct = await crypto.subtle.encrypt({ name: "AES-GCM", iv, tagLength: 128 }, k, plaintext);
  return new Uint8Array(ct); // ciphertext || tag(16)
}

const ENC_SALT = "bgnconnect/enc/v1";
const ENC_INFO = "aes-256-gcm";

// Fixed, deterministic inputs.
const secret = fromHex("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f");
const plaintextStr = '{"type":"text","text":"hello world 🌍"}';
const plaintext = enc.encode(plaintextStr);
const iv = fromHex("0102030405060708090a0b0c"); // 12 bytes

const digest = await sha256(secret);
const roomId = b64url(digest).slice(0, 32);
const encKey = await hkdfSha256(secret, enc.encode(ENC_SALT), enc.encode(ENC_INFO), 32);
const ct = await aesGcmEncrypt(encKey, iv, plaintext);

const vectors = {
  description:
    "bgnconnect crypto test vectors. Go (linux) and Kotlin (android) MUST reproduce `expected` exactly. Backend does no crypto.",
  derivation: {
    roomId: "base64url(SHA-256(secret)), first 32 chars, no padding",
    encKey: `HKDF-SHA256(ikm=secret, salt='${ENC_SALT}', info='${ENC_INFO}', len=32)`,
    cipher: "AES-256-GCM; ct = base64(ciphertext || 16-byte tag)",
  },
  input: {
    secret_hex: hex(secret),
    plaintext_utf8: plaintextStr,
    iv_hex: hex(iv),
    enc_salt: ENC_SALT,
    enc_info: ENC_INFO,
  },
  expected: {
    sha256_secret_hex: hex(digest),
    roomId,
    encKey_hex: hex(encKey),
    ct_base64: b64(ct),
  },
};

writeFileSync(new URL("./crypto-test-vectors.json", import.meta.url), JSON.stringify(vectors, null, 2) + "\n");
console.log(JSON.stringify(vectors, null, 2));
