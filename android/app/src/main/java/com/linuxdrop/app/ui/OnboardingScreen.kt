package com.linuxdrop.app.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.QrCodeScanner
import androidx.compose.material3.Button
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.ui.unit.dp

@Composable
fun OnboardingScreen(
    ui: UiModel,
    onCreateNetwork: (relay: String) -> Unit,
    onScanQr: () -> Unit,
    onGrantShizuku: () -> Unit,
    onSetName: (String) -> Unit,
) {
    var name by remember { mutableStateOf(ui.deviceName) }
    var relay by remember { mutableStateOf("") }
    val relayOk = relay.trim().startsWith("ws://") || relay.trim().startsWith("wss://")

    Column(
        Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(24.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Spacer(Modifier.height(8.dp))
        Text("LinuxDrop", style = MaterialTheme.typography.headlineMedium)
        Text(
            "Keep your phone and computer clipboards in sync — automatic and end-to-end encrypted.",
            style = MaterialTheme.typography.bodyLarge,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )

        OutlinedTextField(
            value = name,
            onValueChange = { name = it; onSetName(it) },
            label = { Text("This device's name") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )

        ShizukuSetupCard(ui, onGrant = onGrantShizuku)

        Text("Get started", style = MaterialTheme.typography.titleMedium)

        // Step 1 — the relay server (shared infrastructure; comes from the QR when joining).
        Text("1 · Your relay server", style = MaterialTheme.typography.titleSmall)
        OutlinedTextField(
            value = relay,
            onValueChange = { relay = it },
            label = { Text("Relay server URL") },
            placeholder = { Text("wss://relay.yourdomain.com") },
            supportingText = { Text("The self-hosted server your devices meet on.") },
            singleLine = true,
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, imeAction = ImeAction.Done),
            modifier = Modifier.fillMaxWidth(),
        )

        // Step 2 — create (generate a key) or join (import a key via QR).
        Text("2 · This device", style = MaterialTheme.typography.titleSmall)
        Button(
            onClick = { onCreateNetwork(relay.trim()) },
            enabled = relayOk,
            modifier = Modifier.fillMaxWidth(),
        ) {
            Icon(Icons.Default.Add, contentDescription = null)
            Spacer(Modifier.width(8.dp))
            Text("Create a new network")
        }
        Text(
            "A new key is generated; this device becomes the first one.",
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        OutlinedButton(onClick = onScanQr, modifier = Modifier.fillMaxWidth()) {
            Icon(Icons.Default.QrCodeScanner, contentDescription = null)
            Spacer(Modifier.width(8.dp))
            Text("Join an existing one (QR)")
        }
        Text(
            "Scan another device's QR — it brings the server + key, so you don't type anything.",
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
    }
}
