package com.linuxdrop.app.service

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import com.linuxdrop.app.MainActivity
import com.linuxdrop.app.R
import com.linuxdrop.app.config.Secret
import android.net.Uri
import com.linuxdrop.app.crypto.LinuxDropCrypto
import com.linuxdrop.app.net.BlobClient
import com.linuxdrop.app.net.P2pManager
import com.linuxdrop.app.net.WsClient
import com.linuxdrop.app.shizuku.ShizukuClipboard
import com.linuxdrop.app.tether.TetherGattServer
import org.json.JSONObject
import java.security.MessageDigest
import java.util.concurrent.Executors

/**
 * Holds the relay connection (foreground service, type connectedDevice — no 6h
 * limit) and the Shizuku-backed background clipboard reader/writer, wiring them
 * through the E2E cipher with two-layer loop prevention. See proto/PROTOCOL.md §5.
 */
class SyncForegroundService : Service() {

    private lateinit var crypto: LinuxDropCrypto
    private lateinit var ws: WsClient
    private lateinit var shizuku: ShizukuClipboard
    private lateinit var blob: BlobClient
    private var p2p: P2pManager? = null
    private var tetherGatt: TetherGattServer? = null
    private lateinit var dev: String
    private lateinit var room: String

    // Blob upload/download is network I/O — keep it off the binder/callback threads.
    private val io = Executors.newSingleThreadExecutor()

    private val lock = Any()
    private var lastHash: String? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        createChannel()
        ClipHistory.ensure(this)
        startForegroundNotification("Starting…")
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        // A "send file" request to the already-running service (P2P, direct).
        if (intent?.action == ACTION_SEND_FILE) {
            val toDev = intent.getStringExtra(EXTRA_TO_DEV)
            val uri = intent.data
            if (toDev != null && uri != null) {
                io.execute { runCatching { p2p?.sendFile(toDev, uri) } }
            }
            return START_STICKY
        }

        val secret = Secret(this)
        val bytes = secret.secretBytes()
        val relay = secret.relayUrl
        if (bytes == null || relay.isNullOrBlank()) {
            Log.w(TAG, "not configured; stopping")
            stopSelf()
            return START_NOT_STICKY
        }

        crypto = LinuxDropCrypto.fromSecret(bytes)
        dev = secret.deviceId
        room = LinuxDropCrypto.roomId(bytes)
        blob = BlobClient(relay)

        shizuku = ShizukuClipboard(this)
        shizuku.startWatching(
            onText = { text -> onLocalClip(text) },
            onImage = { data, mime -> onLocalImage(data, mime) },
        )

        SyncStatus.setRunning(true)
        ws = WsClient(
            context = this,
            relayBaseUrl = relay,
            roomId = room,
            dev = dev,
            helloEnc = buildHelloEnc(secret.deviceName),
            onClip = { iv, ct -> onRemoteClip(iv, ct) },
            onPeers = { n -> SyncStatus.setPeerCount(n) },
            onRoster = { entries -> onRoster(entries) },
            onSignal = { fromDev, iv, ct -> onRemoteSignal(fromDev, iv, ct) },
            onState = { connected ->
                SyncStatus.setConnected(connected)
                startForegroundNotification(if (connected) "Connected" else "Connecting…")
            },
        )
        ws.start()

        // Direct P2P file transfer: signaling rides ws (sealed), bytes go peer-to-peer.
        p2p = P2pManager(
            context = this,
            relayBaseUrl = relay,
            dev = dev,
            sendSignal = { toDev, payload ->
                val (iv, ct) = crypto.seal(payload)
                ws.sendSignal(toDev, iv, ct, randomId())
            },
            onReceived = { name, size, uri ->
                ClipHistory.addFile(this, name, outgoing = false, size = size, uri = uri?.toString())
                notifyFile("Received $name")
            },
            onSent = { name, size, ok ->
                if (ok) ClipHistory.addFile(this, name, outgoing = true, size = size, uri = null)
                notifyFile(if (ok) "Sent $name" else "Failed to send $name")
            },
        )

        // BLE tether wake: lets a paired computer with no internet ask us to enable the hotspot.
        // Only armed when the user has the feature on (Home → "Phone internet sharing").
        if (secret.tetherEnabled) {
            val tetherCtrl = com.linuxdrop.app.tether.TetherController(this)
            tetherCtrl.setOnAutoOff { SyncStatus.setTether(false, "") }
            tetherGatt = TetherGattServer(this, bytes, tetherCtrl).also { runCatching { it.start() } }
        }

