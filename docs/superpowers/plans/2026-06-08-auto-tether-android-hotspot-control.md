# Auto-tether — Android hotspot control (Phase 0–1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the Android app the ability to turn its own Wi-Fi hotspot on/off on command — with a fixed SSID/passphrase — through a Shizuku shell-uid UserService, plus a safety auto-off so the hotspot can never be stranded on. No BLE yet; driven by a debug button.

**Architecture:** Mirror the existing `ClipboardUserService` Shizuku pattern. A new `TetherUserService` runs as shell uid (2000) and calls the hidden framework APIs by reflection — `WifiManager.setSoftApConfiguration(...)` (permission `OVERRIDE_WIFI_CONFIG`, held by shell) to pin a fixed SSID/PSK, then `TetheringManager.startTethering(TETHERING_WIFI)` (permission `TETHER_PRIVILEGED`, held by shell) to bring it up. An app-process `ShizukuTether` wrapper binds the service (exactly like `ShizukuClipboard`), and a `TetherController` exposes `enable/disable/keepAlive` with keepalive-based safety auto-off. **`cmd wifi start-softap` is NOT used — it is root-only.**

**Tech Stack:** Kotlin, Android (Compose app), Shizuku (`rikka.shizuku`), AIDL, Java reflection against hidden `android.net.wifi` / `android.net` APIs.

**Scope note:** This is plan 1 of a staged, gate-driven series (see the spec at `docs/superpowers/specs/2026-06-08-phone-tether-on-no-internet-design.md`). **Task 1 is the Phase-0 feasibility GATE** — stop and reassess if the hotspot does not come up on the target device. BLE signaling, secret-derived SSID/PSK, the Linux daemon, and the auto-trigger orchestrator are later plans.

---

## File structure

| File | Responsibility | Create/Modify |
|---|---|---|
| `android/app/src/main/aidl/com/linuxdrop/app/shizuku/ITetherUserService.aidl` | Shell-uid service interface (enable/disable/keepAlive/destroy) | Create |
| `android/app/src/main/aidl/com/linuxdrop/app/shizuku/ITetherCallback.aidl` | Async callback (safety auto-off notification) | Create |
| `android/app/src/main/java/com/linuxdrop/app/shizuku/TetherUserService.kt` | Runs as shell uid; reflection hotspot on/off + safety timer | Create |
| `android/app/src/main/java/com/linuxdrop/app/shizuku/ShizukuTether.kt` | App-process side: bind the UserService, expose enable/disable/keepAlive | Create |
| `android/app/src/main/java/com/linuxdrop/app/tether/TetherResult.kt` | Result-code constants shared by service + wrapper | Create |
| `android/app/src/main/java/com/linuxdrop/app/tether/TetherController.kt` | App-side seam the future BLE layer calls; owns wrapper + keepalive | Create |
| `android/app/src/main/java/com/linuxdrop/app/ui/SettingsScreen.kt` | Add a DEBUG-only "Tether spike" button | Modify |

Conventions to follow (from the existing code): explicit AIDL transaction ids with `void destroy() = 16777114;` (see `IClipboardUserService.aidl`); the UserService has a `constructor(context: Context)` that Shizuku invokes (see `ClipboardUserService`); the wrapper builds `Shizuku.UserServiceArgs(...).daemon(false).processNameSuffix(...).debuggable(false).version(n)` and binds via `Shizuku.bindUserService` (see `ShizukuClipboard`). The app already requests Shizuku permission for clipboard — the same binder is reused; **no new app manifest permission is required** (tethering privilege belongs to the shell uid, not our app).

---

## Task 1: Phase-0 feasibility spike (GATE) — toggle a fixed hotspot via reflection

**Files:**
- Create: `android/app/src/main/aidl/com/linuxdrop/app/shizuku/ITetherUserService.aidl`
- Create: `android/app/src/main/java/com/linuxdrop/app/shizuku/TetherUserService.kt`
- Create: `android/app/src/main/java/com/linuxdrop/app/shizuku/ShizukuTether.kt`
- Modify: `android/app/src/main/java/com/linuxdrop/app/ui/SettingsScreen.kt`

