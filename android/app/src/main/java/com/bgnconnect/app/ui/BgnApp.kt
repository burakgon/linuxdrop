package com.bgnconnect.app.ui

import android.widget.Toast
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions

const val SHIZUKU_REQUEST_CODE = 4001

private enum class Screen { HOME, SETTINGS, ADD, HISTORY }

@Composable
fun BgnApp(vm: MainViewModel) {
    val ui by vm.ui.collectAsStateWithLifecycle()
    val context = LocalContext.current

    val scanLauncher = rememberLauncherForActivityResult(ScanContract()) { result ->
        val contents = result.contents
        if (contents != null) {
            val ok = vm.joinWith(contents)
            Toast.makeText(context, if (ok) "Joined the network ✓" else "Invalid QR / key", Toast.LENGTH_SHORT).show()
        }
    }
    val scan: () -> Unit = {
        scanLauncher.launch(
            ScanOptions()
                .setCaptureActivity(PortraitCaptureActivity::class.java)
                .setOrientationLocked(true)
                .setBeepEnabled(false)
                .setPrompt("Scan the pairing QR")
                .setDesiredBarcodeFormats(ScanOptions.QR_CODE),
        )
    }
    val grantShizuku: () -> Unit = { vm.requestShizukuPermission(SHIZUKU_REQUEST_CODE) }

    if (!ui.configured) {
        OnboardingScreen(
            ui = ui,
            onCreateNetwork = { relay -> vm.createNetwork(relay) },
            onScanQr = scan,
            onGrantShizuku = grantShizuku,
            onSetName = { vm.setDeviceName(it) },
        )
        return
    }

    var screen by remember { mutableStateOf(Screen.HOME) }
    when (screen) {
        Screen.SETTINGS -> SettingsScreen(
            ui = ui,
            onBack = { screen = Screen.HOME },
            onSave = { name, relay -> vm.setDeviceName(name); vm.setRelay(relay) },
            onRegenerateKey = { vm.regenerateKey() },
            onGrantShizuku = grantShizuku,
        )
        Screen.ADD -> AddDeviceScreen(ui = ui, onBack = { screen = Screen.HOME })
        Screen.HISTORY -> HistoryScreen(
            onBack = { screen = Screen.HOME },
            onCopy = { vm.copyToClipboard(it) },
            onClear = { vm.clearHistory() },
        )
        Screen.HOME -> HomeScreen(
            ui = ui,
            onToggle = { vm.toggle() },
            onSettings = { screen = Screen.SETTINGS },
            onAddDevice = { screen = Screen.ADD },
            onScanQr = scan,
            onGrantShizuku = grantShizuku,
            onHistory = { screen = Screen.HISTORY },
        )
    }
}
