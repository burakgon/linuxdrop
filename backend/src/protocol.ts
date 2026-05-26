// Shared protocol constants/validation. See ../../proto/PROTOCOL.md.

export const PROTOCOL_VERSION = 1;

// roomId = base64url(SHA-256(secret))[:32] → 32 url-safe chars. Accept a bounded range.
const ROOM_RE = /^[A-Za-z0-9_-]{16,128}$/;

export function isValidRoomId(room: string): boolean {
  return ROOM_RE.test(room);
}