> Reflection against hidden framework APIs cannot be unit-tested off-device; the verification loop is build → install → tap → observe → read logcat. That is by design — this task exists to prove the mechanism on real hardware before anything is built on top of it.

- [ ] **Step 1: Create the AIDL interface**

`android/app/src/main/aidl/com/linuxdrop/app/shizuku/ITetherUserService.aidl`:
```aidl
package com.linuxdrop.app.shizuku;

// Implemented by the process Shizuku starts as the shell user (uid 2000).
// `destroy` uses the transaction id Shizuku reserves for tearing down a UserService.
interface ITetherUserService {
    void destroy() = 16777114;
    // Returns a TetherResult code (0 = OK). Pins a fixed SoftAp config then starts Wi-Fi tethering.
    int enableHotspot(String ssid, String passphrase) = 1;
    int disableHotspot() = 2;
}
```

- [ ] **Step 2: Create the shell-uid `TetherUserService`**

`android/app/src/main/java/com/linuxdrop/app/shizuku/TetherUserService.kt`:
```kotlin
package com.linuxdrop.app.shizuku

import android.content.Context
import android.net.wifi.WifiManager
import android.util.Log
import java.lang.reflect.Proxy
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/**
 * Runs INSIDE the process Shizuku starts as the shell user (uid 2000). The shell
 * package (com.android.shell) holds TETHER_PRIVILEGED and OVERRIDE_WIFI_CONFIG, so
 * from here we may pin a SoftAp config and start Wi-Fi tethering — the same
 * reflection-against-the-device's-own-classes trick ClipboardUserService uses for
 * IClipboard. `cmd wifi start-softap` is NOT used: it is gated to ROOT_UID only.
 *
 * Highest-risk code (the M3 class): hidden-API method signatures vary across
 * Android versions and OEM skins. Heavy logging is intentional — Task 1 is a spike.
 */
class TetherUserService() : ITetherUserService.Stub() {

    @Volatile private var ctx: Context? = null

    constructor(context: Context) : this() {
        ctx = context
    }

    override fun enableHotspot(ssid: String, passphrase: String): Int {
        val context = ctx ?: return TETHER_ERR_NO_CONTEXT
        return try {
            applySoftApConfig(context, ssid, passphrase)
            startWifiTethering(context)
        } catch (t: Throwable) {
            Log.e(TAG, "enableHotspot reflection failed", t)
            TETHER_ERR_REFLECTION
        }
    }

    override fun disableHotspot(): Int {
        val context = ctx ?: return TETHER_ERR_NO_CONTEXT
        return try {
            val tm = context.getSystemService("tethering")
                ?: return TETHER_ERR_REFLECTION
            val tmCls = Class.forName("android.net.TetheringManager")
            val tetheringWifi = tmCls.getField("TETHERING_WIFI").getInt(null)
            tmCls.getMethod("stopTethering", Int::class.javaPrimitiveType)
                .invoke(tm, tetheringWifi)
            Log.i(TAG, "stopTethering(WIFI) invoked")
            TETHER_OK
        } catch (t: Throwable) {
            Log.e(TAG, "disableHotspot reflection failed", t)
            TETHER_ERR_REFLECTION
        }
    }

    /** WifiManager.setSoftApConfiguration(SoftApConfiguration) — permission OVERRIDE_WIFI_CONFIG (shell). */
    private fun applySoftApConfig(context: Context, ssid: String, passphrase: String) {
        val cfgCls = Class.forName("android.net.wifi.SoftApConfiguration")
        val builderCls = Class.forName("android.net.wifi.SoftApConfiguration\$Builder")
        val builder = builderCls.getConstructor().newInstance()
        builderCls.getMethod("setSsid", String::class.java).invoke(builder, ssid)
        // WPA2-PSK. SECURITY_TYPE_WPA2_PSK == 1; read it reflectively to be safe.
        val wpa2 = cfgCls.getField("SECURITY_TYPE_WPA2_PSK").getInt(null)
        builderCls.getMethod("setPassphrase", String::class.java, Int::class.javaPrimitiveType)
            .invoke(builder, passphrase, wpa2)
        val config = builderCls.getMethod("build").invoke(builder)

        val wifi = context.getSystemService(Context.WIFI_SERVICE) as WifiManager
        WifiManager::class.java.getMethod("setSoftApConfiguration", cfgCls).invoke(wifi, config)
        Log.i(TAG, "setSoftApConfiguration ok: ssid=$ssid")
    }

    /** TetheringManager.startTethering(TetheringRequest, Executor, StartTetheringCallback) — TETHER_PRIVILEGED (shell). */
    private fun startWifiTethering(context: Context): Int {
        val tm = context.getSystemService("tethering") ?: return TETHER_ERR_REFLECTION
        val tmCls = Class.forName("android.net.TetheringManager")
        val tetheringWifi = tmCls.getField("TETHERING_WIFI").getInt(null)

        val reqBuilderCls = Class.forName("android.net.TetheringManager\$TetheringRequest\$Builder")
        val reqBuilder = reqBuilderCls.getConstructor(Int::class.javaPrimitiveType).newInstance(tetheringWifi)
        val request = reqBuilderCls.getMethod("build").invoke(reqBuilder)
        val reqCls = Class.forName("android.net.TetheringManager\$TetheringRequest")

        val cbCls = Class.forName("android.net.TetheringManager\$StartTetheringCallback")
        val latch = CountDownLatch(1)
        val result = intArrayOf(TETHER_ERR_TIMEOUT)
        val cbProxy = Proxy.newProxyInstance(cbCls.classLoader, arrayOf(cbCls)) { _, method, args ->
            when (method.name) {
                "onTetheringStarted" -> { result[0] = TETHER_OK; latch.countDown() }
                "onTetheringFailed" -> {
                    result[0] = TETHER_ERR_TETHER_FAILED
                    Log.w(TAG, "onTetheringFailed: ${args?.getOrNull(0)}")
                    latch.countDown()
                }
            }
            null
        }
        val executor = Executors.newSingleThreadExecutor()
        tmCls.getMethod("startTethering", reqCls, java.util.concurrent.Executor::class.java, cbCls)
            .invoke(tm, request, executor, cbProxy)
        latch.await(12, TimeUnit.SECONDS)
        Log.i(TAG, "startTethering result=${result[0]}")
        return result[0]
    }

    override fun destroy() {
        System.exit(0)
    }

    companion object {
        private const val TAG = "linuxDropTether"
    }
}
```

