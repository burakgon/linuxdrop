package com.linuxdrop.app.ui

import android.widget.Toast
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Computer
import androidx.compose.material.icons.filled.Smartphone
import androidx.compose.ui.unit.dp
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
fun LinuxDropApp(vm: MainViewModel) {
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

    // Send a file directly (P2P) to a chosen device: tap a device → pick a file → send.
    var pendingDev by remember { mutableStateOf<String?>(null) }
    val fileLauncher = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
        val target = pendingDev
        pendingDev = null
        if (uri != null && target != null) {
            vm.sendFile(target, uri)
            Toast.makeText(context, "Sending file…", Toast.LENGTH_SHORT).show()
        }
    }
    val sendFileTo: (String) -> Unit = { dev ->
        pendingDev = dev
        fileLauncher.launch(arrayOf("*/*"))
    }

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
            onOpenFile = { vm.openFile(it) },
        )
        Screen.HOME -> HomeScreen(
            ui = ui,
            onToggle = { vm.toggle() },
            onSettings = { screen = Screen.SETTINGS },
            onAddDevice = { screen = Screen.ADD },
            onScanQr = scan,
            onGrantShizuku = grantShizuku,
            onHistory = { screen = Screen.HISTORY },
            onSendFile = sendFileTo,
            onToggleTether = { vm.setTetherEnabled(it) },
        )
    }

    // Files shared into the app (OS Share sheet) → choose a device to send them to.
    val pendingShares by vm.pendingShares.collectAsStateWithLifecycle()
    if (pendingShares.isNotEmpty()) {
        ShareTargetDialog(
            devices = ui.sync.devices.filter { !it.self },
            count = pendingShares.size,
            onPick = { dev ->
                pendingShares.forEach { vm.sendFile(dev, it) }
                vm.clearPendingShares()
                Toast.makeText(context, "Sending…", Toast.LENGTH_SHORT).show()
            },
            onDismiss = { vm.clearPendingShares() },
        )
    }
}

@Composable
private fun ShareTargetDialog(
    devices: List<com.linuxdrop.app.service.SyncStatus.Device>,
    count: Int,
    onPick: (String) -> Unit,
    onDismiss: () -> Unit,
) {
    androidx.compose.material3.AlertDialog(
        onDismissRequest = onDismiss,
        title = { androidx.compose.material3.Text(if (count == 1) "Send file to…" else "Send $count files to…") },
        text = {
            if (devices.isEmpty()) {
                androidx.compose.material3.Text("No connected device. Turn on sync and make sure another device is online.")
            } else {
                androidx.compose.foundation.layout.Column {
                    devices.forEach { d ->
                        androidx.compose.foundation.layout.Row(
                            modifier = androidx.compose.ui.Modifier
                                .fillMaxWidth()
                                .clickable { onPick(d.dev) }
                                .padding(vertical = 12.dp),
                            verticalAlignment = androidx.compose.ui.Alignment.CenterVertically,
                            horizontalArrangement = androidx.compose.foundation.layout.Arrangement.spacedBy(12.dp),
                        ) {
                            androidx.compose.material3.Icon(
                                if (d.platform == "linux") Icons.Default.Computer else Icons.Default.Smartphone,
                                contentDescription = null,
                            )
                            androidx.compose.material3.Text("${d.name} · ${d.platform}")
                        }
                    }
                }
            }
        },
        confirmButton = {},
        dismissButton = {
            androidx.compose.material3.TextButton(onClick = onDismiss) { androidx.compose.material3.Text("Cancel") }
        },
    )
}
