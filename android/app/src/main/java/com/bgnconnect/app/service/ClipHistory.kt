package com.bgnconnect.app.service

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import org.json.JSONArray
import org.json.JSONObject

/**
 * Local clipboard history shared between [SyncForegroundService] (writer) and the
 * Compose UI (reader). Persisted to EncryptedSharedPreferences (Android Keystore
 * backed) so it survives restarts, mirrored into an in-memory StateFlow the UI
 * observes. Clipboard contents never leave the device unencrypted — same threat
 * model as [com.bgnconnect.app.config.Secret].
 */
object ClipHistory {

    data class Item(val text: String, val ts: Long, val outgoing: Boolean)

    private const val MAX = 50
    private const val FILE = "bgnconnect_history"
    private const val KEY = "items"

    private val _items = MutableStateFlow<List<Item>>(emptyList())
    val items: StateFlow<List<Item>> = _items.asStateFlow()

    @Volatile private var prefs: SharedPreferences? = null

    @Synchronized
    private fun prefs(ctx: Context): SharedPreferences {
        prefs?.let { return it }
        val master = MasterKey.Builder(ctx.applicationContext)
            .setKeyScheme(MasterKey.KeyScheme.AES256_GCM).build()
        val p = EncryptedSharedPreferences.create(
            ctx.applicationContext, FILE, master,
            EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
            EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM,
        )
        prefs = p
        _items.value = read(p)
        return p
    }

    /** Load history from disk into the StateFlow. Idempotent; call on app/service start. */
    fun ensure(ctx: Context) { prefs(ctx) }

    /** Record a synced clip (deduping an immediate repeat of the newest entry). */
    fun add(ctx: Context, text: String, outgoing: Boolean) {
        if (text.isEmpty()) return
        val p = prefs(ctx)
        val cur = _items.value
        if (cur.firstOrNull()?.text == text) return
        val next = (listOf(Item(text, System.currentTimeMillis(), outgoing)) + cur).take(MAX)
        _items.value = next
        write(p, next)
    }

    fun clear(ctx: Context) {
        val p = prefs(ctx)
        _items.value = emptyList()
        p.edit().remove(KEY).apply()
    }

    private fun read(p: SharedPreferences): List<Item> {
        val raw = p.getString(KEY, null) ?: return emptyList()
        return runCatching {
            val arr = JSONArray(raw)
            (0 until arr.length()).map {
                val o = arr.getJSONObject(it)
                Item(o.getString("t"), o.getLong("ts"), o.optBoolean("o"))
            }
        }.getOrDefault(emptyList())
    }

    private fun write(p: SharedPreferences, items: List<Item>) {
        val arr = JSONArray()
        items.forEach { arr.put(JSONObject().put("t", it.text).put("ts", it.ts).put("o", it.outgoing)) }
        p.edit().putString(KEY, arr.toString()).apply()
    }
}
