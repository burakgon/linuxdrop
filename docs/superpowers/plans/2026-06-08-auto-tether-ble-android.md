# Auto-tether — BLE protocol + Android GATT peripheral (Plan 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the phone advertise an authenticated, replay-protected BLE GATT service that, on an `ENABLE`/`DISABLE`/`KEEPALIVE` command sealed with a secret-derived key, drives `TetherController` (Plan 1) — and pin the secret→{SSID, PSK, BLE key} derivations as cross-language test vectors.

**Architecture:** A new `TetherGattServer` (BLE peripheral) is hosted inside the existing `SyncForegroundService` (already a `FOREGROUND_SERVICE_CONNECTED_DEVICE`). The laptop (BLE central, Plan 3) reads a per-connection random nonce, then writes AEAD-sealed commands; the phone verifies the seal + a monotonic sequence (replay protection) using a key derived from the LinuxDrop secret, and calls `TetherController.enable/disable/keepAlive`. No OS Bluetooth bonding — the app-layer AEAD is the trust boundary, exactly like the rest of LinuxDrop. SSID/PSK/BLE-key are derived offline from the secret so both ends agree without exchanging them.

**Tech Stack:** Kotlin, Android BLE (`BluetoothGattServer`, `BluetoothLeAdvertiser`), the project's `LinuxDropCrypto` (AES-256-GCM + HKDF-SHA256), AIDL-free (pure in-process), Bun/TS vector generator, JUnit (JVM unit tests).

**Scope:** This is plan 2 of the gated series (spec: `docs/superpowers/specs/2026-06-08-phone-tether-on-no-internet-design.md`; Plan 1 done — Android hotspot control). The Linux BLE **central** + Wi-Fi connector + orchestrator are **Plan 3** and consume the protocol/derivations defined here. UX is Plan 4.

---

## Reference: the tether BLE protocol (pin this exactly; Plan 3's Go side must match)

**Derivations** (HKDF-SHA256, RFC 5869; same single-block style as `LinuxDropCrypto.deriveKey`):
- `K_ble   = HKDF(ikm=secret, salt="linuxdrop/tether/v1", info="ble-aead-key", len=32)` — AES-256-GCM key for BLE frames.
- `ssid    = "LD-" + hex(HKDF(secret, "linuxdrop/tether/v1", "softap-ssid", len=4))` → `LD-` + 8 hex chars.
- `psk     = hex(HKDF(secret, "linuxdrop/tether/v1", "softap-psk", len=12))` → 24 hex chars (valid WPA2 passphrase).

**GATT service** (128-bit UUIDs, fixed):
- Service:        `e3a9f5c0-1d2b-4e3a-9c8d-0a1b2c3d4e5f`
- `nonce`  (READ):   `e3a9f5c1-1d2b-4e3a-9c8d-0a1b2c3d4e5f` — returns the current 16-byte per-connection session nonce.
- `command`(WRITE):  `e3a9f5c2-1d2b-4e3a-9c8d-0a1b2c3d4e5f` — an AEAD frame (below).
- `status` (NOTIFY): `e3a9f5c3-1d2b-4e3a-9c8d-0a1b2c3d4e5f` — an AEAD frame the phone pushes after each command (needs a standard CCCD descriptor `00002902-0000-1000-8000-00805f9b34fb`).

**AEAD frame** = `LinuxDropCrypto.sealBlob` output keyed by `K_ble`: `iv(12) || ciphertext || tag(16)` (random IV per frame).
- **command** plaintext = `sessionNonce(16) || seq(4, big-endian) || opcode(1)`; opcodes `ENABLE=1`, `DISABLE=2`, `KEEPALIVE=3`.
- **status**  plaintext = `opcode(1) || resultCode(1)` (resultCode is a `TetherResult` value).

**Replay protection:** the phone issues a fresh random `sessionNonce` per BLE connection (exposed via `nonce`). A command is accepted only if (a) the seal opens with `K_ble`, (b) the embedded `sessionNonce` equals the connection's current one, and (c) `seq` is strictly greater than the last accepted seq for this connection. A wrong secret fails the GCM tag → rejected.

---

## File structure

