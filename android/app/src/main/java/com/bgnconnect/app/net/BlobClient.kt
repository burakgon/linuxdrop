package com.bgnconnect.app.net

import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.util.concurrent.TimeUnit

/**
 * HTTP client for the relay's short-lived, E2E-encrypted blob store (images/files).
 * Bytes are sealed by the caller (BgnCrypto.sealBlob); the relay never decrypts.
 * See proto/PROTOCOL.md §6. All calls are blocking — invoke off the main thread.
 */
class BlobClient(relayBaseUrl: String) {

    private val base = run {
        val b = relayBaseUrl.trimEnd('/')
        when {
            b.startsWith("wss://") -> "https://" + b.removePrefix("wss://")
            b.startsWith("ws://") -> "http://" + b.removePrefix("ws://")
            else -> b
        }
    }

    private val http = OkHttpClient.Builder()
        .callTimeout(60, TimeUnit.SECONDS)
        .build()

    private val octet = "application/octet-stream".toMediaType()

    /** Uploads encrypted bytes; returns the relay-assigned blob id, or null on failure. */
    fun put(room: String, data: ByteArray): String? {
        val req = Request.Builder()
            .url("$base/blob?room=$room")
            .put(data.toRequestBody(octet))
            .build()
        return runCatching {
            http.newCall(req).execute().use { resp ->
                if (!resp.isSuccessful) return null
                val body = resp.body?.string() ?: return null
                JSONObject(body).optString("id").ifEmpty { null }
            }
        }.getOrNull()
    }

    /** Downloads encrypted bytes for a blob id (room-scoped); null if missing/expired. */
    fun get(room: String, id: String): ByteArray? {
        val req = Request.Builder().url("$base/blob/$id?room=$room").get().build()
        return runCatching {
            http.newCall(req).execute().use { resp ->
                if (!resp.isSuccessful) return null
                resp.body?.bytes()
            }
        }.getOrNull()
    }
}