        return START_STICKY
    }

    /** Inbound P2P signaling: decrypt and hand to the WebRTC manager. */
    private fun onRemoteSignal(fromDev: String, iv: String, ct: String) {
        val payload = try {
            crypto.open(iv, ct)
        } catch (e: Exception) {
            Log.w(TAG, "signal decrypt failed")
            return
        }
        p2p?.onSignal(fromDev, payload)
    }

    private fun notifyFile(text: String) {
        val n = NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle("LinuxDrop")
            .setContentText(text)
            .setSmallIcon(R.drawable.ic_sync)
            .setAutoCancel(true)
            .build()
        runCatching {
            (getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager)
                .notify((System.currentTimeMillis() % 100000).toInt(), n)
        }
    }

    override fun onDestroy() {
        runCatching { ws.stop() }
        runCatching { shizuku.unbind() }
        runCatching { p2p?.close() }
        runCatching { tetherGatt?.stop() }
        runCatching { io.shutdownNow() }
        SyncStatus.setRunning(false)
        super.onDestroy()
    }

    /** Local clipboard changed (from the Shizuku watcher) → encrypt + send. */
    private fun onLocalClip(text: String) {
        if (text.isEmpty()) return
        val h = sha256Hex(text)
        synchronized(lock) {
            if (h == lastHash) return
            lastHash = h
        }
        val payload = JSONObject()
            .put("type", "text").put("text", text)
            .put("ch", h).put("origin", dev).put("ts", System.currentTimeMillis())
        val (iv, ct) = crypto.seal(payload.toString().toByteArray(Charsets.UTF_8))
        ws.sendClip(iv, ct, randomId())
        SyncStatus.setLastSync(outgoing = true, chars = text.length)
        ClipHistory.add(this, text, outgoing = true)
        Log.i(TAG, "→ sent clipboard (${text.length} chars)")
    }

    /** Local image copied → encrypt, upload as a blob, broadcast a clip referencing it. §6 */
    private fun onLocalImage(bytes: ByteArray, mime: String) {
        if (bytes.isEmpty()) return
        val h = sha256Hex(bytes)
        synchronized(lock) {
            if (h == lastHash) return
            lastHash = h
        }
        io.execute {
            val id = blob.put(room, crypto.sealBlob(bytes)) ?: run {
                Log.w(TAG, "image upload failed")
                return@execute
            }
            val payload = JSONObject()
                .put("type", "image").put("name", "clipboard.${extFor(mime)}").put("mime", mime)
                .put("size", bytes.size).put("blobId", id)
                .put("ch", h).put("origin", dev).put("ts", System.currentTimeMillis())
            val (iv, ct) = crypto.seal(payload.toString().toByteArray(Charsets.UTF_8))
            ws.sendClip(iv, ct, randomId())
            SyncStatus.setLastSync(outgoing = true, chars = bytes.size)
            Log.i(TAG, "→ sent image (${bytes.size} bytes)")
        }
    }

    /** Remote clip from the relay → decrypt, then dispatch by type (loop-safe). */
    private fun onRemoteClip(iv: String, ct: String) {
        val payload = try {
            JSONObject(String(crypto.open(iv, ct), Charsets.UTF_8))
        } catch (e: Exception) {
            Log.w(TAG, "decrypt failed (wrong secret?)")
            return
        }
        if (payload.optString("origin") == dev) return // our own (defense-in-depth)
        when (payload.optString("type")) {
            "text" -> onRemoteText(payload)
            "image", "file" -> onRemoteImage(payload)
        }
    }

    private fun onRemoteText(payload: JSONObject) {
        val text = payload.optString("text")
        if (text.isEmpty()) return
        val h = sha256Hex(text)
        synchronized(lock) {
            if (h == lastHash) return
            lastHash = h // set BEFORE writing so the write-induced watch event is swallowed
        }
        shizuku.setClipboard(text)
        SyncStatus.setLastSync(outgoing = false, chars = text.length)
        ClipHistory.add(this, text, outgoing = false)
        Log.i(TAG, "← received clipboard (${text.length} chars)")
    }

    /** Remote image → fetch blob, decrypt, write to the clipboard. Missing blobs skipped. §6 */
    private fun onRemoteImage(payload: JSONObject) {
        val blobId = payload.optString("blobId")
        if (blobId.isEmpty()) return
        val ch = payload.optString("ch")
        synchronized(lock) { if (ch == lastHash) return }
        val mime = payload.optString("mime", "image/png")
        io.execute {
            val sealed = blob.get(room, blobId) ?: run {
                Log.w(TAG, "image download skipped (expired?)")
                return@execute
            }
            val data = try {
                crypto.openBlob(sealed)
            } catch (e: Exception) {
                Log.w(TAG, "image decrypt failed")
                return@execute
            }
            val h = sha256Hex(data)
            synchronized(lock) {
                if (h == lastHash) return@execute
                lastHash = h // set BEFORE writing so the write-induced watch event is swallowed
            }
            shizuku.setImage(data, mime)
            SyncStatus.setLastSync(outgoing = false, chars = data.size)
            Log.i(TAG, "← received image (${data.size} bytes)")
        }
    }

    private fun extFor(mime: String): String = when {
        mime.contains("png") -> "png"
        mime.contains("jpeg") || mime.contains("jpg") -> "jpg"
        mime.contains("gif") -> "gif"
        mime.contains("webp") -> "webp"
        else -> "img"
    }

    /** Seals this device's {name, platform} for the relay roster (relay can't read it). */
    private fun buildHelloEnc(name: String): JSONObject {
        val payload = JSONObject().put("name", name).put("platform", "android")
        val (iv, ct) = crypto.seal(payload.toString().toByteArray(Charsets.UTF_8))
        return JSONObject().put("v", 1).put("alg", "AES-256-GCM").put("iv", iv).put("ct", ct)
    }

    /** Decrypt each roster entry into a device for the UI; mark self by device id. */
    private fun onRoster(entries: List<WsClient.RosterEntry>) {
        val devices = entries.distinctBy { it.dev }.map { e ->
            var name = e.dev
            var platform = "?"
            val iv = e.iv
            val ct = e.ct
            if (!iv.isNullOrEmpty() && !ct.isNullOrEmpty()) {
                runCatching {
                    val p = JSONObject(String(crypto.open(iv, ct), Charsets.UTF_8))
                    name = p.optString("name", e.dev)
                    platform = p.optString("platform", "?")
                }
            }
            SyncStatus.Device(dev = e.dev, name = name, platform = platform, self = e.dev == dev)
        }
        SyncStatus.setDevices(devices)
    }

    private fun startForegroundNotification(status: String) {
        val pi = android.app.PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            android.app.PendingIntent.FLAG_IMMUTABLE,
        )
        val n: Notification = NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle("LinuxDrop")
            .setContentText("Clipboard sync — $status")
            .setSmallIcon(R.drawable.ic_sync)
            .setOngoing(true)
            .setContentIntent(pi)
            .build()
        val type = if (Build.VERSION.SDK_INT >= 30) ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE else 0
        ServiceCompat.startForeground(this, NOTIF_ID, n, type)
    }

    private fun createChannel() {
        val mgr = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        mgr.createNotificationChannel(
            NotificationChannel(CHANNEL_ID, "Sync", NotificationManager.IMPORTANCE_LOW),
        )
    }

    companion object {
        private const val TAG = "linuxDropSvc"
        private const val CHANNEL_ID = "linuxdrop.sync"
        private const val NOTIF_ID = 1
        private const val ACTION_SEND_FILE = "com.linuxdrop.app.SEND_FILE"
        private const val EXTRA_TO_DEV = "toDev"

        fun start(context: Context) {
            val i = Intent(context, SyncForegroundService::class.java)
            if (Build.VERSION.SDK_INT >= 26) context.startForegroundService(i) else context.startService(i)
        }

        fun stop(context: Context) {
            context.stopService(Intent(context, SyncForegroundService::class.java))
        }

        /** Ask the running service to send a file directly (P2P) to a peer device. */
        fun sendFile(context: Context, toDev: String, uri: Uri) {
            val i = Intent(context, SyncForegroundService::class.java)
                .setAction(ACTION_SEND_FILE)
                .putExtra(EXTRA_TO_DEV, toDev)
                .setData(uri)
                .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            if (Build.VERSION.SDK_INT >= 26) context.startForegroundService(i) else context.startService(i)
        }

        private fun sha256Hex(b: ByteArray): String =
            MessageDigest.getInstance("SHA-256").digest(b).joinToString("") { "%02x".format(it) }

        private fun sha256Hex(s: String): String = sha256Hex(s.toByteArray(Charsets.UTF_8))

        private fun randomId(): String =
            java.security.SecureRandom().let { r -> ByteArray(10).also { r.nextBytes(it) } }
                .joinToString("") { "%02x".format(it) }
    }
}