- [ ] **Step 3: Create the result-code constants** (used by Step 2; keep in the same package for the spike)

Add to the TOP of `TetherUserService.kt` (above the class), so `enableHotspot` compiles:
```kotlin
const val TETHER_OK = 0
const val TETHER_ERR_NO_CONTEXT = 1
const val TETHER_ERR_REFLECTION = 2
const val TETHER_ERR_TETHER_FAILED = 3
const val TETHER_ERR_TIMEOUT = 4
```
(These move to `tether/TetherResult.kt` in Task 2 — for now they live here so the spike is self-contained.)

- [ ] **Step 4: Create the app-process wrapper `ShizukuTether`** (mirrors `ShizukuClipboard`)

`android/app/src/main/java/com/linuxdrop/app/shizuku/ShizukuTether.kt`:
```kotlin
package com.linuxdrop.app.shizuku

import android.content.ComponentName
import android.content.Context
import android.content.ServiceConnection
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
        runCatching { Shizuku.checkSelfPermission() == android.content.pm.PackageManager.PERMISSION_GRANTED }
            .getOrDefault(false)

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
```

- [ ] **Step 5: Add a DEBUG-only spike button to Settings**

In `android/app/src/main/java/com/linuxdrop/app/ui/SettingsScreen.kt`, inside the existing settings `Column`/list, add (importing `com.linuxdrop.app.BuildConfig`, `com.linuxdrop.app.shizuku.ShizukuTether`, `androidx.compose.material3.Button`, `androidx.compose.material3.Text`, `android.widget.Toast`, `androidx.compose.ui.platform.LocalContext`, `androidx.compose.runtime.remember`):
```kotlin
if (BuildConfig.DEBUG) {
    val ctx = LocalContext.current
    val tether = remember { ShizukuTether(ctx) }
    Button(onClick = {
        tether.enable("LD-spike", "spikepass12") { code ->
            android.os.Handler(android.os.Looper.getMainLooper()).post {
                Toast.makeText(ctx, "tether enable = $code", Toast.LENGTH_LONG).show()
            }
        }
    }) { Text("Spike: hotspot ON") }
    Button(onClick = {
        tether.disable { code ->
            android.os.Handler(android.os.Looper.getMainLooper()).post {
                Toast.makeText(ctx, "tether disable = $code", Toast.LENGTH_LONG).show()
            }
        }
    }) { Text("Spike: hotspot OFF") }
}
```

