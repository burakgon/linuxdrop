package com.linuxdrop.app.tether

import com.linuxdrop.app.crypto.LinuxDropCrypto
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import java.util.HexFormat

class TetherFrameTest {
    private val secret = HexFormat.of().parseHex("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
    private val key = LinuxDropCrypto.tetherBleKey(secret)
    private val nonce = ByteArray(16) { it.toByte() }

    @Test
    fun commandRoundTrip_acceptsMonotonicSeq() {
        val verifier = TetherFrame.Verifier(key, nonce)
        val f1 = TetherFrame.sealCommand(key, nonce, seq = 1, opcode = TetherFrame.OP_ENABLE)
        val f2 = TetherFrame.sealCommand(key, nonce, seq = 2, opcode = TetherFrame.OP_KEEPALIVE)
        assertEquals(TetherFrame.OP_ENABLE, verifier.open(f1)!!.opcode)
        assertEquals(TetherFrame.OP_KEEPALIVE, verifier.open(f2)!!.opcode)
    }

    @Test
    fun rejectsReplayedOrStaleSeq() {
        val verifier = TetherFrame.Verifier(key, nonce)
        val f1 = TetherFrame.sealCommand(key, nonce, seq = 5, opcode = TetherFrame.OP_ENABLE)
        assertEquals(TetherFrame.OP_ENABLE, verifier.open(f1)!!.opcode)
        assertNull(verifier.open(f1)) // replay (seq 5 again)
        assertNull(verifier.open(TetherFrame.sealCommand(key, nonce, 4, TetherFrame.OP_DISABLE))) // stale
    }

    @Test
    fun rejectsWrongKeyAndWrongNonce() {
        val verifier = TetherFrame.Verifier(key, nonce)
        val wrongKey = LinuxDropCrypto.tetherBleKey(ByteArray(32) { 9 })
        assertNull(verifier.open(TetherFrame.sealCommand(wrongKey, nonce, 1, TetherFrame.OP_ENABLE)))
        assertNull(verifier.open(TetherFrame.sealCommand(key, ByteArray(16) { 7 }, 1, TetherFrame.OP_ENABLE)))
    }

    @Test
    fun statusRoundTrip() {
        val s = TetherFrame.sealStatus(key, opcode = TetherFrame.OP_ENABLE, result = 0)
        val (op, res) = TetherFrame.openStatus(key, s)!!
        assertEquals(TetherFrame.OP_ENABLE, op)
        assertEquals(0, res)
    }
}
