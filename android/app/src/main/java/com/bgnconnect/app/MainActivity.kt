package com.bgnconnect.app

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
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
import com.bgnconnect.app.ui.BgnApp
import com.bgnconnect.app.ui.MainViewModel
import com.bgnconnect.app.ui.theme.BgnTheme
import rikka.shizuku.Shizuku

class MainActivity : ComponentActivity() {

    private val vm: MainViewModel by viewModels()

    private val shizukuListener = Shizuku.OnRequestPermissionResultListener { _, _ -> vm.refresh() }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        Shizuku.addRequestPermissionResultListener(shizukuListener)
        maybeRequestNotifications()
        setContent {
            BgnTheme {
                Surface(modifier = Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    BgnApp(vm)
                }
            }
        }
        handleDeepLink(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handleDeepLink(intent)
    }

    /** Pair via a bgnconnect:// link (QR alternative): am start -a VIEW -d "bgnconnect://pair?...". */
    private fun handleDeepLink(intent: Intent?) {
        val data = intent?.data ?: return
        if (data.scheme == "bgnconnect") {
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
}