- [ ] **Step 6: Build and install the debug APK**

Run: `cd android && ./gradlew installDebug`
Expected: `BUILD SUCCESSFUL`, app installed. (For a clean hermetic build use `bash scripts/build-apk.sh`, but the gradle install loop is faster for the spike.)

- [ ] **Step 7: On-device verification (THE GATE)**

1. Open the app, ensure Shizuku is running and the app has Shizuku permission (the clipboard onboarding already establishes this).
2. Make sure the phone's mobile data is ON and any existing hotspot is OFF.
3. Tap **"Spike: hotspot ON"**. Watch logcat: `adb logcat -s linuxDropTether`.
4. From a second device (or your laptop), look for a Wi-Fi network named **`LD-spike`** and join it with passphrase `spikepass12`.
5. Tap **"Spike: hotspot OFF"** and confirm the network disappears.

Expected success: Toast `tether enable = 0`; logcat shows `setSoftApConfiguration ok` then `startTethering result=0`; the `LD-spike` SSID is visible and joinable.

**STOP-AND-VERIFY checkpoint:**
- ✅ If the hotspot comes up and toggles off → the gate is green. Proceed to Task 2.
- ❌ If it fails, capture the exact logcat exception and classify it before doing anything else:
  - `NoSuchMethodException` / `NoSuchFieldException` on `SoftApConfiguration$Builder.setPassphrase`, `setSsid`, or `TetheringManager...` → this device's hidden-API signature differs. Adjust the reflected method name/parameter types to match this OS build (the same way `ClipboardUserService.buildArgs` absorbs IClipboard overload differences) and re-run Step 6.
  - `onTetheringFailed` with a non-zero error, or `result=3` → likely a **carrier tethering-entitlement** block. Note it; the mitigation (`WRITE_SECURE_SETTINGS` → `settings put global tether_dun_required 0`) is a deliberate later decision, not part of this gate.
  - `SecurityException` → re-confirm Shizuku is running as shell (not a degraded mode) and the UserService actually started in the shell process (`adb logcat` for the Shizuku process name `*:tethersvc`).
  - **Do not proceed past this checkpoint until ON/OFF works on the target device.** This is the whole point of Phase 0.

- [ ] **Step 8: Commit the spike**

```bash
git add android/app/src/main/aidl/com/linuxdrop/app/shizuku/ITetherUserService.aidl \
        android/app/src/main/java/com/linuxdrop/app/shizuku/TetherUserService.kt \
        android/app/src/main/java/com/linuxdrop/app/shizuku/ShizukuTether.kt \
        android/app/src/main/java/com/linuxdrop/app/ui/SettingsScreen.kt
git commit -m "spike(tether): toggle Wi-Fi hotspot via Shizuku shell-uid reflection (Phase 0)"
```

---

## Task 2: Promote result codes to a shared file

