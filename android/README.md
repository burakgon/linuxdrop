# LinuxDrop — Android client

Native Kotlin + **Jetpack Compose (Material 3)**. Reads the clipboard **in the background,
event-driven** (no polling) via Shizuku, and connects to the relay over a WebSocket held by a
`connectedDevice` foreground service. Content is end-to-end encrypted with AES-256-GCM (the
relay can't read it).

## Experience (built for non-technical users)
- **First launch (onboarding):** welcome → guided **Shizuku** step (detects installed/running/
  granted + links) → device name → enter your **self-hosted relay URL** and **"Create network"**,
  or **"Join via QR"**.
- **Home:** big status (Connected/Connecting/Off) + on/off, **connected-devices list**
  (name + platform), last-sync info, **"Add a device (show QR)"**, **"Scan QR · Join network"**,
  and a **clipboard history** screen.
- **Key stays out of the way:** auto-generated, hidden; visible via Settings → "Show key".
- **No built-in server:** you point the app at your own relay (Settings → Advanced).
- **Pairing:** QR (portrait scanner) or a `linuxdrop://pair?...` link (deep link) that carries
  the relay URL, so joining devices don't type anything.
- **Sensitive content** (OTP/passwords — flagged `IS_SENSITIVE`) is not synced.

## Requirements / build
- **JDK 17 or 21** (AGP 8.7.3). If your machine has a newer JDK, use the **hermetic Docker build**:
  ```bash
  bash scripts/build-apk.sh   # → android/app/build/outputs/apk/debug/app-debug.apk
  ```
  (Docker `gradle:8.11.1-jdk21` + SDK 35 in a persistent volume; a persistent debug keystore →
  reinstalls don't need an uninstall.)
- Or open `android/` in Android Studio (JDK 17/21) and build there.
- `compileSdk 35`, `minSdk 29` (Android 10), `targetSdk 35` (runs on Android 16).

## Verify crypto without a device
```bash
gradle :app:testDebugUnitTest   # LinuxDropCryptoTest, against proto/crypto-test-vectors.json
```

## Install + use
1. `adb install -r app-debug.apk` (or copy the APK to the phone and install it).
2. Install + start **Shizuku** (the app guides you through onboarding).
3. Open the app → onboarding → grant Shizuku → enter your relay URL → **Create network**
   (or scan the QR from another device).
4. Other device: scan this device's **"Add a device (show QR)"** code via **"Scan QR"**.

## Known risk (device-verified, may need tweaks on some OEMs)
`shizuku/ClipboardUserService.kt` — `IClipboard` AIDL signatures vary by Android version/OEM;
the code reflects over the device's own `IClipboard$Stub` and picks overloads by parameter type
(using the `com.android.shell` package for the background-read exemption). **Verified on
OnePlus/ColorOS Android 16.** Rare variants may need a `buildArgs` tweak. Background image
read/write (Shizuku pipe + FileProvider) is the newest path and OEM-dependent.

## Architecture
`ui/` (Compose: Home/Onboarding/Settings/AddDevice/History + MainViewModel) · `service/`
(foreground service + `SyncStatus` state flow + `ClipHistory`) · `net/` (`WsClient` OkHttp WS +
roster, `BlobClient` for images) · `shizuku/` (UserService + reflection) · `crypto/LinuxDropCrypto` ·
`config/Secret`. Protocol: [`../proto/PROTOCOL.md`](../proto/PROTOCOL.md).
