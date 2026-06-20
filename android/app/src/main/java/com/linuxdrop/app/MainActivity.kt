package com.linuxdrop.app

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import androidx.core.content.IntentCompat
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.viewModels
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.ui.Modifier
import androidx.core.app.ActivityCompat
import androidx.core.content.ContextCompat
import com.linuxdrop.app.service.SyncForegroundService
import com.linuxdrop.app.ui.LinuxDropApp
import com.linuxdrop.app.ui.MainViewModel
import com.linuxdrop.app.ui.theme.LinuxDropTheme
import rikka.shizuku.Shizuku

class MainActivity : ComponentActivity() {

    private val vm: MainViewModel by viewModels()

    private val shizukuListener = Shizuku.OnRequestPermissionResultListener { _, _ -> vm.refresh() }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        if (consumeTextShare(intent)) return // a plain text share: send it and finish, no UI to flash
        Shizuku.addRequestPermissionResultListener(shizukuListener)
        maybeRequestNotifications()
        maybeRequestBtPermissions()
        setContent {
            LinuxDropTheme {
                Surface(modifier = Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    LinuxDropApp(vm)
                }
            }
        }
        handleDeepLink(intent)
        handleSendIntent(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        if (consumeTextShare(intent)) return
        handleDeepLink(intent)
        handleSendIntent(intent)
    }

    /** A plain text share (no file): broadcast it to the paired devices as a clip, confirm, and finish.
     *  Returns true if it consumed the intent, so the caller stops (no UI flash). */
    private fun consumeTextShare(intent: Intent?): Boolean {
        if (intent?.action != Intent.ACTION_SEND || intent.hasExtra(Intent.EXTRA_STREAM)) return false
        val text = intent.getCharSequenceExtra(Intent.EXTRA_TEXT)?.toString()
        if (text.isNullOrEmpty()) return false
        SyncForegroundService.shareText(this, text)
        Toast.makeText(this, "Shared to your devices", Toast.LENGTH_SHORT).show()
        finish()
        return true
    }

    /** Files shared into the app via the OS Share sheet → await a device choice in the UI. */
    private fun handleSendIntent(intent: Intent?) {
        intent ?: return
        val uris = when (intent.action) {
            Intent.ACTION_SEND ->
                listOfNotNull(IntentCompat.getParcelableExtra(intent, Intent.EXTRA_STREAM, Uri::class.java))
            Intent.ACTION_SEND_MULTIPLE ->
                IntentCompat.getParcelableArrayListExtra(intent, Intent.EXTRA_STREAM, Uri::class.java) ?: emptyList()
            else -> emptyList()
        }
        if (uris.isNotEmpty()) vm.setPendingShares(uris)
    }

    /** Pair via a linuxdrop:// link (QR alternative): am start -a VIEW -d "linuxdrop://pair?...". */
    private fun handleDeepLink(intent: Intent?) {
        val data = intent?.data ?: return
        if (data.scheme == "linuxdrop") {
            val ok = vm.joinWith(data.toString())
            Toast.makeText(this, if (ok) "Joined the network ✓" else "Invalid link", Toast.LENGTH_SHORT).show()
        }
    }

    override fun onResume() {
        super.onResume()
        vm.refresh() // re-read Shizuku state when returning from the Shizuku app
    }

    override fun onDestroy() {
        Shizuku.removeRequestPermissionResultListener(shizukuListener)
        super.onDestroy()
    }

    private fun maybeRequestNotifications() {
        if (Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            ActivityCompat.requestPermissions(this, arrayOf(Manifest.permission.POST_NOTIFICATIONS), 4002)
        }
    }

    /** BLE advertise/connect — needed so the phone can be woken over BLE to enable the hotspot (Android 12+). */
    private fun maybeRequestBtPermissions() {
        if (Build.VERSION.SDK_INT < 31) return
        val needed = arrayOf(
            Manifest.permission.BLUETOOTH_ADVERTISE,
            Manifest.permission.BLUETOOTH_CONNECT,
        ).filter { ContextCompat.checkSelfPermission(this, it) != PackageManager.PERMISSION_GRANTED }
        if (needed.isNotEmpty()) ActivityCompat.requestPermissions(this, needed.toTypedArray(), 4003)
    }
}
