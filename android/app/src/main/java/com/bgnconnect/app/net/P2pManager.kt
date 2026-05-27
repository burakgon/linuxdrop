package com.bgnconnect.app.net

import android.content.ContentValues
import android.content.Context
import android.net.Uri
import android.os.Environment
import android.provider.MediaStore
import android.provider.OpenableColumns
import android.util.Log
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONObject
import org.webrtc.DataChannel
import org.webrtc.IceCandidate
import org.webrtc.MediaConstraints
import org.webrtc.MediaStream
import org.webrtc.PeerConnection
import org.webrtc.PeerConnectionFactory
import org.webrtc.RtpReceiver
import org.webrtc.SdpObserver
import org.webrtc.SessionDescription
import java.io.OutputStream
import java.nio.ByteBuffer
import java.security.MessageDigest
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Direct peer-to-peer file transfer over a WebRTC DataChannel. File bytes go
 * straight to the peer (LAN-direct, or hole-punched); the relay carries only the
 * E2E-encrypted SDP/ICE signaling. Mirrors linux/internal/p2p: trickle ICE
 * (offer/answer sent immediately, candidates streamed + queued until remote SDP is
 * set), message-framed transfer (head → binary chunks → done{sha256}), receiver
 * replies ok/err. See PROTOCOL.md §7.
 */