**Files:**
- Create: `android/app/src/main/java/com/linuxdrop/app/tether/TetherResult.kt`
- Modify: `android/app/src/main/java/com/linuxdrop/app/shizuku/TetherUserService.kt`

- [ ] **Step 1: Create `TetherResult.kt`**

`android/app/src/main/java/com/linuxdrop/app/tether/TetherResult.kt`:
```kotlin
package com.linuxdrop.app.tether

/** Result codes returned across the Shizuku tether binder. 0 = success. */
object TetherResult {
    const val OK = 0
    const val ERR_NO_CONTEXT = 1
    const val ERR_REFLECTION = 2
    const val ERR_TETHER_FAILED = 3
    const val ERR_TIMEOUT = 4

    fun label(code: Int): String = when (code) {
        OK -> "ok"
        ERR_NO_CONTEXT -> "no-context"
        ERR_REFLECTION -> "reflection-failed"
        ERR_TETHER_FAILED -> "tether-failed (entitlement?)"
        ERR_TIMEOUT -> "timed-out"
        else -> "unknown($code)"
    }
}
```

- [ ] **Step 2: Replace the top-level constants in `TetherUserService.kt` with references**

Delete the five `const val TETHER_*` declarations added in Task 1 Step 3. Add `import com.linuxdrop.app.tether.TetherResult` and replace each usage: `TETHER_OK` → `TetherResult.OK`, `TETHER_ERR_NO_CONTEXT` → `TetherResult.ERR_NO_CONTEXT`, `TETHER_ERR_REFLECTION` → `TetherResult.ERR_REFLECTION`, `TETHER_ERR_TETHER_FAILED` → `TetherResult.ERR_TETHER_FAILED`, `TETHER_ERR_TIMEOUT` → `TetherResult.ERR_TIMEOUT`.

In `ShizukuTether.kt`, replace the two `TETHER_ERR_REFLECTION` fallbacks in `enable`/`disable` with `TetherResult.ERR_REFLECTION` and add the import.

- [ ] **Step 3: Build to verify nothing broke**

Run: `cd android && ./gradlew assembleDebug`
Expected: `BUILD SUCCESSFUL`. (Re-tap both spike buttons after install to confirm codes still read `0`.)

- [ ] **Step 4: Commit**

```bash
git add android/app/src/main/java/com/linuxdrop/app/tether/TetherResult.kt \
        android/app/src/main/java/com/linuxdrop/app/shizuku/TetherUserService.kt \
        android/app/src/main/java/com/linuxdrop/app/shizuku/ShizukuTether.kt
git commit -m "refactor(tether): extract TetherResult codes to a shared file"
```

---

## Task 3: Safety auto-off (keepalive-based) + callback

The hotspot must never be stranded on. The laptop will (in a later plan) ping a keepalive while it wants the hotspot; if pings stop for the safety window, the phone turns tethering off itself and tells the app.

**Files:**
- Create: `android/app/src/main/aidl/com/linuxdrop/app/shizuku/ITetherCallback.aidl`
- Modify: `android/app/src/main/aidl/com/linuxdrop/app/shizuku/ITetherUserService.aidl`
- Modify: `android/app/src/main/java/com/linuxdrop/app/shizuku/TetherUserService.kt`
- Modify: `android/app/src/main/java/com/linuxdrop/app/shizuku/ShizukuTether.kt`

- [ ] **Step 1: Create the callback AIDL**

`android/app/src/main/aidl/com/linuxdrop/app/shizuku/ITetherCallback.aidl`:
```aidl
package com.linuxdrop.app.shizuku;

interface ITetherCallback {
    // reason: 1 = no keepalive within the safety window.
    void onAutoOff(int reason) = 1;
}
```

- [ ] **Step 2: Extend the service AIDL** — add `keepAlive` and `setCallback`

In `ITetherUserService.aidl`, add `import com.linuxdrop.app.shizuku.ITetherCallback;` and these methods:
```aidl
    void keepAlive() = 3;
    void setCallback(ITetherCallback cb) = 4;
```

- [ ] **Step 3: Implement the safety timer in `TetherUserService.kt`**

