package com.linuxdrop.app.shizuku

import android.content.ComponentName
import android.content.Context
import android.content.ServiceConnection
import android.content.pm.PackageManager
import android.os.IBinder
import android.util.Log
import rikka.shizuku.Shizuku

/** App-process side: binds the shell-uid [TetherUserService] and calls hotspot on/off. */
class ShizukuTether(context: Context) {

    private val userServiceArgs = Shizuku.UserServiceArgs(
        ComponentName(context.packageName, TetherUserService::class.java.name),
    ).daemon(false).processNameSuffix("tethersvc").debuggable(false).version(1)

    @Volatile private var service: ITetherUserService? = null
    @Volatile private var pending: ((ITetherUserService) -> Unit)? = null

    private val connection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName?, binder: IBinder?) {
            val svc = ITetherUserService.Stub.asInterface(binder)
            service = svc
            Log.i(TAG, "tether user service connected")
            pending?.let { it(svc); pending = null }
        }

        override fun onServiceDisconnected(name: ComponentName?) {
            service = null
            Log.w(TAG, "tether user service disconnected")
        }
    }

    fun permissionGranted(): Boolean =
        runCatching { Shizuku.checkSelfPermission() == PackageManager.PERMISSION_GRANTED }.getOrDefault(false)

    fun bind() = Shizuku.bindUserService(userServiceArgs, connection)
    fun unbind() = runCatching { Shizuku.unbindUserService(userServiceArgs, connection, true) }

    /** Enable the hotspot; runs [onResult] with a TETHER_* code once the service answers. */
    fun enable(ssid: String, passphrase: String, onResult: (Int) -> Unit) =
        withService { onResult(runCatching { it.enableHotspot(ssid, passphrase) }.getOrDefault(TETHER_ERR_REFLECTION)) }

    fun disable(onResult: (Int) -> Unit) =
        withService { onResult(runCatching { it.disableHotspot() }.getOrDefault(TETHER_ERR_REFLECTION)) }

    private fun withService(block: (ITetherUserService) -> Unit) {
        val svc = service
        if (svc != null) block(svc) else { pending = block; bind() }
    }

    companion object { private const val TAG = "linuxDropTether" }
}
