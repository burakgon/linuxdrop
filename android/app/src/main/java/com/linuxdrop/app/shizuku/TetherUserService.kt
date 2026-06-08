package com.linuxdrop.app.shizuku

import android.content.Context
import android.net.IIntResultListener
import android.os.IBinder
import android.util.Log
import com.linuxdrop.app.tether.TetherResult
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * Runs INSIDE the process Shizuku starts as the shell user (uid 2000). The shell
 * package (com.android.shell) holds TETHER_PRIVILEGED and OVERRIDE_WIFI_CONFIG, so
 * from here we may pin a SoftAp config and start Wi-Fi tethering — the same
 * reflection-against-the-device's-own-classes trick ClipboardUserService uses for
 * IClipboard. `cmd wifi start-softap` is NOT used: it is gated to ROOT_UID only.
 *
 * Highest-risk code (the "M3" class): hidden-API method signatures vary across
 * Android versions and OEM skins. Heavy logging is intentional — this is a spike.
 */
class TetherUserService() : ITetherUserService.Stub() {

    @Volatile private var ctx: Context? = null
    @Volatile private var callback: ITetherCallback? = null
    @Volatile private var lastKeepAlive = 0L
    @Volatile private var hotspotOn = false
    private val watchdog = java.util.concurrent.Executors.newSingleThreadScheduledExecutor()

    // Shizuku instantiates with a Context; keep it for getSystemService().
    constructor(context: Context) : this() {
        ctx = context
        // Safety net: if the laptop stops sending keepalives (walked away / BLE lost / app died),
        // turn the hotspot off ourselves so it can never be stranded on, draining data/battery.
        watchdog.scheduleWithFixedDelay({
            try {
                if (hotspotOn && System.currentTimeMillis() - lastKeepAlive > SAFETY_WINDOW_MS) {
                    Log.w(TAG, "safety auto-off: no keepalive within ${SAFETY_WINDOW_MS}ms")
                    disableHotspot()
                    runCatching { callback?.onAutoOff(1) }
                }
            } catch (t: Throwable) {
                Log.e(TAG, "watchdog", t)
            }
        }, WATCH_PERIOD_MS, WATCH_PERIOD_MS, TimeUnit.MILLISECONDS)
    }

    override fun enableHotspot(ssid: String, passphrase: String): Int {
        val context = ctx ?: return TetherResult.ERR_NO_CONTEXT
        return try {
            applySoftApConfig(ssid, passphrase)
            val r = startWifiTethering(context)
            if (r == TetherResult.OK) {
                lastKeepAlive = System.currentTimeMillis()
                hotspotOn = true
            }
            r
        } catch (t: Throwable) {
            Log.e(TAG, "enableHotspot reflection failed", t)
            TetherResult.ERR_REFLECTION
        }
    }

    override fun disableHotspot(): Int {
        val context = ctx ?: return TetherResult.ERR_NO_CONTEXT
        return try {
            val connector = getConnector(context) ?: return TetherResult.ERR_REFLECTION
            val tetheringWifi = Class.forName("android.net.TetheringManager").getField("TETHERING_WIFI").getInt(null)
            val listener = resultListener { code -> Log.i(TAG, "stopTethering onResult=$code") }
            val m = connector.javaClass.methods.first { it.name == "stopTethering" }
            m.invoke(connector, *argsByType(m, tetherType = tetheringWifi, listener = listener))
            hotspotOn = false
            Log.i(TAG, "stopTethering(WIFI) invoked (connector, pkg=$SHELL_PKG)")
            TetherResult.OK
        } catch (t: Throwable) {
            Log.e(TAG, "disableHotspot reflection failed", t)
            TetherResult.ERR_REFLECTION
        }
    }

    /** Pin the SoftAp config by calling IWifiManager directly and passing the shell package, so the
     *  service's checkPackage(callingUid=2000, packageName) passes. The high-level WifiManager
     *  attaches the app package (getOpPackageName) → "does not belong to 2000". Same trick as
     *  ClipboardUserService passing pkg="com.android.shell" to IClipboard. */
    private fun applySoftApConfig(ssid: String, passphrase: String) {
        val cfgCls = Class.forName("android.net.wifi.SoftApConfiguration")
        val builderCls = Class.forName("android.net.wifi.SoftApConfiguration\$Builder")
        val builder = builderCls.getConstructor().newInstance()
        builderCls.getMethod("setSsid", String::class.java).invoke(builder, ssid)
        // WPA2-PSK. SECURITY_TYPE_WPA2_PSK == 1; read it reflectively to be safe.
        val wpa2 = cfgCls.getField("SECURITY_TYPE_WPA2_PSK").getInt(null)
        builderCls.getMethod("setPassphrase", String::class.java, Int::class.javaPrimitiveType)
            .invoke(builder, passphrase, wpa2)
        val config = builderCls.getMethod("build").invoke(builder)

        val sm = Class.forName("android.os.ServiceManager")
        val binder = sm.getMethod("getService", String::class.java).invoke(null, "wifi") as IBinder
        val wifiSvc = Class.forName("android.net.wifi.IWifiManager\$Stub")
            .getMethod("asInterface", IBinder::class.java).invoke(null, binder)!!
        val m = wifiSvc.javaClass.methods.first { it.name == "setSoftApConfiguration" }
        m.invoke(wifiSvc, *argsByType(m, softAp = config))
        Log.i(TAG, "setSoftApConfiguration ok (IWifiManager pkg=$SHELL_PKG): ssid=$ssid")
    }