class P2pManager(
    context: Context,
    relayBaseUrl: String,
    private val dev: String,
    private val sendSignal: (toDev: String, payload: ByteArray) -> Unit,
    private val onReceived: (name: String, size: Long, uri: Uri?) -> Unit = { _, _, _ -> },
    private val onSent: (name: String, size: Long, ok: Boolean) -> Unit = { _, _, _ -> },
) {
    private val appContext = context.applicationContext
    private val httpBase = run {
        val b = relayBaseUrl.trimEnd('/')
        when {
            b.startsWith("wss://") -> "https://" + b.removePrefix("wss://")
            b.startsWith("ws://") -> "http://" + b.removePrefix("ws://")
            else -> b
        }
    }
    private val http = OkHttpClient()
    private val factory: PeerConnectionFactory
    private val peers = ConcurrentHashMap<String, Session>()

    init {
        ensureFactoryInit(appContext)
        factory = PeerConnectionFactory.builder().createPeerConnectionFactory()
    }

    /** One peer connection with trickle-ICE candidate queueing. */
    private inner class Session(val pc: PeerConnection) {
        private val lock = Any()
        private var remoteSet = false
        private val pending = ArrayList<IceCandidate>()

        fun addCandidate(c: IceCandidate) {
            synchronized(lock) {
                if (!remoteSet) {
                    pending.add(c)
                    return
                }
            }
            runCatching { pc.addIceCandidate(c) }
        }

        fun setRemote(desc: SessionDescription, onDone: () -> Unit = {}) {
            pc.setRemoteDescription(object : SimpleSdp() {
                override fun onSetSuccess() {
                    val flush: List<IceCandidate>
                    synchronized(lock) {
                        remoteSet = true
                        flush = ArrayList(pending)
                        pending.clear()
                    }
                    flush.forEach { runCatching { pc.addIceCandidate(it) } }
                    onDone()
                }
            }, desc)
        }
    }

    // ---- public API ------------------------------------------------------------

    fun sendFile(toDev: String, uri: Uri) {
        val (name, size) = queryMeta(uri)
        val mime = appContext.contentResolver.getType(uri) ?: "application/octet-stream"
        val config = PeerConnection.RTCConfiguration(iceServers())
        val pc = factory.createPeerConnection(config, object : PcObserver() {
            override fun onIceCandidate(c: IceCandidate?) { c?.let { sendCandidate(toDev, it) } }
        }) ?: run { onSent(name, size, false); return }
        peers[toDev] = Session(pc)

        val dc = pc.createDataChannel(CHANNEL, DataChannel.Init())
        dc.registerObserver(object : DataChannel.Observer {
            override fun onBufferedAmountChange(prev: Long) {}
            override fun onStateChange() {
                if (dc.state() == DataChannel.State.OPEN) Thread { streamFile(dc, uri, name, size, mime) }.start()
            }
            override fun onMessage(buffer: DataChannel.Buffer) {
                if (buffer.binary) return
                val o = readJson(buffer) ?: return
                when (o.optString("t")) {
                    "ok" -> { onSent(name, size, true); closePeer(toDev) }
                    "err" -> { onSent(name, size, false); closePeer(toDev) }
                }
            }
        })

        pc.createOffer(object : SimpleSdp() {
            override fun onCreateSuccess(sdp: SessionDescription) {
                pc.setLocalDescription(SimpleSdp(), sdp)
                sendSig(toDev, "offer", sdp.description) // trickle: send immediately
            }
        }, MediaConstraints())
    }

    fun onSignal(fromDev: String, payload: ByteArray) {
        val sig = runCatching { JSONObject(String(payload, Charsets.UTF_8)) }.getOrNull() ?: return
        when (sig.optString("kind")) {
            "offer" -> handleOffer(fromDev, sig.optString("sdp"))
            "answer" -> peers[fromDev]?.setRemote(SessionDescription(SessionDescription.Type.ANSWER, sig.optString("sdp")))
            "candidate" -> peers[fromDev]?.addCandidate(
                IceCandidate(sig.optString("sdpMid"), sig.optInt("sdpMLineIndex"), sig.optString("candidate")),
            )
        }
    }

    fun close() {
        peers.values.forEach { runCatching { it.pc.close() } }
        peers.clear()
    }

    // ---- receive (answerer) ----------------------------------------------------

    private fun handleOffer(fromDev: String, sdp: String) {
        val config = PeerConnection.RTCConfiguration(iceServers())
        val pc = factory.createPeerConnection(config, object : PcObserver() {
            override fun onIceCandidate(c: IceCandidate?) { c?.let { sendCandidate(fromDev, it) } }
            override fun onDataChannel(dc: DataChannel) = receive(fromDev, dc)
        }) ?: return
        val sess = Session(pc)
        peers[fromDev] = sess

        sess.setRemote(SessionDescription(SessionDescription.Type.OFFER, sdp)) {
            pc.createAnswer(object : SimpleSdp() {
                override fun onCreateSuccess(answer: SessionDescription) {
                    pc.setLocalDescription(SimpleSdp(), answer)
                    sendSig(fromDev, "answer", answer.description) // trickle: send immediately
                }
            }, MediaConstraints())
        }
    }

    private fun receive(fromDev: String, dc: DataChannel) {
        dc.registerObserver(object : DataChannel.Observer {
            var out: OutputStream? = null
            var uri: Uri? = null
            var md = MessageDigest.getInstance("SHA-256")
            var expected = 0L
            var received = 0L
            var name = "file"

            override fun onBufferedAmountChange(prev: Long) {}
            override fun onStateChange() {}

            override fun onMessage(buffer: DataChannel.Buffer) {
                if (!buffer.binary) {
                    val o = readJson(buffer) ?: return
                    when (o.optString("t")) {
                        "head" -> startReceive(o)
                        "done" -> finishReceive(o)
                    }
                    return
                }
                val o = out ?: return
                val data = ByteArray(buffer.data.remaining()).also { buffer.data.get(it) }
                runCatching {
                    o.write(data); md.update(data); received += data.size
                }.onFailure { fail(dc, "write error") }
            }

            private fun startReceive(o: JSONObject) {
                name = o.optString("name", "file")
                expected = o.optLong("size", -1)
                val mime = o.optString("mime", "application/octet-stream")
                md = MessageDigest.getInstance("SHA-256")
                received = 0
                runCatching {
                    val values = ContentValues().apply {
                        put(MediaStore.Downloads.DISPLAY_NAME, name)
                        put(MediaStore.Downloads.MIME_TYPE, mime)
                        put(MediaStore.Downloads.IS_PENDING, 1)
                        put(MediaStore.Downloads.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS)
                    }
                    val u = appContext.contentResolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
                    uri = u
                    out = u?.let { appContext.contentResolver.openOutputStream(it) }
                }.onFailure { fail(dc, "cannot open output") }
                Log.i(TAG, "receiving $name ($expected bytes)")
            }

            private fun finishReceive(o: JSONObject) {
                val u = uri ?: return
                runCatching { out?.flush(); out?.close() }
                out = null
                val sha = o.optString("sha256")
                val got = md.digest().joinToString("") { "%02x".format(it) }
                val okSize = expected < 0 || received == expected
                if (okSize && (sha.isEmpty() || sha == got)) {
                    val v = ContentValues().apply { put(MediaStore.Downloads.IS_PENDING, 0) }
                    appContext.contentResolver.update(u, v, null, null)
                    dc.send(textBuffer(JSONObject().put("t", "ok")))
                    Log.i(TAG, "saved $name")
                    onReceived(name, if (expected > 0) expected else received, u)
                } else {
                    appContext.contentResolver.delete(u, null, null)
                    dc.send(textBuffer(JSONObject().put("t", "err").put("msg", "size/hash mismatch")))
                }
                uri = null
            }

            private fun fail(dc: DataChannel, msg: String) {
                runCatching { out?.close() }
                uri?.let { appContext.contentResolver.delete(it, null, null) }
                out = null; uri = null
                dc.send(textBuffer(JSONObject().put("t", "err").put("msg", msg)))
            }
        })
    }

    // ---- send (offerer) --------------------------------------------------------

    private fun streamFile(dc: DataChannel, uri: Uri, name: String, size: Long, mime: String) {
        runCatching {
            dc.send(textBuffer(JSONObject().put("t", "head").put("name", name).put("size", size).put("mime", mime)))
            val md = MessageDigest.getInstance("SHA-256")
            appContext.contentResolver.openInputStream(uri)!!.use { input ->
                val buf = ByteArray(CHUNK)
                while (true) {
                    val n = input.read(buf)
                    if (n < 0) break
                    while (dc.bufferedAmount() > MAX_BUFFERED) Thread.sleep(5)
                    dc.send(DataChannel.Buffer(ByteBuffer.wrap(buf.copyOf(n)), true))
                    md.update(buf, 0, n)
                }
            }
            val sha = md.digest().joinToString("") { "%02x".format(it) }
            dc.send(textBuffer(JSONObject().put("t", "done").put("sha256", sha)))
            Log.i(TAG, "streamed $name ($size bytes)")
        }.onFailure {
            Log.e(TAG, "stream failed", it)
            onSent(name, size, false)
        }
    }

    // ---- helpers ---------------------------------------------------------------

    private fun sendSig(toDev: String, kind: String, sdp: String) {
        val payload = JSONObject().put("kind", kind).put("sdp", sdp).put("origin", dev)
        sendSignal(toDev, payload.toString().toByteArray(Charsets.UTF_8))
    }

    private fun sendCandidate(toDev: String, c: IceCandidate) {
        val payload = JSONObject()
            .put("kind", "candidate")
            .put("candidate", c.sdp)
            .put("sdpMid", c.sdpMid)
            .put("sdpMLineIndex", c.sdpMLineIndex)
            .put("origin", dev)
        sendSignal(toDev, payload.toString().toByteArray(Charsets.UTF_8))
    }

    private fun closePeer(dev: String) {
        peers.remove(dev)?.let { runCatching { it.pc.close() } }
    }

    private fun iceServers(): List<PeerConnection.IceServer> {
        val fallback = listOf(PeerConnection.IceServer.builder("stun:stun.l.google.com:19302").createIceServer())
        return runCatching {
            http.newCall(Request.Builder().url("$httpBase/ice").build()).execute().use { resp ->
                if (!resp.isSuccessful) return fallback
                val arr = JSONObject(resp.body?.string() ?: "").optJSONArray("iceServers") ?: return fallback
                val out = ArrayList<PeerConnection.IceServer>()
                for (i in 0 until arr.length()) {
                    val o = arr.getJSONObject(i)
                    val urls = ArrayList<String>()
                    when (val u = o.opt("urls")) {
                        is String -> urls.add(u)
                        is org.json.JSONArray -> for (j in 0 until u.length()) urls.add(u.getString(j))
                    }
                    if (urls.isEmpty()) continue
                    val b = PeerConnection.IceServer.builder(urls)
                    o.optString("username").takeIf { it.isNotEmpty() }?.let { b.setUsername(it) }
                    o.optString("credential").takeIf { it.isNotEmpty() }?.let { b.setPassword(it) }
                    out.add(b.createIceServer())
                }
                if (out.isEmpty()) fallback else out
            }
        }.getOrDefault(fallback)
    }

    private fun queryMeta(uri: Uri): Pair<String, Long> {
        var name = "file"
        var size = -1L
        runCatching {
            appContext.contentResolver.query(uri, null, null, null, null)?.use { c ->
                val ni = c.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                val si = c.getColumnIndex(OpenableColumns.SIZE)
                if (c.moveToFirst()) {
                    if (ni >= 0) name = c.getString(ni) ?: name
                    if (si >= 0 && !c.isNull(si)) size = c.getLong(si)
                }
            }
        }
        return name to size
    }

    private fun readJson(buffer: DataChannel.Buffer): JSONObject? {
        val data = ByteArray(buffer.data.remaining()).also { buffer.data.get(it) }
        return runCatching { JSONObject(String(data, Charsets.UTF_8)) }.getOrNull()
    }

    private fun textBuffer(o: JSONObject) =
        DataChannel.Buffer(ByteBuffer.wrap(o.toString().toByteArray(Charsets.UTF_8)), false)

    /** PeerConnection.Observer with no-op defaults; override what you need. */
    private open class PcObserver : PeerConnection.Observer {
        override fun onSignalingChange(s: PeerConnection.SignalingState?) {}
        override fun onIceConnectionChange(s: PeerConnection.IceConnectionState?) {}
        override fun onIceConnectionReceivingChange(receiving: Boolean) {}
        override fun onIceGatheringChange(s: PeerConnection.IceGatheringState?) {}
        override fun onIceCandidate(c: IceCandidate?) {}
        override fun onIceCandidatesRemoved(c: Array<out IceCandidate>?) {}
        override fun onAddStream(s: MediaStream?) {}
        override fun onRemoveStream(s: MediaStream?) {}
        override fun onDataChannel(dc: DataChannel) {}
        override fun onRenegotiationNeeded() {}
        override fun onAddTrack(r: RtpReceiver?, s: Array<out MediaStream>?) {}
    }

    /** SdpObserver with no-op defaults; override onCreateSuccess/onSetSuccess. */
    private open class SimpleSdp : SdpObserver {
        override fun onCreateSuccess(sdp: SessionDescription) {}
        override fun onSetSuccess() {}
        override fun onCreateFailure(error: String?) { Log.w(TAG, "sdp create: $error") }
        override fun onSetFailure(error: String?) { Log.w(TAG, "sdp set: $error") }
    }

    companion object {
        private const val TAG = "bgnP2p"
        private const val CHANNEL = "bgn-file"
        private const val CHUNK = 16 * 1024
        private const val MAX_BUFFERED = 1L shl 20
        private val factoryInited = AtomicBoolean(false)

        private fun ensureFactoryInit(ctx: Context) {
            if (factoryInited.compareAndSet(false, true)) {
                PeerConnectionFactory.initialize(
                    PeerConnectionFactory.InitializationOptions.builder(ctx).createInitializationOptions(),
                )
            }
        }
    }
}
