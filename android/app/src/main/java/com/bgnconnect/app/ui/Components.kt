package com.bgnconnect.app.ui

import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.BatteryAlert
import androidx.compose.material.icons.filled.Shield
import androidx.compose.material3.Button
import androidx.compose.material3.ElevatedCard
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp

private const val SHIZUKU_PKG = "moe.shizuku.privileged.api"

private fun openUrl(ctx: Context, url: String) =
    ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))

private fun openShizukuInstall(ctx: Context) {
    runCatching { openUrl(ctx, "market://details?id=$SHIZUKU_PKG") }
        .onFailure { openUrl(ctx, "https://shizuku.rikka.app/") }
}

private fun openShizukuApp(ctx: Context) {
    val intent = ctx.packageManager.getLaunchIntentForPackage(SHIZUKU_PKG)
    if (intent != null) ctx.startActivity(intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) else openShizukuInstall(ctx)
}

/** Guides the user through the one-time Shizuku setup (install → start → grant). */
@Composable
fun ShizukuSetupCard(ui: UiModel, onGrant: () -> Unit, modifier: Modifier = Modifier) {
    val ctx = LocalContext.current
    ElevatedCard(modifier = modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                Icon(Icons.Default.Shield, contentDescription = null, tint = MaterialTheme.colorScheme.primary)
                Text("Background permission (Shizuku)", style = MaterialTheme.typography.titleMedium)
            }
            when {
                !ui.shizukuInstalled -> {
                    Text(
                        "To read the clipboard in the background you need to install the Shizuku app once.",
                        style = MaterialTheme.typography.bodyMedium,
                    )
                    Button(onClick = { openShizukuInstall(ctx) }) { Text("Install Shizuku") }
                }
                !ui.shizukuRunning -> {
                    Text(
                        "Shizuku is installed but not running. Open Shizuku and start it via “Wireless debugging”.",
                        style = MaterialTheme.typography.bodyMedium,
                    )
                    Button(onClick = { openShizukuApp(ctx) }) { Text("Open Shizuku") }
                }
                !ui.shizukuGranted -> {
                    Text("Last step: grant bgnconnect the Shizuku permission.", style = MaterialTheme.typography.bodyMedium)
                    Button(onClick = onGrant) { Text("Grant permission") }
                }
                else -> Text("Ready ✓", style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.primary)
            }
        }
    }
}

/** Prompts the user to exempt the app from battery optimization so the OS keeps the sync service alive. */
@Composable
fun BatteryCard(modifier: Modifier = Modifier) {
    val ctx = LocalContext.current
    ElevatedCard(modifier = modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                Icon(Icons.Default.BatteryAlert, contentDescription = null, tint = MaterialTheme.colorScheme.primary)
                Text("Battery optimization", style = MaterialTheme.typography.titleMedium)
            }
            Text(
                "Exempt bgnconnect from battery optimization so sync isn't killed in the background.",
                style = MaterialTheme.typography.bodyMedium,
            )
            Button(onClick = { requestBatteryExemption(ctx) }) { Text("Exempt from battery saver") }
        }
    }
}

private fun requestBatteryExemption(ctx: Context) {
    runCatching {
        ctx.startActivity(
            Intent(android.provider.Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:${ctx.packageName}"))
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
        )
    }.onFailure {
        runCatching {
            ctx.startActivity(
                Intent(android.provider.Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
            )
        }
    }
}
