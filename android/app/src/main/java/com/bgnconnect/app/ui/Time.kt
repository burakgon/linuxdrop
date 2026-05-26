package com.bgnconnect.app.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import kotlinx.coroutines.delay

/** A clock that ticks every 30s so relative timestamps stay fresh without manual refresh. */
@Composable
fun liveNow(): Long {
    var now by remember { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(Unit) {
        while (true) {
            delay(30_000L)
            now = System.currentTimeMillis()
        }
    }
    return now
}

/** Short relative time, e.g. "just now", "3 min ago", "2 h ago", "1 d ago". */
fun relTime(atMs: Long, now: Long): String {
    val d = now - atMs
    return when {
        d < 60_000L -> "just now"
        d < 3_600_000L -> "${d / 60_000L} min ago"
        d < 86_400_000L -> "${d / 3_600_000L} h ago"
        else -> "${d / 86_400_000L} d ago"
    }
}
