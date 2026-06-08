package com.linuxdrop.app.tether

import android.content.Context
import android.util.Log
import com.linuxdrop.app.shizuku.ShizukuTether

/**
 * App-side orchestration seam for hotspot control. The BLE GATT server (Plan 2) calls
 * enable/disable/keepAlive here; this class hides the Shizuku wrapper and forwards the phone-side
 * safety auto-off signal. SSID/passphrase are passed in by the caller (Plan 2 derives them from the
 * LinuxDrop secret).
 */
class TetherController(context: Context) {
    private val shizuku = ShizukuTether(context)
    @Volatile private var onAutoOff: ((Int) -> Unit)? = null

    fun setOnAutoOff(cb: (Int) -> Unit) {
        onAutoOff = cb
        shizuku.setOnAutoOff { reason -> onAutoOff?.invoke(reason) }
    }

    fun enable(ssid: String, passphrase: String, onResult: (Int) -> Unit) {
        shizuku.enable(ssid, passphrase) { code ->
            Log.i(TAG, "enable -> ${TetherResult.label(code)}")
            onResult(code)
        }
    }

    fun disable(onResult: (Int) -> Unit = {}) =
        shizuku.disable { code -> Log.i(TAG, "disable -> ${TetherResult.label(code)}"); onResult(code) }

    /** Call periodically (≤ safety window) while the laptop wants the hotspot up. */
    fun keepAlive() = shizuku.keepAlive()

    fun release() = shizuku.unbind()

    companion object { private const val TAG = "linuxDropTether" }
}
