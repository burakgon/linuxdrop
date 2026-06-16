@file:OptIn(ExperimentalMaterial3Api::class)

package com.linuxdrop.app.ui

import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Computer
import androidx.compose.material.icons.filled.History
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.QrCode2
import androidx.compose.material.icons.filled.QrCodeScanner
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Smartphone
import androidx.compose.material.icons.filled.Stop
import androidx.compose.material.icons.filled.WifiTethering
import androidx.compose.material3.Button
import androidx.compose.material3.ElevatedCard
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import com.linuxdrop.app.service.SyncStatus

@Composable
fun HomeScreen(
    ui: UiModel,
    onToggle: () -> Unit,
    onSettings: () -> Unit,
    onAddDevice: () -> Unit,
    onScanQr: () -> Unit,
    onGrantShizuku: () -> Unit,
    onHistory: () -> Unit,
    onSendFile: (dev: String) -> Unit,
    onToggleTether: (Boolean) -> Unit,
) {
    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("LinuxDrop") },
                actions = {
                    IconButton(onClick = onHistory) { Icon(Icons.Default.History, contentDescription = "Clipboard history") }
                    IconButton(onClick = onSettings) { Icon(Icons.Default.Settings, contentDescription = "Settings") }
                },
            )
        },
    ) { padding ->
        Column(
            Modifier
                .padding(padding)
                .fillMaxSize()
                .verticalScroll(rememberScrollState())
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            StatusCard(ui, onToggle)
            if (!ui.shizukuReady) ShizukuSetupCard(ui, onGrant = onGrantShizuku)
            if (!ui.batteryUnrestricted) BatteryCard()
            DevicesCard(ui, onSendFile)
            TetherCard(ui, onToggleTether)
            Button(onClick = onAddDevice, modifier = Modifier.fillMaxWidth()) {
                Icon(Icons.Default.QrCode2, contentDescription = null)
                Spacer(Modifier.width(8.dp))
                Text("Add a device (show QR)")
            }
            OutlinedButton(onClick = onScanQr, modifier = Modifier.fillMaxWidth()) {
                Icon(Icons.Default.QrCodeScanner, contentDescription = null)
                Spacer(Modifier.width(8.dp))
                Text("Scan QR · Join network")
            }
        }
    }
}

@Composable
private fun StatusCard(ui: UiModel, onToggle: () -> Unit) {
    val label: String
    val sub: String
    val dot: Color
    when {
        ui.sync.running && ui.sync.connected -> {
            label = "Connected"; sub = "Clipboard sync is on"; dot = Color(0xFF2E7D32)
        }
        ui.sync.running -> {
            label = "Connecting…"; sub = "Reaching the server"; dot = Color(0xFFF9A825)
        }
        else -> {
            label = "Off"; sub = "Sync is stopped"; dot = MaterialTheme.colorScheme.outline
        }
    }

    // Pulse the dot while connecting; steady once connected/idle.
    val connecting = ui.sync.running && !ui.sync.connected
    val transition = rememberInfiniteTransition(label = "pulse")
    val pulse by transition.animateFloat(
        initialValue = 1f,
        targetValue = 0.35f,
        animationSpec = infiniteRepeatable(tween(750), RepeatMode.Reverse),
        label = "pulseAlpha",
    )
    val dotAlpha = if (connecting) pulse else 1f

    ElevatedCard(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                Box(Modifier.size(14.dp).clip(CircleShape).background(dot).alpha(dotAlpha))
                Column {
                    Text(label, style = MaterialTheme.typography.headlineSmall)
                    Text(sub, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
            if (ui.sync.lastSyncAt > 0L) {
                val now = liveNow()
                val dir = if (ui.sync.lastSyncOutgoing) "sent" else "received"
                Text(
                    "Last sync: ${relTime(ui.sync.lastSyncAt, now)} · $dir (${ui.sync.lastSyncChars} chars)",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            if (ui.sync.running) {
                FilledTonalButton(onClick = onToggle, modifier = Modifier.fillMaxWidth()) {
                    Icon(Icons.Default.Stop, contentDescription = null)
                    Spacer(Modifier.width(8.dp))
                    Text("Stop")
                }
            } else {
                Button(onClick = onToggle, modifier = Modifier.fillMaxWidth(), enabled = ui.shizukuReady) {
                    Icon(Icons.Default.PlayArrow, contentDescription = null)
                    Spacer(Modifier.width(8.dp))
                    Text(if (ui.shizukuReady) "Start" else "Set up Shizuku first")
                }
            }
        }
    }
}

@Composable
private fun DevicesCard(ui: UiModel, onSendFile: (String) -> Unit) {
    val canSend = ui.sync.running && ui.sync.connected
    ElevatedCard(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Text("Connected devices", style = MaterialTheme.typography.titleMedium)
            if (ui.sync.devices.isNotEmpty()) {
                ui.sync.devices.forEach { DeviceRow(it, canSend, onSendFile) }
            } else {
                val n = ui.sync.peerCount
                val text = when {
                    !ui.sync.running -> "Sync is off."
                    !ui.sync.connected -> "Waiting for connection…"
                    n <= 0 -> "This device is connected. Share the QR to add another."
                    else -> "This device + $n more connected."
                }
                Text(text, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
    }
}

@Composable
private fun TetherCard(ui: UiModel, onToggle: (Boolean) -> Unit) {
    val (statusText, sharing) = when {
        !ui.tetherEnabled -> "Off" to false
        ui.sync.tetherSharing -> "Sharing now — hotspot ${ui.sync.tetherSsid}" to true
        ui.sync.running -> "Ready · listening over Bluetooth" to false
        else -> "Turns on when sync is running" to false
    }
    ElevatedCard(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                Icon(Icons.Default.WifiTethering, contentDescription = null, tint = MaterialTheme.colorScheme.primary)
                Column(Modifier.weight(1f)) {
                    Text("Phone internet sharing", style = MaterialTheme.typography.titleMedium)
                    Text(
                        statusText,
                        style = MaterialTheme.typography.bodySmall,
                        color = if (sharing) Color(0xFF2E7D32) else MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
                Switch(checked = ui.tetherEnabled, onCheckedChange = onToggle)
            }
            Text(
                "When your computer has no internet, it wakes this phone over Bluetooth and turns on the hotspot " +
                    "to share your mobile data — automatically, no tapping needed.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}

@Composable
private fun DeviceRow(d: SyncStatus.Device, canSend: Boolean, onSendFile: (String) -> Unit) {
    Row(
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Icon(if (d.platform == "linux") Icons.Default.Computer else Icons.Default.Smartphone, contentDescription = null)
        Text(if (d.self) "${d.name} · this device" else d.name)
        Text("· ${d.platform}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        if (canSend && !d.self) {
            Spacer(Modifier.weight(1f))
            IconButton(onClick = { onSendFile(d.dev) }) {
                Icon(Icons.AutoMirrored.Filled.Send, contentDescription = "Send file")
            }
        }
    }
}
