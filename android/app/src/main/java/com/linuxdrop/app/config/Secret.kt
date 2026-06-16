package com.linuxdrop.app.config

import android.content.Context
import android.net.Uri
import android.os.Build
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import java.security.SecureRandom
import java.util.Base64

/**
 * Stores the sync key (shared secret), relay URL and device name in
 * EncryptedSharedPreferences (Android Keystore backed). The key pairs devices and
 * derives the E2E key; it is auto-generated and kept out of the way — revealed
 * only on request. There is no built-in relay: the user points the app at their
 * own self-hosted server. See proto/PROTOCOL.md §4.
 */
class Secret(context: Context) {

    private val prefs = run {
        val key = MasterKey.Builder(context)
            .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
            .build()
        EncryptedSharedPreferences.create(
            context,
            "linuxdrop_secret",
            key,
            EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
            EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM,
        )
    }

    /** Relay URL of the user's self-hosted server. Empty until set during onboarding/pairing. */
    var relayUrl: String
        get() = prefs.getString(KEY_RELAY, null)?.takeIf { it.isNotBlank() } ?: ""
        set(v) = prefs.edit().putString(KEY_RELAY, v.trim()).apply()

    /** Hex-encoded sync key (the "network"). Null until a network is created/joined. */
    var secretHex: String?
        get() = prefs.getString(KEY_SECRET, null)
        set(v) = prefs.edit().putString(KEY_SECRET, v).apply()

    /** Friendly device name shown to other devices. Defaults to the model. */
    var deviceName: String
        get() = prefs.getString(KEY_NAME, null)?.takeIf { it.isNotBlank() } ?: defaultName()
        set(v) = prefs.edit().putString(KEY_NAME, v.trim()).apply()

    /** Stable per-install device id (random; created lazily). */
    val deviceId: String
        get() = prefs.getString(KEY_DEV, null) ?: ("andr-" + randomHex(3)).also {
            prefs.edit().putString(KEY_DEV, it).apply()
        }

    /** Whether this phone may share its internet (over the BLE tether) when a paired computer asks. */
    var tetherEnabled: Boolean
        get() = prefs.getBoolean(KEY_TETHER, true)
        set(v) = prefs.edit().putBoolean(KEY_TETHER, v).apply()

    fun secretBytes(): ByteArray? = secretHex?.let { hexToBytes(it) }

    /** Configured once both a relay (self-hosted server) and a network key exist. */
    fun isConfigured(): Boolean = relayUrl.isNotBlank() && !secretHex.isNullOrBlank()

    /** Create a new network (key) if none exists; returns the key hex. */
    fun ensureNetwork(): String = secretHex ?: newSecretHex().also { secretHex = it }

    /** Forget the current network key (used by "regenerate"). */
    fun clearNetwork() = prefs.edit().remove(KEY_SECRET).apply()

    /** Pairing string for QR / text: linuxdrop://pair?s=<b64url>&relay=<url>. */
    fun pairingUri(): String? {
        val bytes = secretBytes() ?: return null
        val s = Base64.getUrlEncoder().withoutPadding().encodeToString(bytes)
        return "linuxdrop://pair?s=$s&relay=${Uri.encode(relayUrl)}"
    }

    /** Accepts a linuxdrop:// pairing URI or a raw hex key. Returns true on success. */
    fun importPairing(input: String): Boolean {
        val text = input.trim()
        if (text.startsWith("linuxdrop://")) {
            val uri = Uri.parse(text)
            val bytes = base64UrlDecode(uri.getQueryParameter("s") ?: return false) ?: return false
            if (bytes.size < 16) return false
            secretHex = bytesToHex(bytes)
            uri.getQueryParameter("relay")?.takeIf { it.isNotBlank() }?.let { relayUrl = it }
            return true
        }
        val bytes = runCatching { hexToBytes(text) }.getOrNull() ?: return false
        if (bytes.size < 16) return false
        secretHex = bytesToHex(bytes)
        return true
    }

    private fun defaultName(): String =
        listOf(Build.MANUFACTURER, Build.MODEL).filter { !it.isNullOrBlank() }
            .joinToString(" ").ifBlank { "Android" }
            .replaceFirstChar { it.uppercase() }

    companion object {
        private const val KEY_RELAY = "relay"
        private const val KEY_SECRET = "secret"
        private const val KEY_DEV = "dev"
        private const val KEY_NAME = "name"
        private const val KEY_TETHER = "tether_enabled"

        fun newSecretHex(): String = bytesToHex(ByteArray(32).also { SecureRandom().nextBytes(it) })

        private fun randomHex(n: Int) = bytesToHex(ByteArray(n).also { SecureRandom().nextBytes(it) })
        private fun bytesToHex(b: ByteArray): String = b.joinToString("") { "%02x".format(it) }

        private fun hexToBytes(s: String): ByteArray {
            val clean = s.trim()
            require(clean.length % 2 == 0) { "odd hex length" }
            return ByteArray(clean.length / 2) { clean.substring(it * 2, it * 2 + 2).toInt(16).toByte() }
        }

        private fun base64UrlDecode(s: String): ByteArray? =
            runCatching { Base64.getUrlDecoder().decode(s) }.getOrNull()
    }
}