    /** The ITetheringConnector binder lives inside TetheringManager; reflect it out so we can call it
     *  directly (passing the shell package) instead of through the manager (which attaches the app pkg). */
    private fun getConnector(context: Context): Any? {
        val tm = context.getSystemService("tethering") ?: return null
        return try {
            tm.javaClass.getDeclaredField("mConnector").apply { isAccessible = true }.get(tm)
        } catch (e: NoSuchFieldException) {
            tm.javaClass.declaredFields.firstOrNull { it.type.name.endsWith("ITetheringConnector") }
                ?.apply { isAccessible = true }?.get(tm)
        }
    }

    /** A REAL IIntResultListener binder. A java.lang.reflect.Proxy returns null from asBinder() and
     *  cannot cross binder, which is why the connector callback silently never fired. The compile-time
     *  mirror lives at aidl/android/net/IIntResultListener.aidl; at runtime the framework's own class is
     *  used (boot classloader shadows ours), so the binder descriptor/transaction codes match. */
    private fun resultListener(onResult: (Int) -> Unit): IIntResultListener =
        object : IIntResultListener.Stub() {
            override fun onResult(resultCode: Int) = onResult(resultCode)
        }

    /** Fill a binder method's args by parameter type; 1st String = shell pkg, extra Strings = null. */
    private fun argsByType(
        m: java.lang.reflect.Method,
        softAp: Any? = null,
        reqParcel: Any? = null,
        listener: Any? = null,
        tetherType: Int? = null,
    ): Array<Any?> {
        var strings = 0
        return m.parameterTypes.map { p ->
            when {
                p.name.endsWith("SoftApConfiguration") -> softAp
                p.name.endsWith("TetheringRequestParcel") -> reqParcel
                p.name.endsWith("IIntResultListener") -> listener
                p == Int::class.javaPrimitiveType -> tetherType
                p == String::class.java -> if (strings++ == 0) SHELL_PKG else null
                else -> null
            }
        }.toTypedArray()
    }

    /** Start Wi-Fi tethering by calling ITetheringConnector directly with the shell package, so the
     *  service's permission/package check (uid 2000 + "com.android.shell") passes. Going through the
     *  high-level TetheringManager attaches the app package and returns error 14 (no-change-permission). */
    private fun startWifiTethering(context: Context): Int {
        val connector = getConnector(context) ?: return TetherResult.ERR_REFLECTION
        val tetheringWifi = Class.forName("android.net.TetheringManager").getField("TETHERING_WIFI").getInt(null)

        // Build a TetheringRequest, then extract the parcel the connector expects.
        val reqBuilderCls = Class.forName("android.net.TetheringManager\$TetheringRequest\$Builder")
        val reqBuilder = reqBuilderCls.getConstructor(Int::class.javaPrimitiveType).newInstance(tetheringWifi)
        val request = reqBuilderCls.getMethod("build").invoke(reqBuilder)
        val parcel = runCatching { request.javaClass.getMethod("getParcel").invoke(request) }
            .getOrElse { request.javaClass.getDeclaredMethod("getParcel").apply { isAccessible = true }.invoke(request) }

        val latch = CountDownLatch(1)
        val result = intArrayOf(TetherResult.ERR_TIMEOUT)
        val listener = resultListener { code ->
            result[0] = if (code == 0) TetherResult.OK else TetherResult.ERR_TETHER_FAILED
            if (code != 0) Log.w(TAG, "tethering onResult error=$code")
            latch.countDown()
        }
        val m = connector.javaClass.methods.first { it.name == "startTethering" }
        m.invoke(connector, *argsByType(m, reqParcel = parcel, listener = listener))
        latch.await(12, TimeUnit.SECONDS)
        Log.i(TAG, "startTethering result=${result[0]}")
        return result[0]
    }

    override fun keepAlive() {
        lastKeepAlive = System.currentTimeMillis()
    }

    override fun setCallback(cb: ITetherCallback?) {
        callback = cb
    }

    override fun destroy() {
        runCatching { watchdog.shutdownNow() }
        System.exit(0)
    }

    companion object {
        private const val TAG = "linuxDropTether"
        private const val SHELL_PKG = "com.android.shell"
        private const val SAFETY_WINDOW_MS = 180_000L  // no keepalive for this long → safety auto-off
        private const val WATCH_PERIOD_MS = 30_000L
    }
}
