package com.linuxdrop.app.net

import android.content.Context
import android.util.Log
import org.json.JSONObject
import org.webrtc.Camera2Enumerator
import org.webrtc.CameraVideoCapturer
import org.webrtc.DataChannel
import org.webrtc.DefaultVideoDecoderFactory
import org.webrtc.DefaultVideoEncoderFactory
import org.webrtc.EglBase
import org.webrtc.IceCandidate
import org.webrtc.MediaConstraints
import org.webrtc.MediaStream
import org.webrtc.MediaStreamTrack
import org.webrtc.PeerConnection
import org.webrtc.PeerConnectionFactory
import org.webrtc.RtpReceiver
import org.webrtc.RtpTransceiver
import org.webrtc.SdpObserver
import org.webrtc.SessionDescription
import org.webrtc.SurfaceTextureHelper
import org.webrtc.VideoSource
import org.webrtc.VideoTrack

/**
 * One webcam streaming session, driven by the Linux side. Linux sends a
 * `webcam-request` → [handleRequest] starts the camera, builds the PC,
 * sends the offer. Then [handleSignal] processes the answer + trickle
 * candidates. The video track is sendonly; we transmit, the laptop receives.
 *
 * Caller is responsible for keeping a foreground service (with
 * `foregroundServiceType="camera"`) alive while this session is active.
 */