Add fields and logic (imports: `java.util.concurrent.ScheduledExecutorService`, `java.util.concurrent.Executors`, `java.util.concurrent.TimeUnit`):
```kotlin
    @Volatile private var callback: ITetherCallback? = null
    @Volatile private var lastKeepAlive = 0L
    @Volatile private var hotspotOn = false
    private val watchdog: ScheduledExecutorService = Executors.newSingleThreadScheduledExecutor()

    override fun setCallback(cb: ITetherCallback?) { callback = cb }

    override fun keepAlive() { lastKeepAlive = System.currentTimeMillis() }
```
At the end of a successful `enableHotspot` (right before `return TetherResult.OK` in `startWifiTethering`, when `result[0] == TetherResult.OK`), arm the watchdog:
```kotlin
        if (result[0] == TetherResult.OK) {
            lastKeepAlive = System.currentTimeMillis()
            hotspotOn = true
        }
```
Schedule the watchdog once, in the `constructor(context)` body:
```kotlin
        watchdog.scheduleWithFixedDelay({
            try {
                if (hotspotOn && System.currentTimeMillis() - lastKeepAlive > SAFETY_WINDOW_MS) {
                    Log.w(TAG, "safety auto-off: no keepalive for ${SAFETY_WINDOW_MS}ms")
                    disableHotspot()
                    hotspotOn = false
                    runCatching { callback?.onAutoOff(1) }
                }
            } catch (t: Throwable) { Log.e(TAG, "watchdog", t) }
        }, WATCH_PERIOD_MS, WATCH_PERIOD_MS, TimeUnit.MILLISECONDS)
```
In `disableHotspot`, set `hotspotOn = false` on the success path. Add to `companion object`:
```kotlin
        private const val SAFETY_WINDOW_MS = 180_000L  // 3 min without keepalive → auto-off
        private const val WATCH_PERIOD_MS = 30_000L
```

- [ ] **Step 4: Expose keepAlive/callback in `ShizukuTether.kt`**

Add:
```kotlin
    fun keepAlive() = runCatching { service?.keepAlive() }

    fun setOnAutoOff(onAutoOff: (Int) -> Unit) = withService { svc ->
        runCatching {
            svc.setCallback(object : ITetherCallback.Stub() {
                override fun onAutoOff(reason: Int) = onAutoOff(reason)
            })
        }
    }
```

- [ ] **Step 5: Build and verify**

Run: `cd android && ./gradlew assembleDebug`
Expected: `BUILD SUCCESSFUL`.

On-device sanity: tap "hotspot ON", do NOT send keepalives, and confirm the hotspot turns itself off ~3 minutes later (logcat: `safety auto-off`). For a faster check, temporarily set `SAFETY_WINDOW_MS = 20_000L`, verify, then restore `180_000L`.

- [ ] **Step 6: Commit**

```bash
git add android/app/src/main/aidl/com/linuxdrop/app/shizuku/ITetherCallback.aidl \
        android/app/src/main/aidl/com/linuxdrop/app/shizuku/ITetherUserService.aidl \
        android/app/src/main/java/com/linuxdrop/app/shizuku/TetherUserService.kt \
        android/app/src/main/java/com/linuxdrop/app/shizuku/ShizukuTether.kt
git commit -m "feat(tether): phone-side safety auto-off after keepalive gap"
```

---

## Task 4: `TetherController` seam + DEBUG-guard the spike UI

Create the single app-side entry point the future BLE layer will call, so Plan 2 plugs into a stable surface instead of touching `ShizukuTether` directly.

**Files:**
- Create: `android/app/src/main/java/com/linuxdrop/app/tether/TetherController.kt`
- Modify: `android/app/src/main/java/com/linuxdrop/app/ui/SettingsScreen.kt`

- [ ] **Step 1: Create `TetherController.kt`**

