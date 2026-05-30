package com.linuxdrop.app.crypto

import org.junit.Assert.assertEquals
import org.junit.Test
import java.util.HexFormat

/**
 * Pins the Android crypto to proto/crypto-test-vectors.json (the same vectors the
 * Go side passes). Pure JVM — runs via `./gradlew testDebugUnitTest`, no device.
 */
class LinuxDropCryptoTest {
    private val hex = HexFormat.of()
    private val secret = hex.parseHex("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
    private val plaintext = "{\"type\":\"text\",\"text\":\"hello world 🌍\"}".toByteArray(Charsets.UTF_8)
    private val iv = hex.parseHex("0102030405060708090a0b0c")

    @Test
    fun roomId_matchesVector() {
        assertEquals("Yw3NKWbEM2aRElRIu7JbT_QSpJxzLbLI", LinuxDropCrypto.roomId(secret))
    }

    @Test
    fun deriveKey_matchesVector() {
        assertEquals(
            "12cbf6eb91c325c287b3ebbca8e7335b7fbfe4fe928067f3b4fadfce99abd58b",
            hex.formatHex(LinuxDropCrypto.deriveKey(secret)),
        )
    }

    @Test
    fun seal_matchesVector() {
        val ct = LinuxDropCrypto.fromSecret(secret).sealForTest(iv, plaintext)
        assertEquals("FUS4yaGo5H/3LtuORl3XfhmiIjn2x1QOtZtg/W445IfITLCLHIYaIWezKj1OrlNycD7Qh121JDbHy9nm", ct)
    }

    @Test
    fun sealOpen_roundTrip() {
        val c = LinuxDropCrypto.fromSecret(secret)
        val (ivB64, ct) = c.seal(plaintext)
        assertEquals(String(plaintext), String(c.open(ivB64, ct)))
    }
}
