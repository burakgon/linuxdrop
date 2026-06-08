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
            "f9a208b7e84bcc359289bac40a8fd0ac24536741a3148ff0a82e2a966f2aaac6",
            hex.formatHex(LinuxDropCrypto.deriveKey(secret)),
        )
    }

    @Test
    fun seal_matchesVector() {
        val ct = LinuxDropCrypto.fromSecret(secret).sealForTest(iv, plaintext)
        assertEquals("rcyxHkF6X/tznsjTgrMo8+NZZ4PscfZm6O7UTon0boegGXBN72LZB53aAKukhUc0lwszdq/HoIf6", ct)
    }

    @Test
    fun sealOpen_roundTrip() {
        val c = LinuxDropCrypto.fromSecret(secret)
        val (ivB64, ct) = c.seal(plaintext)
        assertEquals(String(plaintext), String(c.open(ivB64, ct)))
    }

    @Test
    fun tetherBleKey_matchesVector() {
        assertEquals(
            "793b6d391031856ed02410d54050c062f02ec2a696c4b3b615e22ff56f130f99",
            hex.formatHex(LinuxDropCrypto.tetherBleKey(secret)),
        )
    }

    @Test
    fun tetherSsidAndPsk_matchVectors() {
        assertEquals("LD-2f0d61cb", LinuxDropCrypto.tetherSsid(secret))
        assertEquals("9ddc1c62b4f9a1da71d45bab", LinuxDropCrypto.tetherPsk(secret))
    }
}