| File | Responsibility | Create/Modify |
|---|---|---|
| `proto/gen-test-vectors.ts` | Add the three tether derivations to the generator | Modify |
| `proto/crypto-test-vectors.json` | Regenerated output (committed) | Modify (generated) |
| `proto/PROTOCOL.md` | Document the tether BLE service + derivations | Modify |
| `android/app/src/main/java/com/linuxdrop/app/crypto/LinuxDropCrypto.kt` | Add `fromRawKey` + `tetherBleKey/tetherSsid/tetherPsk` | Modify |
| `android/app/src/test/java/com/linuxdrop/app/crypto/LinuxDropCryptoTest.kt` | Pin the new derivations to the vectors | Modify |
| `android/app/src/main/java/com/linuxdrop/app/tether/TetherFrame.kt` | Seal/open + parse the command/status frames (replay state) | Create |
| `android/app/src/test/java/com/linuxdrop/app/tether/TetherFrameTest.kt` | Frame round-trip + replay-rejection tests | Create |
| `android/app/src/main/java/com/linuxdrop/app/tether/TetherGattServer.kt` | BLE peripheral: advertise, serve nonce/command/status, auth, drive TetherController | Create |
| `android/app/src/main/AndroidManifest.xml` | `BLUETOOTH_ADVERTISE` + `BLUETOOTH_CONNECT` | Modify |
| `android/app/src/main/java/com/linuxdrop/app/service/SyncForegroundService.kt` | Start/stop `TetherGattServer` | Modify |
| `android/app/src/main/java/com/linuxdrop/app/MainActivity.kt` | Request BT runtime permissions (Android 12+) | Modify |

---

## Task 1: Pin the derivations as cross-language vectors

**Files:**
- Modify: `proto/gen-test-vectors.ts`
- Modify: `proto/crypto-test-vectors.json` (regenerated)
- Modify: `proto/PROTOCOL.md`

- [ ] **Step 1: Add the tether derivations to the generator**

In `proto/gen-test-vectors.ts`, after the `const ct = await aesGcmEncrypt(...)` line, add:
```ts
// --- tether (Plan 2) derivations ---
const TETHER_SALT = "linuxdrop/tether/v1";
const kBle = await hkdfSha256(secret, enc.encode(TETHER_SALT), enc.encode("ble-aead-key"), 32);
const ssidRaw = await hkdfSha256(secret, enc.encode(TETHER_SALT), enc.encode("softap-ssid"), 4);
const pskRaw = await hkdfSha256(secret, enc.encode(TETHER_SALT), enc.encode("softap-psk"), 12);
const tetherSsid = "LD-" + hex(ssidRaw);
const tetherPsk = hex(pskRaw);
```
Then add a `tether` block inside the `expected` object (after `ct_base64`):
```ts
    tether: {
      salt: TETHER_SALT,
      kBle_hex: hex(kBle),
      ssid: tetherSsid,
      psk: tetherPsk,
    },
```

- [ ] **Step 2: Regenerate the vectors**

Run: `bun run proto/gen-test-vectors.ts`
Expected: prints the JSON including a `expected.tether` object; `proto/crypto-test-vectors.json` is rewritten. **Copy the printed `kBle_hex`, `ssid`, `psk` values** — Task 2 Step 5 and Task 3 hardcode them as the expected constants.

- [ ] **Step 3: Document the protocol in PROTOCOL.md**

Append a new section to `proto/PROTOCOL.md` (after the existing crypto section):
```markdown
## N. Tether over BLE (auto-tether)

When the Linux box has no internet it wakes the phone over **BLE** (the relay is
unreachable) to enable a Wi-Fi hotspot. All values derive from the shared secret:

- `K_ble = HKDF-SHA256(secret, salt="linuxdrop/tether/v1", info="ble-aead-key", 32)`
- `ssid  = "LD-" + hex(HKDF(secret, "linuxdrop/tether/v1", "softap-ssid", 4))`
- `psk   = hex(HKDF(secret, "linuxdrop/tether/v1", "softap-psk", 12))`

GATT service `e3a9f5c0-1d2b-4e3a-9c8d-0a1b2c3d4e5f`: `nonce`(read, …c1), `command`(write, …c2),
`status`(notify, …c3). Frames are `sealBlob(K_ble)` = `iv(12)||ct||tag(16)`.
- command plaintext = `sessionNonce(16) || seq(4 BE) || opcode(1)` (ENABLE=1, DISABLE=2, KEEPALIVE=3)
- status  plaintext = `opcode(1) || resultCode(1)`

The phone issues a fresh `sessionNonce` per connection; a command is accepted iff the seal
opens, the embedded nonce matches, and `seq` strictly increases. Unbonded GATT — the AEAD is
the trust boundary. Vectors: `proto/crypto-test-vectors.json` → `expected.tether`.
```

- [ ] **Step 4: Commit**

```bash
git add proto/gen-test-vectors.ts proto/crypto-test-vectors.json proto/PROTOCOL.md
git commit -m "proto(tether): pin BLE key + SSID/PSK derivations and document the BLE service"
```

---

