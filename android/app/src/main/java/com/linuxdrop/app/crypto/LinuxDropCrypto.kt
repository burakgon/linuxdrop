package com.linuxdrop.app.crypto

import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Base64
import javax.crypto.Cipher
import javax.crypto.Mac
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

/**
 * E2E primitives, byte-for-byte compatible with the Go (linux) side and
 * proto/crypto-test-vectors.json. Uses only java.* crypto (works on Android 26+
 * AND in JVM unit tests). See proto/PROTOCOL.md §4.
 */
class LinuxDropCrypto private constructor(private val key: ByteArray) {

    /** Returns iv (base64) and ct = base64(ciphertext || 16-byte GCM tag). */
    fun seal(plaintext: ByteArray): Pair<String, String> {
        val iv = ByteArray(12).also { RNG.nextBytes(it) }
        return Base64.getEncoder().encodeToString(iv) to sealForTest(iv, plaintext)
    }

    /** Deterministic seal for tests/fixed-nonce vectors. Never reuse a nonce in production. */
    fun sealForTest(iv: ByteArray, plaintext: ByteArray): String {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, iv))
        return Base64.getEncoder().encodeToString(cipher.doFinal(plaintext))
    }

    /** Decrypts a wire (iv, ct). Throws on wrong key / tampering (= peer auth). */
    fun open(ivB64: String, ctB64: String): ByteArray {
        val iv = Base64.getDecoder().decode(ivB64)
        val sealed = Base64.getDecoder().decode(ctB64)
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, iv))
        return cipher.doFinal(sealed)
    }

    /** Self-contained blob format for image/file transfer: iv(12) || ciphertext || tag(16). Matches Go SealBlob. */
    fun sealBlob(content: ByteArray): ByteArray {
        val iv = ByteArray(12).also { RNG.nextBytes(it) }
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, iv))
        return iv + cipher.doFinal(content)
    }

    /** Reverses [sealBlob]: splits the leading 12-byte iv, then decrypts. Throws on wrong key/tampering. */
    fun openBlob(blob: ByteArray): ByteArray {
        require(blob.size >= 12) { "blob too short" }
        val iv = blob.copyOfRange(0, 12)
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, iv))
        return cipher.doFinal(blob.copyOfRange(12, blob.size))
    }

    companion object {
        private const val ENC_SALT = "linuxdrop/enc/v1"
        private const val ENC_INFO = "aes-256-gcm"
        private const val TETHER_SALT = "linuxdrop/tether/v1"
        private const val ROOM_ID_LEN = 32
        private val RNG = SecureRandom()

        fun fromSecret(secret: ByteArray): LinuxDropCrypto = LinuxDropCrypto(deriveKey(secret))

        /** A cipher keyed directly by a raw 32-byte key (e.g. K_ble), bypassing secret→encKey. */
        fun fromRawKey(key: ByteArray): LinuxDropCrypto = LinuxDropCrypto(key)

        /** roomId = base64url(SHA-256(secret))[:32], no padding. */
        fun roomId(secret: ByteArray): String {
            val digest = MessageDigest.getInstance("SHA-256").digest(secret)
            return Base64.getUrlEncoder().withoutPadding().encodeToString(digest).substring(0, ROOM_ID_LEN)
        }

        /** encKey = HKDF-SHA256(ikm=secret, salt=ENC_SALT, info=ENC_INFO, len=32). RFC 5869. */
        fun deriveKey(secret: ByteArray): ByteArray {
            val mac = Mac.getInstance("HmacSHA256")
            mac.init(SecretKeySpec(ENC_SALT.toByteArray(Charsets.UTF_8), "HmacSHA256"))
            val prk = mac.doFinal(secret) // extract
            mac.init(SecretKeySpec(prk, "HmacSHA256"))
            mac.update(ENC_INFO.toByteArray(Charsets.UTF_8))
            mac.update(0x01.toByte()) // expand; len<=32 → single block
            return mac.doFinal().copyOf(32)
        }

        /** HKDF-SHA256, single-block expand (len<=32). RFC 5869. Generalizes [deriveKey]. */
        private fun hkdf(secret: ByteArray, salt: String, info: String, len: Int): ByteArray {
            val mac = Mac.getInstance("HmacSHA256")
            mac.init(SecretKeySpec(salt.toByteArray(Charsets.UTF_8), "HmacSHA256"))
            val prk = mac.doFinal(secret)
            mac.init(SecretKeySpec(prk, "HmacSHA256"))
            mac.update(info.toByteArray(Charsets.UTF_8))
            mac.update(0x01.toByte())
            return mac.doFinal().copyOf(len)
        }

        /** AES-256-GCM key for the BLE tether frames (proto/PROTOCOL.md §8). */
        fun tetherBleKey(secret: ByteArray): ByteArray = hkdf(secret, TETHER_SALT, "ble-aead-key", 32)

        /** Stable hotspot SSID derived from the secret: "LD-" + 8 hex chars. */
        fun tetherSsid(secret: ByteArray): String =
            "LD-" + hkdf(secret, TETHER_SALT, "softap-ssid", 4).joinToString("") { "%02x".format(it) }

        /** Stable WPA2 passphrase derived from the secret: 24 hex chars. */
        fun tetherPsk(secret: ByteArray): String =
            hkdf(secret, TETHER_SALT, "softap-psk", 12).joinToString("") { "%02x".format(it) }
    }
}