class WebcamSession(
    private val context: Context,
    private val signalSink: (toDev: String, payload: JSONObject) -> Unit,
    private val onEnded: (reason: String) -> Unit,
) {
    var sessionId: String = ""
        private set
    var peerDev: String = ""
        private set

    private var pc: PeerConnection? = null
    private var factory: PeerConnectionFactory? = null
    private var capturer: CameraVideoCapturer? = null
    private var videoSource: VideoSource? = null
    private var videoTrack: VideoTrack? = null
    private var surfaceHelper: SurfaceTextureHelper? = null
    private val pendingCandidates = mutableListOf<IceCandidate>()
    private var remoteSet = false
    @Volatile private var ended = false

    companion object {
        private const val TAG = "linuxDropWebcam"
        @Volatile private var pcfInitialized = false
        // EglBase is process-scoped: WebRTC's native engine threads live as
        // long as the process. We create one and reuse it across sessions —
        // disposing it per-session also tore down the OkHttp WebSocket
        // receive dispatcher on at least one OPPO build, so the *next*
        // session couldn't receive signals.
        @Volatile private var sharedEglBase: EglBase? = null

        @Synchronized
        private fun ensurePcfInit(ctx: Context) {
            if (pcfInitialized) return
            PeerConnectionFactory.initialize(
                PeerConnectionFactory.InitializationOptions.builder(ctx).createInitializationOptions()
            )
            pcfInitialized = true
        }

        @Synchronized
        private fun egl(): EglBase {
            sharedEglBase?.let { return it }
            val e = EglBase.create()
            sharedEglBase = e
            return e
        }
    }

    fun handleRequest(
        fromDev: String,
        sessionId: String,
        w: Int,
        h: Int,
        fps: Int,
        camera: String,
        @Suppress("UNUSED_PARAMETER") codecPref: String,
    ) {
        this.peerDev = fromDev
        this.sessionId = sessionId
        try {
            ensurePcfInit(context.applicationContext)
            initPeerConnection()
            attachCamera(camera, w, h, fps)
            createAndSendOffer()
        } catch (t: Throwable) {
            Log.w(TAG, "handleRequest failed", t)
            val reason = when {
                t.message?.contains("permission", ignoreCase = true) == true -> "no-permission"
                t.message?.contains("camera", ignoreCase = true) == true -> "no-camera"
                else -> "init-failed"
            }
            emitStop(reason)
            cleanupAndNotify(reason)
        }
    }

    fun handleSignal(payload: JSONObject) {
        if (ended) return
        when (payload.optString("kind")) {
            "webcam-answer" -> setRemoteAnswer(payload.optString("sdp"))
            "webcam-candidate" -> addCandidate(
                IceCandidate(
                    payload.optString("sdpMid"),
                    payload.optInt("sdpMLineIndex"),
                    payload.optString("candidate"),
                )
            )
            "webcam-stop" -> {
                Log.i(TAG, "peer requested stop: ${payload.optString("reason")}")
                cleanupAndNotify("peer-stop")
            }
        }
    }

    fun stop(reason: String = "user") {
        if (ended) return
        emitStop(reason)
        cleanupAndNotify(reason)
    }

    private fun initPeerConnection() {
        val f = PeerConnectionFactory.builder()
            .setVideoEncoderFactory(DefaultVideoEncoderFactory(egl().eglBaseContext, true, true))
            .setVideoDecoderFactory(DefaultVideoDecoderFactory(egl().eglBaseContext))
            .createPeerConnectionFactory()
        factory = f

        val rtcConfig = PeerConnection.RTCConfiguration(emptyList()).apply {
            sdpSemantics = PeerConnection.SdpSemantics.UNIFIED_PLAN
        }
        pc = f.createPeerConnection(rtcConfig, object : PeerConnection.Observer {
            override fun onIceCandidate(c: IceCandidate) {
                if (ended) return
                val msg = JSONObject().apply {
                    put("kind", "webcam-candidate")
                    put("session", sessionId)
                    put("candidate", c.sdp)
                    put("sdpMid", c.sdpMid)
                    put("sdpMLineIndex", c.sdpMLineIndex)
                }
                signalSink(peerDev, msg)
            }
            override fun onIceConnectionChange(state: PeerConnection.IceConnectionState) {
                Log.d(TAG, "ICE state: $state")
                if (state == PeerConnection.IceConnectionState.FAILED) {
                    emitStop("ice-failed"); cleanupAndNotify("ice-failed")
                } else if (state == PeerConnection.IceConnectionState.CLOSED) {
                    cleanupAndNotify("ice-closed")
                }
            }
            override fun onConnectionChange(newState: PeerConnection.PeerConnectionState) {
                Log.d(TAG, "PC state: $newState")
            }
            override fun onAddStream(s: MediaStream?) {}
            override fun onRemoveStream(s: MediaStream?) {}
            override fun onDataChannel(d: DataChannel?) {}
            override fun onRenegotiationNeeded() {}
            override fun onSignalingChange(s: PeerConnection.SignalingState?) {}
            override fun onIceConnectionReceivingChange(b: Boolean) {}
            override fun onIceGatheringChange(s: PeerConnection.IceGatheringState?) {}
            override fun onIceCandidatesRemoved(cs: Array<out IceCandidate>?) {}
            override fun onAddTrack(r: RtpReceiver?, ms: Array<out MediaStream>?) {}
            override fun onTrack(t: RtpTransceiver?) {}
        }) ?: throw IllegalStateException("createPeerConnection returned null")
    }

    private fun attachCamera(which: String, w: Int, h: Int, fps: Int) {
        val f = factory ?: throw IllegalStateException("factory not initialized")
        val enumerator = Camera2Enumerator(context)
        val deviceName = enumerator.deviceNames.firstOrNull { name ->
            when (which) {
                "front" -> enumerator.isFrontFacing(name)
                else -> enumerator.isBackFacing(name)
            }
        } ?: enumerator.deviceNames.firstOrNull()
            ?: throw IllegalStateException("no camera available")

        capturer = enumerator.createCapturer(deviceName, object : CameraVideoCapturer.CameraEventsHandler {
            override fun onCameraError(p0: String?) { Log.w(TAG, "cam error: $p0") }
            override fun onCameraDisconnected() {}
            override fun onCameraFreezed(p0: String?) {}
            override fun onCameraOpening(p0: String?) {}
            override fun onFirstFrameAvailable() {}
            override fun onCameraClosed() {}
        }) ?: throw IllegalStateException("failed to create camera capturer for $deviceName")

        surfaceHelper = SurfaceTextureHelper.create("WebcamCaptureThread", egl().eglBaseContext)
        videoSource = f.createVideoSource(false)
        capturer!!.initialize(surfaceHelper, context, videoSource!!.capturerObserver)
        capturer!!.startCapture(w, h, fps)

        videoTrack = f.createVideoTrack("linuxdrop-video", videoSource)
        val transceiver = pc!!.addTransceiver(videoTrack!!,
            RtpTransceiver.RtpTransceiverInit(RtpTransceiver.RtpTransceiverDirection.SEND_ONLY))
        // Prefer H.264, then H.265 — H.264 is the universal floor; H.265 is opportunistic.
        try {
            val caps = f.getRtpSenderCapabilities(MediaStreamTrack.MediaType.MEDIA_TYPE_VIDEO)
            val ordered = caps.codecs
                .filter { it.mimeType == "video/H264" || it.mimeType == "video/H265" }
                .sortedBy { if (it.mimeType == "video/H264") 0 else 1 }
            if (ordered.isNotEmpty()) {
                transceiver.setCodecPreferences(ordered)
            }
        } catch (t: Throwable) {
            Log.w(TAG, "setCodecPreferences", t)
        }
        // Low-latency encoder tuning. WebRTC's default encoder ramps bitrate up
        // gradually over a few seconds (the "rate control startup" phase) and
        // queues 1-2 GOPs while doing so → visible 1-2s lag on every motion at
        // the start of a session. Pinning a high min/max bitrate skips the
        // ramp-up; networkPriority HIGH tells GCC to prefer this stream.
        try {
            val sender = transceiver.sender
            val params = sender.parameters
            params.encodings?.forEach { e ->
                e.maxBitrateBps = if (w * h > 1280 * 720) 6_000_000 else 3_500_000
                e.minBitrateBps = if (w * h > 1280 * 720) 3_000_000 else 1_500_000
                e.maxFramerate = fps
                e.networkPriority = 3 // HIGH
                e.bitratePriority = 4.0
            }
            sender.parameters = params
        } catch (t: Throwable) {
            Log.w(TAG, "setEncoderParams", t)
        }
    }

    private fun createAndSendOffer() {
        pc!!.createOffer(object : SdpObserver {
            override fun onCreateSuccess(sdp: SessionDescription) {
                pc!!.setLocalDescription(object : SdpObserver {
                    override fun onCreateSuccess(p0: SessionDescription?) {}
                    override fun onSetSuccess() {
                        val msg = JSONObject().apply {
                            put("kind", "webcam-offer")
                            put("session", sessionId)
                            put("sdp", sdp.description)
                        }
                        signalSink(peerDev, msg)
                    }
                    override fun onCreateFailure(err: String?) {}
                    override fun onSetFailure(err: String?) {
                        Log.w(TAG, "setLocalDescription: $err")
                        emitStop("set-local-failed"); cleanupAndNotify("set-local-failed")
                    }
                }, sdp)
            }
            override fun onSetSuccess() {}
            override fun onCreateFailure(err: String?) {
                Log.w(TAG, "createOffer: $err")
                emitStop("create-offer-failed"); cleanupAndNotify("create-offer-failed")
            }
            override fun onSetFailure(err: String?) {}
        }, MediaConstraints())
    }

    private fun setRemoteAnswer(sdp: String) {
        pc!!.setRemoteDescription(object : SdpObserver {
            override fun onCreateSuccess(p0: SessionDescription?) {}
            override fun onSetSuccess() {
                remoteSet = true
                pendingCandidates.forEach { pc!!.addIceCandidate(it) }
                pendingCandidates.clear()
            }
            override fun onCreateFailure(err: String?) {}
            override fun onSetFailure(err: String?) {
                Log.w(TAG, "setRemoteDescription: $err")
                emitStop("set-remote-failed"); cleanupAndNotify("set-remote-failed")
            }
        }, SessionDescription(SessionDescription.Type.ANSWER, sdp))
    }

    private fun addCandidate(c: IceCandidate) {
        if (!remoteSet) {
            pendingCandidates.add(c); return
        }
        pc?.addIceCandidate(c)
    }

    private fun emitStop(reason: String) {
        val msg = JSONObject().apply {
            put("kind", "webcam-stop")
            put("session", sessionId)
            put("reason", reason)
        }
        signalSink(peerDev, msg)
    }

    @Synchronized
    private fun cleanupAndNotify(reason: String) {
        if (ended) return
        ended = true
        // Snapshot the native handles, then yield the caller IMMEDIATELY by
        // invoking onEnded BEFORE the (potentially slow / hang-prone) disposes.
        // Camera2/WebRTC disposal can take >1s and on some OPPO builds the
        // call-stack actually hangs, blocking whichever thread was driving the
        // session and preventing the next webcam-request from being processed.
        // Disposing in a background thread protects the signal pipeline.
        val capturerSnap = capturer
        val videoSourceSnap = videoSource
        val videoTrackSnap = videoTrack
        val surfaceHelperSnap = surfaceHelper
        val pcSnap = pc
        capturer = null; videoSource = null; videoTrack = null
        surfaceHelper = null; pc = null; factory = null
        onEnded(reason)
        Thread({
            runCatching { capturerSnap?.stopCapture() }
            runCatching { capturerSnap?.dispose() }
            runCatching { videoSourceSnap?.dispose() }
            runCatching { videoTrackSnap?.dispose() }
            runCatching { surfaceHelperSnap?.dispose() }
            runCatching { pcSnap?.close() }
        }, "webcam-dispose").start()
    }
}