## Task 2: Kotlin derivations (`tetherBleKey`/`tetherSsid`/`tetherPsk`)

**Files:**
- Modify: `android/app/src/main/java/com/linuxdrop/app/crypto/LinuxDropCrypto.kt`
- Modify: `android/app/src/test/java/com/linuxdrop/app/crypto/LinuxDropCryptoTest.kt`

- [ ] **Step 1: Write the failing test**

In `LinuxDropCryptoTest.kt`, add (replace `<<…>>` with the exact values printed by Task 1 Step 2):
```kotlin
    @Test
    fun tetherBleKey_matchesVector() {
        assertEquals(
            "<<kBle_hex from generator>>",
            hex.formatHex(LinuxDropCrypto.tetherBleKey(secret)),
        )
    }

    @Test
    fun tetherSsidAndPsk_matchVectors() {
        assertEquals("<<ssid from generator>>", LinuxDropCrypto.tetherSsid(secret))
        assertEquals("<<psk from generator>>", LinuxDropCrypto.tetherPsk(secret))
    }
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd android && bash ../scripts/build-apk.sh >/dev/null 2>&1; docker run --rm -e ANDROID_HOME=/sdk -e GRADLE_USER_HOME=/sdk/.gradle -v "$PWD":/work -v linuxdrop-android-sdk:/sdk -w /work gradle:8.11.1-jdk21 gradle :app:testDebugUnitTest --no-daemon --tests '*LinuxDropCryptoTest'`
Expected: FAIL — `tetherBleKey`/`tetherSsid`/`tetherPsk` unresolved.

> Unit tests run in the same hermetic container as the build (no host JDK). If a project-local
> `gradlew`/JDK is added later, `./gradlew :app:testDebugUnitTest` works too.

- [ ] **Step 3: Implement the derivations**

In `LinuxDropCrypto.kt`, add a private generic HKDF and the public tether helpers to the `companion object` (the existing `deriveKey` keeps working; this generalizes the same single-block expand):
```kotlin
        private const val TETHER_SALT = "linuxdrop/tether/v1"

        /** HKDF-SHA256, single-block expand (len<=32). RFC 5869. */
        private fun hkdf(secret: ByteArray, salt: String, info: String, len: Int): ByteArray {
            val mac = Mac.getInstance("HmacSHA256")
            mac.init(SecretKeySpec(salt.toByteArray(Charsets.UTF_8), "HmacSHA256"))
            val prk = mac.doFinal(secret)
            mac.init(SecretKeySpec(prk, "HmacSHA256"))
            mac.update(info.toByteArray(Charsets.UTF_8))
            mac.update(0x01.toByte())
            return mac.doFinal().copyOf(len)
        }

        /** AES-256-GCM key for the BLE tether frames. */
        fun tetherBleKey(secret: ByteArray): ByteArray = hkdf(secret, TETHER_SALT, "ble-aead-key", 32)

        /** Stable hotspot SSID derived from the secret: "LD-" + 8 hex chars. */
        fun tetherSsid(secret: ByteArray): String =
            "LD-" + hkdf(secret, TETHER_SALT, "softap-ssid", 4).joinToString("") { "%02x".format(it) }

        /** Stable WPA2 passphrase derived from the secret: 24 hex chars. */
        fun tetherPsk(secret: ByteArray): String =
            hkdf(secret, TETHER_SALT, "softap-psk", 12).joinToString("") { "%02x".format(it) }

        /** A cipher keyed directly by a raw 32-byte key (e.g. K_ble), bypassing secret→encKey. */
        fun fromRawKey(key: ByteArray): LinuxDropCrypto = LinuxDropCrypto(key)
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `docker run --rm -e ANDROID_HOME=/sdk -e GRADLE_USER_HOME=/sdk/.gradle -v "$PWD/android":/work -v linuxdrop-android-sdk:/sdk -w /work gradle:8.11.1-jdk21 gradle :app:testDebugUnitTest --no-daemon --tests '*LinuxDropCryptoTest'`
Expected: PASS (all `LinuxDropCryptoTest` tests green).

- [ ] **Step 5: Commit**

```bash
git add android/app/src/main/java/com/linuxdrop/app/crypto/LinuxDropCrypto.kt \
        android/app/src/test/java/com/linuxdrop/app/crypto/LinuxDropCryptoTest.kt
git commit -m "feat(crypto): secret-derived tether BLE key, SSID and PSK (pinned to vectors)"
```

---

## Task 3: Command/status frame codec + replay state (`TetherFrame`)

**Files:**
- Create: `android/app/src/main/java/com/linuxdrop/app/tether/TetherFrame.kt`
- Create: `android/app/src/test/java/com/linuxdrop/app/tether/TetherFrameTest.kt`

- [ ] **Step 1: Write the failing test**

`android/app/src/test/java/com/linuxdrop/app/tether/TetherFrameTest.kt`:
```kotlin
package com.linuxdrop.app.tether

