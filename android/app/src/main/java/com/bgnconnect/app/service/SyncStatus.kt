package com.bgnconnect.app.service

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update

/**
 * Process-wide, observable sync state shared between [SyncForegroundService] and
 * the Compose UI (no service binding needed). The service writes; the UI collects.
 */
object SyncStatus {

    data class Device(
        val dev: String,
        val name: String,
        val platform: String, // "android" | "linux" | ...
        val self: Boolean,
    )

    data class State(
        val running: Boolean = false,
        val connected: Boolean = false,
        val peerCount: Int = 0,            // other devices in the room (from relay "peers")
        val devices: List<Device> = emptyList(), // populated once roster/presence is available
        val lastSyncAt: Long = 0L,         // last successful sync (epoch ms)
        val lastSyncOutgoing: Boolean = false, // true = we sent, false = we received
        val lastSyncChars: Int = 0,
    )

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state.asStateFlow()

    fun setRunning(running: Boolean) = _state.update {
        if (running) it.copy(running = true)
        else State() // fully reset when the service stops
    }

    fun setConnected(connected: Boolean) = _state.update { it.copy(connected = connected) }

    fun setPeerCount(count: Int) = _state.update { it.copy(peerCount = count) }

    fun setDevices(devices: List<Device>) =
        _state.update { it.copy(devices = devices, peerCount = devices.count { d -> !d.self }) }

    fun setLastSync(outgoing: Boolean, chars: Int) = _state.update {
        it.copy(lastSyncAt = System.currentTimeMillis(), lastSyncOutgoing = outgoing, lastSyncChars = chars)
    }
}