`android/app/src/main/java/com/linuxdrop/app/tether/TetherController.kt`:
```kotlin
package com.linuxdrop.app.tether

import android.content.Context
import android.util.Log
import com.linuxdrop.app.shizuku.ShizukuTether

/**
 * App-side orchestration seam for hotspot control. The BLE GATT server (Plan 2)
 * calls enable/disable/keepAlive here; this class hides the Shizuku wrapper and
 * forwards the phone-side safety auto-off signal. SSID/passphrase are passed in by
 * the caller (Plan 2 derives them from the LinuxDrop secret).
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
```

- [ ] **Step 2: Point the DEBUG spike buttons at `TetherController`**

In `SettingsScreen.kt`, change the `remember { ShizukuTether(ctx) }` to `remember { TetherController(ctx) }` (import `com.linuxdrop.app.tether.TetherController`, drop the `ShizukuTether` import). The `enable("LD-spike", "spikepass12") { code -> ... }` and `disable { code -> ... }` calls are unchanged (same signatures). Keep both buttons inside the existing `if (BuildConfig.DEBUG)` block.

- [ ] **Step 3: Build and verify the seam still toggles the hotspot**

Run: `cd android && ./gradlew installDebug`
Expected: `BUILD SUCCESSFUL`. Re-tap both buttons; Toasts still read `0`, hotspot still toggles. logcat now also shows `enable -> ok` / `disable -> ok`.

- [ ] **Step 4: Commit**

```bash
git add android/app/src/main/java/com/linuxdrop/app/tether/TetherController.kt \
        android/app/src/main/java/com/linuxdrop/app/ui/SettingsScreen.kt
git commit -m "feat(tether): TetherController seam for the future BLE layer"
```

---

## End state & handoff to Plan 2

After Task 4 the phone can, on command, bring up a fixed-SSID/passphrase Wi-Fi hotspot via Shizuku, report a structured result code, and auto-off after a keepalive gap — all behind a stable `TetherController` surface, exercised by a DEBUG-only button. **The Phase-0 feasibility gate (Task 1 Step 7) is cleared.**

Plan 2 (next) adds: the BLE GATT peripheral inside `SyncForegroundService`, AEAD command auth + replay protection, and the secret-derived SSID/PSK/BLE-key (HKDF, co-pinned with the Go side in `proto/gen-test-vectors.ts` + `crypto-test-vectors.json`) — calling exactly the `TetherController.enable/disable/keepAlive` surface built here. Then Plan 3 builds the Linux daemon (BLE central, Wi-Fi connector, connectivity monitor, orchestrator); Plan 4 the tray/CLI UX.

## Self-review (against the spec)

- **Spec §3 feasibility recipe** (`setSoftApConfiguration` + `TetheringManager.startTethering`, not `cmd wifi`) → Task 1 Steps 2. ✅
- **Spec §10 Phase 0 GATE** (on-device spike, stop-if-fail) → Task 1 Step 7 STOP-AND-VERIFY. ✅
- **Spec §10 Phase 1** (`TetherUserService` enable/disable/status + safety auto-off, behind a debug button) → Tasks 1–4. ✅
- **Spec §7 phone-side safety auto-off** (auto-off on no keepalive) → Task 3. ✅
- **Spec §3 residual risks** (OEM signature variance, carrier entitlement) → Task 1 Step 7 classification guidance. ✅
- **Deferred to later plans (explicit, not gaps):** BLE GATT + AEAD auth (Plan 2), secret-derived SSID/PSK + crypto vectors (Plan 2), Linux daemon (Plan 3), tray/CLI + per-SSID opt-out + connectivity probe/orchestrator (Plans 3–4). These are out of Plan 1's scope by design (gate-first, BLE wire-format co-designed with Linux).
- **Placeholder scan:** none — every step has concrete code/commands. The reflection in Task 1 is intentionally a starting implementation whose signatures the on-device gate confirms or adjusts (documented, not a placeholder).
- **Type consistency:** `TetherResult.*` codes (Task 2) used uniformly; `TetherController.enable/disable/keepAlive/setOnAutoOff` signatures match `ShizukuTether` and the AIDL across Tasks 1–4. ✅
