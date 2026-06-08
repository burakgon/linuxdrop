package com.linuxdrop.app.tether

import android.annotation.SuppressLint
import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothGattCharacteristic
import android.bluetooth.BluetoothGattDescriptor
import android.bluetooth.BluetoothGattServer
import android.bluetooth.BluetoothGattServerCallback
import android.bluetooth.BluetoothGattService
import android.bluetooth.BluetoothManager
import android.bluetooth.BluetoothProfile
import android.bluetooth.le.AdvertiseCallback
import android.bluetooth.le.AdvertiseData
import android.bluetooth.le.AdvertiseSettings
import android.content.Context
import android.os.ParcelUuid
import android.util.Log
import com.linuxdrop.app.crypto.LinuxDropCrypto
import java.security.SecureRandom
import java.util.UUID

/**
 * BLE peripheral exposing the tether service (proto/PROTOCOL.md §8). The laptop reads [UUID_NONCE],
 * writes AEAD-sealed commands to [UUID_COMMAND]; we verify with K_ble + a per-connection
 * [TetherFrame.Verifier] and drive [TetherController]. Status is pushed (sealed) on [UUID_STATUS].
 * Unbonded — the AEAD is the trust boundary, like the rest of the protocol.
 */
@SuppressLint("MissingPermission") // BLUETOOTH_ADVERTISE/CONNECT requested in MainActivity
class TetherGattServer(
    private val context: Context,
    secret: ByteArray,
    private val controller: TetherController,
) {
    private val kBle = LinuxDropCrypto.tetherBleKey(secret)
    private val ssid = LinuxDropCrypto.tetherSsid(secret)
    private val psk = LinuxDropCrypto.tetherPsk(secret)
    private val rng = SecureRandom()

    private var gattServer: BluetoothGattServer? = null
    @Volatile private var sessionNonce = ByteArray(16)
    @Volatile private var verifier: TetherFrame.Verifier? = null

    private val nonceChar = BluetoothGattCharacteristic(
        UUID_NONCE, BluetoothGattCharacteristic.PROPERTY_READ, BluetoothGattCharacteristic.PERMISSION_READ,
    )
    private val commandChar = BluetoothGattCharacteristic(
        UUID_COMMAND, BluetoothGattCharacteristic.PROPERTY_WRITE, BluetoothGattCharacteristic.PERMISSION_WRITE,
    )
    private val statusChar = BluetoothGattCharacteristic(
        UUID_STATUS, BluetoothGattCharacteristic.PROPERTY_NOTIFY, 0,
    ).apply {
        addDescriptor(
            BluetoothGattDescriptor(
                UUID_CCCD,
                BluetoothGattDescriptor.PERMISSION_READ or BluetoothGattDescriptor.PERMISSION_WRITE,
            ),
        )
    }

    fun start() {
        val mgr = context.getSystemService(Context.BLUETOOTH_SERVICE) as BluetoothManager
        val adapter = mgr.adapter ?: run { Log.w(TAG, "no BT adapter"); return }
        if (!adapter.isEnabled) { Log.w(TAG, "BT off; tether wake unavailable"); return }

        val server = mgr.openGattServer(context, callback) ?: run { Log.e(TAG, "openGattServer failed"); return }
        val service = BluetoothGattService(UUID_SERVICE, BluetoothGattService.SERVICE_TYPE_PRIMARY).apply {
            addCharacteristic(nonceChar); addCharacteristic(commandChar); addCharacteristic(statusChar)
        }
        server.addService(service)
        gattServer = server

        adapter.bluetoothLeAdvertiser?.startAdvertising(
            AdvertiseSettings.Builder()
                .setAdvertiseMode(AdvertiseSettings.ADVERTISE_MODE_LOW_POWER)
                .setConnectable(true).build(),
            AdvertiseData.Builder()
                .setIncludeDeviceName(false)
                .addServiceUuid(ParcelUuid(UUID_SERVICE)).build(),
            advCallback,
        )
        Log.i(TAG, "tether GATT server up; ssid=$ssid")
    }

    fun stop() {
        runCatching {
            (context.getSystemService(Context.BLUETOOTH_SERVICE) as BluetoothManager)
                .adapter?.bluetoothLeAdvertiser?.stopAdvertising(advCallback)
        }
        runCatching { gattServer?.close() }
        gattServer = null
    }

    private val advCallback = object : AdvertiseCallback() {
        override fun onStartFailure(errorCode: Int) { Log.e(TAG, "advertise failed: $errorCode") }
    }

    private val callback = object : BluetoothGattServerCallback() {
        override fun onConnectionStateChange(device: BluetoothDevice, status: Int, newState: Int) {
            if (newState == BluetoothProfile.STATE_CONNECTED) {
                // Fresh session nonce + verifier per connection (replay protection).
                sessionNonce = ByteArray(16).also { rng.nextBytes(it) }
                verifier = TetherFrame.Verifier(kBle, sessionNonce)
                Log.i(TAG, "central connected")
            } else if (newState == BluetoothProfile.STATE_DISCONNECTED) {
                verifier = null
            }
        }

        override fun onCharacteristicReadRequest(
            device: BluetoothDevice, requestId: Int, offset: Int, ch: BluetoothGattCharacteristic,
        ) {
            val value = if (ch.uuid == UUID_NONCE) sessionNonce else ByteArray(0)
            gattServer?.sendResponse(device, requestId, 0 /*GATT_SUCCESS*/, offset, value)
        }

        override fun onCharacteristicWriteRequest(
            device: BluetoothDevice, requestId: Int, ch: BluetoothGattCharacteristic,
            preparedWrite: Boolean, responseNeeded: Boolean, offset: Int, value: ByteArray,
        ) {
            if (responseNeeded) gattServer?.sendResponse(device, requestId, 0, offset, null)
            if (ch.uuid != UUID_COMMAND) return
            val cmd = verifier?.open(value) ?: run { Log.w(TAG, "rejected command (auth/replay)"); return }
            when (cmd.opcode) {
                TetherFrame.OP_ENABLE -> controller.enable(ssid, psk) { code -> notifyStatus(device, cmd.opcode, code) }
                TetherFrame.OP_DISABLE -> controller.disable { code -> notifyStatus(device, cmd.opcode, code) }
                TetherFrame.OP_KEEPALIVE -> { controller.keepAlive(); notifyStatus(device, cmd.opcode, TetherResult.OK) }
                else -> Log.w(TAG, "unknown opcode ${cmd.opcode}")
            }
        }

        override fun onDescriptorWriteRequest(
            device: BluetoothDevice, requestId: Int, descriptor: BluetoothGattDescriptor,
            preparedWrite: Boolean, responseNeeded: Boolean, offset: Int, value: ByteArray,
        ) {
            if (responseNeeded) gattServer?.sendResponse(device, requestId, 0, offset, null)
        }
    }

    private fun notifyStatus(device: BluetoothDevice, opcode: Int, result: Int) {
        val frame = TetherFrame.sealStatus(kBle, opcode, result)
        statusChar.value = frame
        runCatching { gattServer?.notifyCharacteristicChanged(device, statusChar, false) }
    }

    companion object {
        private const val TAG = "linuxDropTetherBle"
        val UUID_SERVICE: UUID = UUID.fromString("e3a9f5c0-1d2b-4e3a-9c8d-0a1b2c3d4e5f")
        val UUID_NONCE: UUID = UUID.fromString("e3a9f5c1-1d2b-4e3a-9c8d-0a1b2c3d4e5f")
        val UUID_COMMAND: UUID = UUID.fromString("e3a9f5c2-1d2b-4e3a-9c8d-0a1b2c3d4e5f")
        val UUID_STATUS: UUID = UUID.fromString("e3a9f5c3-1d2b-4e3a-9c8d-0a1b2c3d4e5f")
        val UUID_CCCD: UUID = UUID.fromString("00002902-0000-1000-8000-00805f9b34fb")
    }
}
