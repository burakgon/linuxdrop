package com.linuxdrop.app.tether

/** Result codes returned across the Shizuku tether binder. 0 = success. */
object TetherResult {
    const val OK = 0
    const val ERR_NO_CONTEXT = 1
    const val ERR_REFLECTION = 2
    const val ERR_TETHER_FAILED = 3
    const val ERR_TIMEOUT = 4

    fun label(code: Int): String = when (code) {
        OK -> "ok"
        ERR_NO_CONTEXT -> "no-context"
        ERR_REFLECTION -> "reflection-failed"
        ERR_TETHER_FAILED -> "tether-failed (entitlement?)"
        ERR_TIMEOUT -> "timed-out"
        else -> "unknown($code)"
    }
}