import com.linuxdrop.app.crypto.LinuxDropCrypto
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import java.util.HexFormat

class TetherFrameTest {
    private val secret = HexFormat.of().parseHex("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
    private val key = LinuxDropCrypto.tetherBleKey(secret)
    private val nonce = ByteArray(16) { it.toByte() }

    @Test
    fun commandRoundTrip_acceptsMonotonicSeq() {
        val verifier = TetherFrame.Verifier(key, nonce)
        val f1 = TetherFrame.sealCommand(key, nonce, seq = 1, opcode = TetherFrame.OP_ENABLE)
        val f2 = TetherFrame.sealCommand(key, nonce, seq = 2, opcode = TetherFrame.OP_KEEPALIVE)
        assertEquals(TetherFrame.OP_ENABLE, verifier.open(f1)!!.opcode)
        assertEquals(TetherFrame.OP_KEEPALIVE, verifier.open(f2)!!.opcode)
    }

    @Test
    fun rejectsReplayedOrStaleSeq() {
        val verifier = TetherFrame.Verifier(key, nonce)
        val f1 = TetherFrame.sealCommand(key, nonce, seq = 5, opcode = TetherFrame.OP_ENABLE)
        assertEquals(TetherFrame.OP_ENABLE, verifier.open(f1)!!.opcode)
        assertNull(verifier.open(f1))                                   // replay (seq 5 again)
        assertNull(verifier.open(TetherFrame.sealCommand(key, nonce, 4, TetherFrame.OP_DISABLE))) // stale
    }

    @Test
    fun rejectsWrongKeyAndWrongNonce() {
        val verifier = TetherFrame.Verifier(key, nonce)
        val wrongKey = LinuxDropCrypto.tetherBleKey(ByteArray(32) { 9 })
        assertNull(verifier.open(TetherFrame.sealCommand(wrongKey, nonce, 1, TetherFrame.OP_ENABLE)))
        assertNull(verifier.open(TetherFrame.sealCommand(key, ByteArray(16) { 7 }, 1, TetherFrame.OP_ENABLE)))
    }

    @Test
    fun statusRoundTrip() {
        val s = TetherFrame.sealStatus(key, opcode = TetherFrame.OP_ENABLE, result = 0)
        val (op, res) = TetherFrame.openStatus(key, s)!!
        assertEquals(TetherFrame.OP_ENABLE, op); assertEquals(0, res)
    }
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `docker run --rm -e ANDROID_HOME=/sdk -e GRADLE_USER_HOME=/sdk/.gradle -v "$PWD/android":/work -v linuxdrop-android-sdk:/sdk -w /work gradle:8.11.1-jdk21 gradle :app:testDebugUnitTest --no-daemon --tests '*TetherFrameTest'`
Expected: FAIL — `TetherFrame` unresolved.

- [ ] **Step 3: Implement `TetherFrame`**

`android/app/src/main/java/com/linuxdrop/app/tether/TetherFrame.kt`:
```kotlin
package com.linuxdrop.app.tether

import com.linuxdrop.app.crypto.LinuxDropCrypto
import java.nio.ByteBuffer

/**
 * Codec for the tether BLE frames (see proto/PROTOCOL.md). Frames are
 * LinuxDropCrypto.sealBlob(K_ble) = iv(12)||ct||tag(16). A wrong key fails the GCM
 * tag → open() returns null. [Verifier] adds per-connection replay protection
 * (session nonce + strictly-increasing seq).
 */
object TetherFrame {
    const val OP_ENABLE: Int = 1
    const val OP_DISABLE: Int = 2
    const val OP_KEEPALIVE: Int = 3

    data class Command(val seq: Long, val opcode: Int)

    /** command plaintext = sessionNonce(16) || seq(4 BE) || opcode(1). */
    fun sealCommand(key: ByteArray, sessionNonce: ByteArray, seq: Long, opcode: Int): ByteArray {
        require(sessionNonce.size == 16) { "nonce must be 16 bytes" }
        val pt = ByteBuffer.allocate(16 + 4 + 1)
            .put(sessionNonce).putInt(seq.toInt()).put(opcode.toByte()).array()
        return LinuxDropCrypto.fromRawKey(key).sealBlob(pt)
    }

    /** status plaintext = opcode(1) || resultCode(1). */
    fun sealStatus(key: ByteArray, opcode: Int, result: Int): ByteArray =
        LinuxDropCrypto.fromRawKey(key).sealBlob(byteArrayOf(opcode.toByte(), result.toByte()))

    /** Returns (opcode, result) or null if the seal is invalid. */
    fun openStatus(key: ByteArray, frame: ByteArray): Pair<Int, Int>? {
        val pt = runCatching { LinuxDropCrypto.fromRawKey(key).openBlob(frame) }.getOrNull() ?: return null
        if (pt.size < 2) return null
        return (pt[0].toInt() and 0xff) to (pt[1].toInt() and 0xff)
    }

    /** Per-connection command verifier: binds to one session nonce, rejects replays/stale seq. */
    class Verifier(private val key: ByteArray, private val sessionNonce: ByteArray) {
        private var lastSeq = 0L

        fun open(frame: ByteArray): Command? {
            val pt = runCatching { LinuxDropCrypto.fromRawKey(key).openBlob(frame) }.getOrNull() ?: return null
            if (pt.size != 21) return null
            val embeddedNonce = pt.copyOfRange(0, 16)
            if (!embeddedNonce.contentEquals(sessionNonce)) return null
            val buf = ByteBuffer.wrap(pt, 16, 5)
            val seq = buf.int.toLong() and 0xffffffffL
            val opcode = buf.get().toInt() and 0xff
            if (seq <= lastSeq) return null   // replay / stale
            lastSeq = seq
            return Command(seq, opcode)
        }
    }
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `docker run --rm -e ANDROID_HOME=/sdk -e GRADLE_USER_HOME=/sdk/.gradle -v "$PWD/android":/work -v linuxdrop-android-sdk:/sdk -w /work gradle:8.11.1-jdk21 gradle :app:testDebugUnitTest --no-daemon --tests '*TetherFrameTest'`
Expected: PASS (round-trip, replay-rejection, wrong-key/nonce all green).

- [ ] **Step 5: Commit**

```bash
git add android/app/src/main/java/com/linuxdrop/app/tether/TetherFrame.kt \
        android/app/src/test/java/com/linuxdrop/app/tether/TetherFrameTest.kt
git commit -m "feat(tether): BLE command/status frame codec with replay-protected verifier"
```

---

## Task 4: BLE permissions

**Files:**
- Modify: `android/app/src/main/AndroidManifest.xml`
- Modify: `android/app/src/main/java/com/linuxdrop/app/MainActivity.kt`

- [ ] **Step 1: Declare the permissions**

In `AndroidManifest.xml`, next to the existing `CHANGE_WIFI_STATE` line, add:
```xml
    <uses-permission android:name="android.permission.BLUETOOTH_ADVERTISE" />
    <uses-permission android:name="android.permission.BLUETOOTH_CONNECT" />
```

- [ ] **Step 2: Request them at runtime (Android 12+)**

In `MainActivity.kt`, find where other runtime permissions are requested (e.g. notifications). Add a helper and call it from the same place the app asks for permissions:
```kotlin
    private fun requestBtPermissionsIfNeeded() {
        if (android.os.Build.VERSION.SDK_INT < 31) return
        val needed = arrayOf(
            android.Manifest.permission.BLUETOOTH_ADVERTISE,
            android.Manifest.permission.BLUETOOTH_CONNECT,
        ).filter {
            checkSelfPermission(it) != android.content.pm.PackageManager.PERMISSION_GRANTED
        }
        if (needed.isNotEmpty()) requestPermissions(needed.toTypedArray(), 0xB7)
    }
```
Call `requestBtPermissionsIfNeeded()` from `onCreate` (or the existing permission-request entrypoint).

- [ ] **Step 3: Build to verify it compiles**

Run: `bash scripts/build-apk.sh`
Expected: `BUILD SUCCESSFUL`.

- [ ] **Step 4: Commit**

```bash
git add android/app/src/main/AndroidManifest.xml \
        android/app/src/main/java/com/linuxdrop/app/MainActivity.kt
git commit -m "feat(tether): declare + request BLE advertise/connect permissions"
```

---

## Task 5: `TetherGattServer` — advertise, serve, authenticate, drive

**Files:**
- Create: `android/app/src/main/java/com/linuxdrop/app/tether/TetherGattServer.kt`

- [ ] **Step 1: Implement the GATT server**

`android/app/src/main/java/com/linuxdrop/app/tether/TetherGattServer.kt`:
```kotlin
package com.linuxdrop.app.tether

import android.annotation.SuppressLint
import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothGattCharacteristic
import android.bluetooth.BluetoothGattDescriptor
import android.bluetooth.BluetoothGattServer
import android.bluetooth.BluetoothGattServerCallback
import android.bluetooth.BluetoothGattService
import android.bluetooth.BluetoothManager
import android.bluetooth.BluetoothProfile
import android.bluetooth.le.AdvertiseCallback
import android.bluetooth.le.AdvertiseData
import android.bluetooth.le.AdvertiseSettings
import android.content.Context
import android.os.ParcelUuid
import android.util.Log
import com.linuxdrop.app.crypto.LinuxDropCrypto
import java.security.SecureRandom
import java.util.UUID

/**
 * BLE peripheral exposing the tether service (proto/PROTOCOL.md). The laptop reads
 * [nonce], writes AEAD-sealed commands to [command]; we verify with K_ble + a
 * per-connection [TetherFrame.Verifier] and drive [TetherController]. Status is
 * pushed (sealed) on [status]. Unbonded — AEAD is the trust boundary.
 */
@SuppressLint("MissingPermission") // BLUETOOTH_ADVERTISE/CONNECT requested in MainActivity
class TetherGattServer(
    private val context: Context,
    secret: ByteArray,
    private val controller: TetherController,
) {
    private val kBle = LinuxDropCrypto.tetherBleKey(secret)
    private val ssid = LinuxDropCrypto.tetherSsid(secret)
    private val psk = LinuxDropCrypto.tetherPsk(secret)
    private val rng = SecureRandom()

    private var gattServer: BluetoothGattServer? = null
    @Volatile private var sessionNonce = ByteArray(16)
    @Volatile private var verifier: TetherFrame.Verifier? = null
    @Volatile private var central: BluetoothDevice? = null

    private val nonceChar = BluetoothGattCharacteristic(
        UUID_NONCE, BluetoothGattCharacteristic.PROPERTY_READ, BluetoothGattCharacteristic.PERMISSION_READ,
    )
    private val commandChar = BluetoothGattCharacteristic(
        UUID_COMMAND, BluetoothGattCharacteristic.PROPERTY_WRITE, BluetoothGattCharacteristic.PERMISSION_WRITE,
    )
    private val statusChar = BluetoothGattCharacteristic(
        UUID_STATUS, BluetoothGattCharacteristic.PROPERTY_NOTIFY, 0,
    ).apply {
        addDescriptor(BluetoothGattDescriptor(
            UUID_CCCD,
            BluetoothGattDescriptor.PERMISSION_READ or BluetoothGattDescriptor.PERMISSION_WRITE,
        ))
    }

    fun start() {
        val mgr = context.getSystemService(Context.BLUETOOTH_SERVICE) as BluetoothManager
        val adapter = mgr.adapter ?: run { Log.w(TAG, "no BT adapter"); return }
        if (!adapter.isEnabled) { Log.w(TAG, "BT off; tether wake unavailable"); return }

        val server = mgr.openGattServer(context, callback) ?: run { Log.e(TAG, "openGattServer failed"); return }
        val service = BluetoothGattService(UUID_SERVICE, BluetoothGattService.SERVICE_TYPE_PRIMARY).apply {
            addCharacteristic(nonceChar); addCharacteristic(commandChar); addCharacteristic(statusChar)
        }
        server.addService(service)
        gattServer = server

        adapter.bluetoothLeAdvertiser?.startAdvertising(
            AdvertiseSettings.Builder()
                .setAdvertiseMode(AdvertiseSettings.ADVERTISE_MODE_LOW_POWER)
                .setConnectable(true).build(),
            AdvertiseData.Builder()
                .setIncludeDeviceName(false)
                .addServiceUuid(ParcelUuid(UUID_SERVICE)).build(),
            advCallback,
        )
        Log.i(TAG, "tether GATT server up; ssid=$ssid")
    }

    fun stop() {
        runCatching {
            (context.getSystemService(Context.BLUETOOTH_SERVICE) as BluetoothManager)
                .adapter?.bluetoothLeAdvertiser?.stopAdvertising(advCallback)
        }
        runCatching { gattServer?.close() }
        gattServer = null
    }

    private val advCallback = object : AdvertiseCallback() {
        override fun onStartFailure(errorCode: Int) { Log.e(TAG, "advertise failed: $errorCode") }
    }

    private val callback = object : BluetoothGattServerCallback() {
        override fun onConnectionStateChange(device: BluetoothDevice, status: Int, newState: Int) {
            if (newState == BluetoothProfile.STATE_CONNECTED) {
                // Fresh session nonce + verifier per connection (replay protection).
                sessionNonce = ByteArray(16).also { rng.nextBytes(it) }
                verifier = TetherFrame.Verifier(kBle, sessionNonce)
                central = device
                Log.i(TAG, "central connected")
            } else if (newState == BluetoothProfile.STATE_DISCONNECTED) {
                verifier = null; central = null
            }
        }

        override fun onCharacteristicReadRequest(
            device: BluetoothDevice, requestId: Int, offset: Int, ch: BluetoothGattCharacteristic,
        ) {
            val value = if (ch.uuid == UUID_NONCE) sessionNonce else ByteArray(0)
            gattServer?.sendResponse(device, requestId, 0 /*GATT_SUCCESS*/, offset, value)
        }

        override fun onCharacteristicWriteRequest(
            device: BluetoothDevice, requestId: Int, ch: BluetoothGattCharacteristic,
            preparedWrite: Boolean, responseNeeded: Boolean, offset: Int, value: ByteArray,
        ) {
            if (responseNeeded) gattServer?.sendResponse(device, requestId, 0, offset, null)
            if (ch.uuid != UUID_COMMAND) return
            val cmd = verifier?.open(value) ?: run { Log.w(TAG, "rejected command (auth/replay)"); return }
            when (cmd.opcode) {
                TetherFrame.OP_ENABLE -> controller.enable(ssid, psk) { code -> notifyStatus(device, cmd.opcode, code) }
                TetherFrame.OP_DISABLE -> controller.disable { code -> notifyStatus(device, cmd.opcode, code) }
                TetherFrame.OP_KEEPALIVE -> { controller.keepAlive(); notifyStatus(device, cmd.opcode, TetherResult.OK) }
                else -> Log.w(TAG, "unknown opcode ${cmd.opcode}")
            }
        }

        override fun onDescriptorWriteRequest(
            device: BluetoothDevice, requestId: Int, descriptor: BluetoothGattDescriptor,
            preparedWrite: Boolean, responseNeeded: Boolean, offset: Int, value: ByteArray,
        ) {
            if (responseNeeded) gattServer?.sendResponse(device, requestId, 0, offset, null)
        }
    }

    private fun notifyStatus(device: BluetoothDevice, opcode: Int, result: Int) {
        val frame = TetherFrame.sealStatus(kBle, opcode, result)
        statusChar.value = frame
        runCatching { gattServer?.notifyCharacteristicChanged(device, statusChar, false) }
    }

    companion object {
        private const val TAG = "linuxDropTetherBle"
        val UUID_SERVICE: UUID = UUID.fromString("e3a9f5c0-1d2b-4e3a-9c8d-0a1b2c3d4e5f")
        val UUID_NONCE: UUID = UUID.fromString("e3a9f5c1-1d2b-4e3a-9c8d-0a1b2c3d4e5f")
        val UUID_COMMAND: UUID = UUID.fromString("e3a9f5c2-1d2b-4e3a-9c8d-0a1b2c3d4e5f")
        val UUID_STATUS: UUID = UUID.fromString("e3a9f5c3-1d2b-4e3a-9c8d-0a1b2c3d4e5f")
        val UUID_CCCD: UUID = UUID.fromString("00002902-0000-1000-8000-00805f9b34fb")
    }
}
```

- [ ] **Step 2: Build to verify it compiles**

Run: `bash scripts/build-apk.sh`
Expected: `BUILD SUCCESSFUL`.

- [ ] **Step 3: Commit**

```bash
git add android/app/src/main/java/com/linuxdrop/app/tether/TetherGattServer.kt
git commit -m "feat(tether): BLE GATT peripheral — authenticated, replay-protected hotspot wake"
```

---

## Task 6: Host the GATT server in `SyncForegroundService`

**Files:**
- Modify: `android/app/src/main/java/com/linuxdrop/app/service/SyncForegroundService.kt`

- [ ] **Step 1: Wire start/stop**

In `SyncForegroundService.kt`, add a field near the other collaborators (`private var p2p: P2pManager? = null`):
```kotlin
    private var tetherGatt: TetherGattServer? = null
```
In `onStartCommand`, after `p2p = P2pManager(...)` is set up (the `bytes`/`secret` are already in scope), add:
```kotlin
        // BLE tether wake: lets a laptop with no internet ask us to enable the hotspot.
        tetherGatt = TetherGattServer(this, bytes, com.linuxdrop.app.tether.TetherController(this)).also {
            runCatching { it.start() }
        }
```
In `onDestroy`, alongside the other `runCatching { … }` teardown calls, add:
```kotlin
        runCatching { tetherGatt?.stop() }
```
Add the import: `import com.linuxdrop.app.tether.TetherGattServer`.

- [ ] **Step 2: Build to verify it compiles**

Run: `bash scripts/build-apk.sh`
Expected: `BUILD SUCCESSFUL`.

- [ ] **Step 3: Commit**

```bash
git add android/app/src/main/java/com/linuxdrop/app/service/SyncForegroundService.kt
git commit -m "feat(tether): start the BLE tether GATT server with the sync service"
```

---

## Task 7: On-device smoke test (generic BLE central)

> **STATUS (2026-06-08): Tasks 1–6 done & committed; unit tests green. Task 7 Steps 1–2 VERIFIED
> on-device (OnePlus CPH2765) via a Linux BlueZ central (`bleak`, crypto self-test PASS): the
> service advertises (`e3a9f5c0…` found at 50:AF:E3:D0:4C:E4), and a 40-byte garbage write to
> `command` is rejected — phone logs `rejected command (auth/replay)`, no hotspot iface (`wlan2`).
> Step 3 (valid sealed ENABLE → hotspot up) + the Plan-1 safety auto-off remain — they need the
> paired secret and are exercised by the Plan-3 Linux central.**


The full end-to-end (laptop joins the hotspot) is Plan 3. Here, verify the phone advertises, authenticates, and toggles the hotspot when driven by **any** BLE central, using a phone-side log check plus a generic scanner.

**Files:** none (manual verification).

- [ ] **Step 1: Install and confirm advertising**

Run: `bash scripts/build-apk.sh && adb install -r android/app/build/outputs/apk/debug/app-debug.apk`
Then start the sync service from the app (toggle "Start"), and from a second phone run a BLE scanner app (e.g. nRF Connect) — confirm a connectable device advertising service `e3a9f5c0-1d2b-4e3a-9c8d-0a1b2c3d4e5f` appears.

- [ ] **Step 2: Confirm auth rejects unsigned writes**

In nRF Connect: connect, read the `nonce` (…c1) characteristic, then write arbitrary bytes to `command` (…c2). `adb logcat -s linuxDropTetherBle` must show `rejected command (auth/replay)` and the hotspot must NOT come up. This proves the AEAD gate.

- [ ] **Step 3: Confirm a correctly-sealed ENABLE works (defer if no central handy)**

A valid frame requires `K_ble`, so a generic scanner can't forge one — this step is genuinely exercised by Plan 3's Linux central. If you want to verify now, add a temporary JVM/`rish` helper that, given the secret + the read nonce, prints `hex(TetherFrame.sealCommand(...))` to paste into nRF Connect's `command` write; expect `linuxDropTether` logs `setSoftApConfiguration ok` + `startTethering result=0` and the `LD-…` SSID to appear. Otherwise mark this verified in Plan 3.

- [ ] **Step 4: Record the result**

Note in the commit / PR whether Steps 1–2 passed on-device. No code commit for this task unless a fix was needed.

---

## Self-review (against the spec)

- **Spec §6 derivations** (SSID/PSK/K_ble from secret, HKDF) → Task 1 + Task 2. ✅ (hex instead of base32 — simpler, identical cross-language; noted.)
- **Spec §6 BLE auth** (unbonded, AEAD with K_ble, nonce + replay) → Task 3 (`Verifier`: session nonce + monotonic seq) + Task 5 (fresh nonce per connection). ✅
- **Spec §4.2 TetherGattServer in the FGS** → Task 5 + Task 6. ✅
- **Spec §6 reuse existing crypto** → `LinuxDropCrypto.fromRawKey` + `sealBlob`/`openBlob`. ✅
- **Spec §6 cross-language pinning** → Task 1 vectors (`expected.tether`), consumed by Plan 3's Go side. ✅
- **Spec §4.2 manifest BLE perms** → Task 4. ✅
- **Commands ENABLE/DISABLE/KEEPALIVE** → wired to `TetherController.enable/disable/keepAlive` (Plan 1 surface). ✅
- **Deferred to Plan 3 (explicit):** Linux BLE central, Wi-Fi join, connectivity monitor, orchestrator, auto-trigger — and the full laptop-joins-hotspot e2e (Task 7 Step 3).
- **Placeholder scan:** the only `<<…>>` are the generated vector hexes in Task 2 Step 1, filled mechanically from Task 1 Step 2's generator output (a generated constant, not a design gap). Everything else is concrete.
- **Type consistency:** `TetherFrame.OP_*`, `Verifier.open`, `sealCommand/sealStatus/openStatus`, `LinuxDropCrypto.tetherBleKey/tetherSsid/tetherPsk/fromRawKey`, `TetherController.enable/disable/keepAlive` all used consistently across Tasks 2–6 and match Plan 1's committed surface. ✅
