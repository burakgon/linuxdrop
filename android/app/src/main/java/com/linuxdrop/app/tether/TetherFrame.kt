package com.linuxdrop.app.tether

import com.linuxdrop.app.crypto.LinuxDropCrypto
import java.nio.ByteBuffer

/**
 * Codec for the tether BLE frames (proto/PROTOCOL.md §8). Frames are
 * LinuxDropCrypto.sealBlob(K_ble) = iv(12)||ct||tag(16). A wrong key fails the GCM tag → open()
 * returns null. [Verifier] adds per-connection replay protection (session nonce + strictly
 * increasing seq).
 */
object TetherFrame {
    const val OP_ENABLE: Int = 1
    const val OP_DISABLE: Int = 2
    const val OP_KEEPALIVE: Int = 3

    data class Command(val seq: Long, val opcode: Int)

    /** command plaintext = sessionNonce(16) || seq(4 BE) || opcode(1). */
    fun sealCommand(key: ByteArray, sessionNonce: ByteArray, seq: Long, opcode: Int): ByteArray {
        require(sessionNonce.size == 16) { "nonce must be 16 bytes" }
        val pt = ByteBuffer.allocate(16 + 4 + 1)
            .put(sessionNonce).putInt(seq.toInt()).put(opcode.toByte()).array()
        return LinuxDropCrypto.fromRawKey(key).sealBlob(pt)
    }

    /** status plaintext = opcode(1) || resultCode(1). */
    fun sealStatus(key: ByteArray, opcode: Int, result: Int): ByteArray =
        LinuxDropCrypto.fromRawKey(key).sealBlob(byteArrayOf(opcode.toByte(), result.toByte()))

    /** Returns (opcode, result) or null if the seal is invalid. */
    fun openStatus(key: ByteArray, frame: ByteArray): Pair<Int, Int>? {
        val pt = runCatching { LinuxDropCrypto.fromRawKey(key).openBlob(frame) }.getOrNull() ?: return null
        if (pt.size < 2) return null
        return (pt[0].toInt() and 0xff) to (pt[1].toInt() and 0xff)
    }

    /** Per-connection command verifier: binds to one session nonce, rejects replays/stale seq. */
    class Verifier(private val key: ByteArray, private val sessionNonce: ByteArray) {
        private var lastSeq = 0L

        fun open(frame: ByteArray): Command? {
            val pt = runCatching { LinuxDropCrypto.fromRawKey(key).openBlob(frame) }.getOrNull() ?: return null
            if (pt.size != 21) return null
            val embeddedNonce = pt.copyOfRange(0, 16)
            if (!embeddedNonce.contentEquals(sessionNonce)) return null
            val buf = ByteBuffer.wrap(pt, 16, 5)
            val seq = buf.int.toLong() and 0xffffffffL
            val opcode = buf.get().toInt() and 0xff
            if (seq <= lastSeq) return null // replay / stale
            lastSeq = seq
            return Command(seq, opcode)
        }
    }
}
