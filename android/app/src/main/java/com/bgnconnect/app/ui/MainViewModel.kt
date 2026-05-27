package com.bgnconnect.app.ui

import android.app.Application
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.bgnconnect.app.config.Secret
import com.bgnconnect.app.service.ClipHistory
import com.bgnconnect.app.service.SyncForegroundService
import com.bgnconnect.app.service.SyncStatus
import com.bgnconnect.app.shizuku.ShizukuClipboard
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

/** Snapshot the Compose UI renders. Rebuilt from prefs + Shizuku + SyncStatus. */
data class UiModel(
    val configured: Boolean = false,
    val deviceName: String = "",
    val relayUrl: String = "",
    val secretHex: String? = null,
    val pairingUri: String? = null,
    val shizukuInstalled: Boolean = false,
    val shizukuRunning: Boolean = false,
    val shizukuGranted: Boolean = false,
    val batteryUnrestricted: Boolean = true,
    val sync: SyncStatus.State = SyncStatus.State(),
) {
    val shizukuReady: Boolean get() = shizukuRunning && shizukuGranted
}

class MainViewModel(app: Application) : AndroidViewModel(app) {

    private val secret = Secret(app)
    val shizuku = ShizukuClipboard(app)

    private val _ui = MutableStateFlow(build())
    val ui: StateFlow<UiModel> = _ui

    // Files handed to us via the OS Share sheet, awaiting a device choice.
    private val _pendingShares = MutableStateFlow<List<android.net.Uri>>(emptyList())
    val pendingShares: StateFlow<List<android.net.Uri>> = _pendingShares
    fun setPendingShares(uris: List<android.net.Uri>) { _pendingShares.value = uris }
    fun clearPendingShares() { _pendingShares.value = emptyList() }

    init {
        ClipHistory.ensure(app)
        viewModelScope.launch { SyncStatus.state.collect { refresh() } }
    }

    /** Re-read prefs + Shizuku state. Called on resume and after permission results. */
    fun refresh() {
        _ui.value = build()
    }

    private fun build() = UiModel(
        configured = secret.isConfigured(),
        deviceName = secret.deviceName,
        relayUrl = secret.relayUrl,
        secretHex = secret.secretHex,
        pairingUri = secret.pairingUri(),
        shizukuInstalled = shizuku.isInstalled(),
        shizukuRunning = shizuku.shizukuAvailable(),
        shizukuGranted = shizuku.permissionGranted(),
        batteryUnrestricted = isBatteryUnrestricted(),
        sync = SyncStatus.state.value,
    )

    private fun isBatteryUnrestricted(): Boolean {
        val app = getApplication<Application>()
        val pm = app.getSystemService(android.os.PowerManager::class.java)
        return pm?.isIgnoringBatteryOptimizations(app.packageName) ?: true
    }

    /** Create a new network on the user's self-hosted relay (key auto-generated). */
    fun createNetwork(relay: String) {
        secret.relayUrl = relay
        secret.ensureNetwork()
        refresh()
    }

    fun joinWith(text: String): Boolean = secret.importPairing(text).also { refresh() }

    fun setDeviceName(name: String) {
        secret.deviceName = name
        refresh(); restartIfRunning()
    }

    fun setRelay(url: String) {
        secret.relayUrl = url
        refresh(); restartIfRunning()
    }

    fun regenerateKey() {
        secret.clearNetwork()
        secret.ensureNetwork()
        refresh(); restartIfRunning()
    }

    fun requestShizukuPermission(code: Int) = shizuku.requestPermission(code)

    fun start() {
        if (!secret.isConfigured()) return
        SyncForegroundService.start(getApplication())
        refresh()
    }

    fun stop() {
        SyncForegroundService.stop(getApplication())
        refresh()
    }

    fun toggle() {
        if (_ui.value.sync.running) stop() else start()
    }

    /** Restore a history item to the clipboard (foreground write → picked up + re-synced). */
    fun copyToClipboard(text: String) {
        val app = getApplication<Application>()
        val cm = app.getSystemService(android.content.ClipboardManager::class.java)
        cm?.setPrimaryClip(android.content.ClipData.newPlainText("bgnconnect", text))
    }

    fun clearHistory() = ClipHistory.clear(getApplication())

    /** Send a file directly (P2P) to a peer device — bytes go peer-to-peer, not via the relay. */
    fun sendFile(toDev: String, uri: android.net.Uri) {
        SyncForegroundService.sendFile(getApplication(), toDev, uri)
    }

    /** Open a received file (from history) in its default app. */
    fun openFile(uriString: String) {
        val app = getApplication<Application>()
        val uri = android.net.Uri.parse(uriString)
        val intent = android.content.Intent(android.content.Intent.ACTION_VIEW).apply {
            setDataAndType(uri, app.contentResolver.getType(uri) ?: "*/*")
            addFlags(android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION or android.content.Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        runCatching { app.startActivity(intent) }
    }

    private fun restartIfRunning() {
        if (_ui.value.sync.running) {
            stop(); start()
        }
    }
}
