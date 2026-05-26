package com.bgnconnect.app.shizuku

import android.content.ClipData
import android.content.ComponentName
import android.content.Context
import android.content.ServiceConnection
import android.content.pm.PackageManager
import android.os.IBinder
import android.os.ParcelFileDescriptor
import android.util.Log
import androidx.core.content.FileProvider
import rikka.shizuku.Shizuku
import java.io.File

/**
 * App-process side: requests Shizuku permission, binds the shell-uid
 * [ClipboardUserService], and exposes background clipboard read (text + image
 * callbacks) and write. See proto/PROTOCOL.md §5-6.
 */
class ShizukuClipboard(context: Context) {

    fun interface OnText {
        fun onText(text: String)
    }

    fun interface OnImage {
        fun onImage(bytes: ByteArray, mime: String)
    }

    private val appContext = context.applicationContext

    private val userServiceArgs = Shizuku.UserServiceArgs(
        ComponentName(context.packageName, ClipboardUserService::class.java.name),
    ).daemon(false).processNameSuffix("clipsvc").debuggable(false).version(2)

    private val packageMgr = context.applicationContext.packageManager

    /** Whether the Shizuku app is installed (not necessarily running). */
    fun isInstalled(): Boolean = runCatching {
        packageMgr.getPackageInfo("moe.shizuku.privileged.api", 0)
        true
    }.getOrDefault(false)

    @Volatile private var service: IClipboardUserService? = null
    private var onText: OnText? = null
    private var onImage: OnImage? = null
    @Volatile private var wantWatch = false

    private val connection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName?, binder: IBinder?) {
            val svc = IClipboardUserService.Stub.asInterface(binder)
            service = svc
            Log.i(TAG, "user service connected")
            if (wantWatch) startWatchingInternal(svc)
        }

        override fun onServiceDisconnected(name: ComponentName?) {
            service = null
            Log.w(TAG, "user service disconnected")
        }
    }

    fun shizukuAvailable(): Boolean = runCatching { Shizuku.pingBinder() }.getOrDefault(false)

    fun permissionGranted(): Boolean =
        runCatching { Shizuku.checkSelfPermission() == PackageManager.PERMISSION_GRANTED }.getOrDefault(false)

    fun requestPermission(requestCode: Int) = Shizuku.requestPermission(requestCode)

    fun bind() = Shizuku.bindUserService(userServiceArgs, connection)

    fun unbind() = runCatching { Shizuku.unbindUserService(userServiceArgs, connection, true) }

    fun startWatching(onText: OnText, onImage: OnImage? = null) {
        this.onText = onText
        this.onImage = onImage
        wantWatch = true
        val svc = service
        if (svc != null) startWatchingInternal(svc) else bind()
    }

    private fun startWatchingInternal(svc: IClipboardUserService) {
        runCatching {
            svc.startWatching(object : IClipboardCallback.Stub() {
                override fun onClipboardText(text: String?) {
                    if (text != null) onText?.onText(text)
                }

                override fun onClipboardImage(pfd: ParcelFileDescriptor?, mime: String?, size: Long) {
                    pfd ?: return
                    val cb = onImage ?: run { runCatching { pfd.close() }; return }
                    // Drain the pipe off the binder thread; the UserService writes concurrently.
                    Thread {
                        val bytes = runCatching {
                            ParcelFileDescriptor.AutoCloseInputStream(pfd).use { it.readBytes() }
                        }.getOrNull()
                        if (bytes != null && bytes.isNotEmpty()) cb.onImage(bytes, mime ?: "image/png")
                    }.start()
                }
            })
        }.onFailure { Log.e(TAG, "startWatching failed", it) }
    }

    fun setClipboard(text: String) {
        runCatching { service?.setClipboard(text) }.onFailure { Log.e(TAG, "setClipboard failed", it) }
    }

    /** Write an image to the clipboard: stage it as our own content uri, then let the shell post it. */
    fun setImage(bytes: ByteArray, mime: String) {
        val svc = service ?: return
        runCatching {
            val dir = File(appContext.cacheDir, "clipimg").apply { mkdirs() }
            val ext = when {
                mime.contains("png") -> "png"
                mime.contains("jpeg") || mime.contains("jpg") -> "jpg"
                mime.contains("gif") -> "gif"
                mime.contains("webp") -> "webp"
                else -> "img"
            }
            val file = File(dir, "clip.$ext")
            file.writeBytes(bytes)
            val uri = FileProvider.getUriForFile(appContext, "${appContext.packageName}.fileprovider", file)
            val clip = ClipData.newUri(appContext.contentResolver, "bgnconnect", uri)
            svc.setClipboardImage(clip)
        }.onFailure { Log.e(TAG, "setImage failed", it) }
    }

    companion object {
        private const val TAG = "bgnShizuku"
    }
}
