package com.linuxdrop.app.net

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.os.Handler
import android.os.Looper
import android.util.Log
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONObject
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.math.min
import kotlin.random.Random

/**
 * Resilient relay client. OkHttp drives the socket + protocol pings; on top we add
 * exponential backoff + jitter AND an immediate reconnect when the network returns
 * (WiFi↔mobile). A generation counter makes stale-socket callbacks no-ops, so we
 * never end up with two live connections. See proto/PROTOCOL.md §1-2.
 */
class WsClient(
    context: Context,
    relayBaseUrl: String, // ws:// or wss://
    private val roomId: String,
    private val dev: String,
    private val helloEnc: JSONObject? = null, // sealed {name, platform}
    private val onClip: (iv: String, ct: String) -> Unit,
    private val onPeers: (count: Int) -> Unit = {},
    private val onRoster: (devices: List<RosterEntry>) -> Unit = {},
    private val onSignal: (fromDev: String, iv: String, ct: String) -> Unit = { _, _, _ -> },
    private val onState: (connected: Boolean) -> Unit,
) {
    /** A roster entry from the relay: device id + (optional) sealed name/platform. */
    data class RosterEntry(val dev: String, val iv: String?, val ct: String?)

    private val appContext = context.applicationContext
    private val connectivityManager = appContext.getSystemService(ConnectivityManager::class.java)

    private val httpUrl: String = run {
        val base = relayBaseUrl.trimEnd('/')
        val http = when {
            base.startsWith("wss://") -> "https://" + base.removePrefix("wss://")
            base.startsWith("ws://") -> "http://" + base.removePrefix("ws://")
            else -> base
        }
        "$http/ws?room=$roomId&v=1"
    }

    private val client = OkHttpClient.Builder()
        // Keepalive every 45s: short enough to survive NAT/router idle drops and screen-off Wi-Fi
        // power-save (connections were dropping ~every 2 min on a 180s ping → reconnect storms, which
        // cost far more battery than the tiny ping). Still well under the relay's 240s idle timeout.
        .pingInterval(45, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()

    private val running = AtomicBoolean(false)
    private val main = Handler(Looper.getMainLooper())
    private val reconnectRunnable = Runnable { connect() }

    @Volatile private var ws: WebSocket? = null
    @Volatile private var open = false
    @Volatile private var gen = 0 // bumped on each connect; stale callbacks (myGen != gen) are ignored
    private var backoffMs = 1000L

    private val networkCallback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
            // Network came back (e.g. WiFi↔mobile switch) — reconnect promptly if down.
            main.post {
                if (running.get() && !open) {
                    backoffMs = 1000L
                    main.removeCallbacks(reconnectRunnable)
                    main.postDelayed(reconnectRunnable, 300)
                }
            }
        }
    }

    fun start() {
        if (running.compareAndSet(false, true)) {
            runCatching { connectivityManager?.registerDefaultNetworkCallback(networkCallback) }
            connect()
        }
    }

    fun stop() {
        running.set(false)
        runCatching { connectivityManager?.unregisterNetworkCallback(networkCallback) }
        main.removeCallbacks(reconnectRunnable)
        ws?.close(1000, "bye")
        ws = null
    }

    fun sendClip(iv: String, ct: String, idHex: String) {
        val enc = JSONObject().put("v", 1).put("alg", "AES-256-GCM").put("iv", iv).put("ct", ct)
        ws?.send(
            JSONObject().put("t", "clip").put("id", idHex).put("ts", System.currentTimeMillis())
                .put("dev", dev).put("enc", enc).toString(),
        )
    }

    /** Route a WebRTC signaling payload to one peer (relay forwards it; §7). */
    fun sendSignal(toDev: String, iv: String, ct: String, idHex: String) {
        val enc = JSONObject().put("v", 1).put("alg", "AES-256-GCM").put("iv", iv).put("ct", ct)
        ws?.send(
            JSONObject().put("t", "signal").put("id", idHex).put("ts", System.currentTimeMillis())
                .put("dev", dev).put("to", toDev).put("enc", enc).toString(),
        )
    }

    private fun connect() {
        if (!running.get()) return
        val myGen = ++gen
        val req = Request.Builder().url(httpUrl).build()
        ws = client.newWebSocket(req, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                if (myGen != gen) { webSocket.cancel(); return }
                open = true
                backoffMs = 1000L
                onState(true)
                val hello = JSONObject().put("t", "hello").put("dev", dev).put("ts", System.currentTimeMillis())
                helloEnc?.let { hello.put("enc", it) }
                webSocket.send(hello.toString())
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                if (myGen != gen) return
                try {
                    val m = JSONObject(text)
                    when (m.optString("t")) {
                        "clip" -> m.optJSONObject("enc")?.let { onClip(it.getString("iv"), it.getString("ct")) }
                        "peers" -> onPeers(m.optInt("count", 0))
                        "roster" -> onRoster(parseRoster(m))
                        "signal" -> m.optJSONObject("enc")?.let { onSignal(m.optString("dev"), it.getString("iv"), it.getString("ct")) }
                    }
                } catch (e: Exception) {
                    Log.e(TAG, "bad message", e)
                }
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                if (myGen != gen) return
                Log.w(TAG, "ws failure: ${t.message}")
                open = false
                onState(false)
                scheduleReconnect()
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                if (myGen != gen) return
                open = false
                onState(false)
                scheduleReconnect()
            }
        })
    }

    private fun parseRoster(m: JSONObject): List<RosterEntry> {
        val arr = m.optJSONArray("devices") ?: return emptyList()
        val out = ArrayList<RosterEntry>(arr.length())
        for (i in 0 until arr.length()) {
            val o = arr.optJSONObject(i) ?: continue
            val enc = o.optJSONObject("enc")
            out.add(RosterEntry(o.optString("dev"), enc?.optString("iv"), enc?.optString("ct")))
        }
        return out
    }

    private fun scheduleReconnect() {
        if (!running.get()) return
        val jitter = Random.nextLong(0, backoffMs / 5 + 1)
        main.removeCallbacks(reconnectRunnable)
        main.postDelayed(reconnectRunnable, backoffMs + jitter)
        backoffMs = min(backoffMs * 2, 60_000L)
    }

    companion object {
        private const val TAG = "linuxDropWs"
    }
}
