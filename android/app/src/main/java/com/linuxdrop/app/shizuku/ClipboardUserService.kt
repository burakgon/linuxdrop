package com.linuxdrop.app.shizuku

import android.content.ClipData
import android.content.Context
import android.content.IOnPrimaryClipChangedListener
import android.net.Uri
import android.os.IBinder
import android.os.ParcelFileDescriptor
import android.util.Log
import java.lang.reflect.Method

/**
 * Runs INSIDE the process Shizuku starts as the shell user (uid 2000). Because
 * the caller is the shell user and we pass package "com.android.shell" — which
 * holds READ_CLIPBOARD_IN_BACKGROUND — the platform ClipboardService lets us read
 * the clipboard AND receive change callbacks in the background (no focus, no
 * polling). See proto/PROTOCOL.md and the AOSP ClipboardService.clipboardAccessAllowed.
 *
 * We talk to android.content.IClipboard via reflection on the DEVICE's own
 * IClipboard$Stub (so binder transaction codes always match this OS build), and
 * pick method overloads by parameter type to absorb the signature differences
 * across Android 10..16 and OEM variants. This is the highest-risk code in the
 * project; it is the part that may need on-device tweaks (see README "M3").
 *
 * Images: read out as bytes (shell uid resolves the content uri) and streamed to
 * the app over a pipe to dodge the 1 MB binder limit; written back by handing the
 * shell our own content uri inside a ClipData.
 */
class ClipboardUserService() : IClipboardUserService.Stub() {

    @Volatile private var ctx: Context? = null

    // Shizuku instantiates with a Context; keep it for the ContentResolver (images).
    constructor(context: Context) : this() {
        ctx = context
    }

    private val pkg = "com.android.shell"
    private val userId = 0
    private val deviceId = 0

    @Volatile private var clipboard: Any? = null // android.content.IClipboard proxy
    private var listener: IOnPrimaryClipChangedListener? = null
    @Volatile private var callback: IClipboardCallback? = null

    override fun startWatching(cb: IClipboardCallback) {
        callback = cb
        val l = object : IOnPrimaryClipChangedListener.Stub() {
            override fun dispatchPrimaryClipChanged() {
                try {
                    dispatchChange()
                } catch (t: Throwable) {
                    Log.e(TAG, "read-on-change failed", t)
                }
            }
        }
        listener = l
        try {
            invoke("addPrimaryClipChangedListener", listener = l)
            Log.i(TAG, "watching clipboard as $pkg")
        } catch (t: Throwable) {
            Log.e(TAG, "addPrimaryClipChangedListener failed", t)
        }
    }

    override fun stopWatching() {
        listener?.let { runCatching { invoke("removePrimaryClipChangedListener", listener = it) } }
        listener = null
        callback = null
    }

    override fun setClipboard(text: String) {
        try {
            invoke("setPrimaryClip", clip = ClipData.newPlainText("LinuxDrop", text))
        } catch (t: Throwable) {
            Log.e(TAG, "setPrimaryClip failed", t)
        }
    }

    override fun setClipboardImage(clip: ClipData) {
        // The app built this ClipData around a content:// uri it owns; the shell
        // just puts it on the system clipboard.
        try {
            invoke("setPrimaryClip", clip = clip)
        } catch (t: Throwable) {
            Log.e(TAG, "setPrimaryClip(image) failed", t)
        }
    }

    override fun destroy() {
        stopWatching()
        System.exit(0)
    }

    /** Read the current clip: prefer text; otherwise stream an image uri's bytes. */
    private fun dispatchChange() {
        val clip = invoke("getPrimaryClip") as? ClipData ?: return
        if (clip.itemCount == 0) return
        // Skip content apps flagged sensitive (OTP fields, password managers).
        if (clip.description?.extras?.getBoolean("android.content.extra.IS_SENSITIVE", false) == true) {
            Log.i(TAG, "skipping sensitive clipboard")
            return
        }
        val item = clip.getItemAt(0)
        val text = item.text?.toString()
        if (!text.isNullOrEmpty()) {
            callback?.onClipboardText(text)
            return
        }
        item.uri?.let { streamImage(it) }
    }

    private fun streamImage(uri: Uri) {
        val cb = callback ?: return
        val resolver = ctx?.contentResolver ?: return
        val mime = resolver.getType(uri) ?: return
        if (!mime.startsWith("image/")) return
        val bytes = runCatching { resolver.openInputStream(uri)?.use { it.readBytes() } }.getOrNull()
        if (bytes == null || bytes.isEmpty()) return

        val pipe = ParcelFileDescriptor.createPipe()
        val read = pipe[0]
        val write = pipe[1]
        // Write on a worker so we never block if the reader is slow / the buffer fills.
        Thread {
            runCatching { ParcelFileDescriptor.AutoCloseOutputStream(write).use { it.write(bytes) } }
        }.start()
        runCatching { cb.onClipboardImage(read, mime, bytes.size.toLong()) }
        runCatching { read.close() } // binder dup'd it across the oneway call
    }

    private fun clipboard(): Any {
        clipboard?.let { return it }
        val sm = Class.forName("android.os.ServiceManager")
        val binder = sm.getMethod("getService", String::class.java).invoke(null, "clipboard") as IBinder
        val stub = Class.forName("android.content.IClipboard\$Stub")
        val c = stub.getMethod("asInterface", IBinder::class.java).invoke(null, binder)!!
        clipboard = c
        return c
    }

    private fun invoke(name: String, clip: ClipData? = null, listener: IOnPrimaryClipChangedListener? = null): Any? {
        val target = clipboard()
        val method = target.javaClass.methods.firstOrNull { it.name == name }
            ?: throw NoSuchMethodException("IClipboard.$name")
        return method.invoke(target, *buildArgs(method, clip, listener))
    }

    /** Build args by parameter type: ClipData→clip, listener→listener, 1st String→pkg
     *  (rest String→null attributionTag), 1st int→userId (rest int→deviceId). */
    private fun buildArgs(method: Method, clip: ClipData?, listener: IOnPrimaryClipChangedListener?): Array<Any?> {
        var strings = 0
        var ints = 0
        return method.parameterTypes.map { p ->
            when {
                p == ClipData::class.java -> clip
                IOnPrimaryClipChangedListener::class.java.isAssignableFrom(p) -> listener
                p == String::class.java -> if (strings++ == 0) pkg else null
                p == Int::class.javaPrimitiveType -> if (ints++ == 0) userId else deviceId
                else -> null
            }
        }.toTypedArray()
    }

    companion object {
        private const val TAG = "linuxDropClipSvc"
    }
}
