package com.bgnconnect.app.ui.theme

import android.os.Build
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext

private val Teal = Color(0xFF1E6F5C)
private val TealLight = Color(0xFF6FE0C6)

private val LightColors = lightColorScheme(primary = Teal, secondary = Teal)
private val DarkColors = darkColorScheme(primary = TealLight, secondary = TealLight)

/** Material 3 theme with Material You dynamic color on Android 12+, brand teal otherwise. */
@Composable
fun BgnTheme(content: @Composable () -> Unit) {
    val dark = isSystemInDarkTheme()
    val context = LocalContext.current
    val colorScheme = when {
        Build.VERSION.SDK_INT >= 31 -> if (dark) dynamicDarkColorScheme(context) else dynamicLightColorScheme(context)
        dark -> DarkColors
        else -> LightColors
    }
    MaterialTheme(colorScheme = colorScheme, content = content)
}
