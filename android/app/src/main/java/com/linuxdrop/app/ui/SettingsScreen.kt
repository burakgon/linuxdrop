@file:OptIn(ExperimentalMaterial3Api::class)

package com.linuxdrop.app.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Visibility
import androidx.compose.material.icons.filled.VisibilityOff
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp

@Composable
fun SettingsScreen(
    ui: UiModel,
    onBack: () -> Unit,
    onSave: (name: String, relay: String) -> Unit,
    onRegenerateKey: () -> Unit,
    onGrantShizuku: () -> Unit,
) {
    val clipboard = LocalClipboardManager.current
    var name by remember { mutableStateOf(ui.deviceName) }
    var relay by remember { mutableStateOf(ui.relayUrl) }
    var showKey by remember { mutableStateOf(false) }
    var confirmRegen by remember { mutableStateOf(false) }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Settings") },
                navigationIcon = {
                    IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back") }
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
            OutlinedTextField(
                value = name,
                onValueChange = { name = it },
                label = { Text("Device name") },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
            )

            Text("Advanced", style = MaterialTheme.typography.titleMedium)
            OutlinedTextField(
                value = relay,
                onValueChange = { relay = it },
                label = { Text("Relay server URL") },
                supportingText = { Text("Your self-hosted server, e.g. wss://relay.yourdomain.com") },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
            )
            Button(
                onClick = { onSave(name.trim(), relay.trim()) },
                modifier = Modifier.fillMaxWidth(),
            ) { Text("Save") }

            HorizontalDivider()

            Text("Sync key", style = MaterialTheme.typography.titleMedium)
            Text(
                "The secret key that pairs your devices. You normally don't need to see it — use the QR to add a device.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Row {
                TextButton(onClick = { showKey = !showKey }) {
                    Icon(if (showKey) Icons.Default.VisibilityOff else Icons.Default.Visibility, contentDescription = null)
                    Spacer(Modifier.width(8.dp))
                    Text(if (showKey) "Hide key" else "Show key")
                }
            }
            if (showKey) {
                SelectionContainer {
                    Text(
                        ui.secretHex ?: "-",
                        style = MaterialTheme.typography.bodySmall,
                        fontFamily = FontFamily.Monospace,
                    )
                }
                OutlinedButton(onClick = { clipboard.setText(AnnotatedString(ui.secretHex ?: "")) }) {
                    Icon(Icons.Default.ContentCopy, contentDescription = null)
                    Spacer(Modifier.width(8.dp))
                    Text("Copy")
                }
            }
            OutlinedButton(onClick = { confirmRegen = true }, modifier = Modifier.fillMaxWidth()) {
                Icon(Icons.Default.Refresh, contentDescription = null)
                Spacer(Modifier.width(8.dp))
                Text("Regenerate key")
            }

            HorizontalDivider()

            if (!ui.shizukuReady) {
                ShizukuSetupCard(ui, onGrant = onGrantShizuku)
            } else {
                Text("Shizuku: granted ✓", style = MaterialTheme.typography.bodyMedium)
            }

            Spacer(Modifier.width(1.dp))
            val ctx = LocalContext.current
            val version = remember(ctx) {
                runCatching {
                    ctx.packageManager.getPackageInfo(ctx.packageName, 0).versionName
                }.getOrNull() ?: ""
            }
            Text(
                "LinuxDrop · version $version",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }

    if (confirmRegen) {
        AlertDialog(
            onDismissRequest = { confirmRegen = false },
            title = { Text("Regenerate key?") },
            text = { Text("A new key will be generated. Other devices on this network will be disconnected and must pair again with the new key.") },
            confirmButton = {
                TextButton(onClick = { confirmRegen = false; onRegenerateKey() }) { Text("Regenerate") }
            },
            dismissButton = { TextButton(onClick = { confirmRegen = false }) { Text("Cancel") } },
        )
    }
}
