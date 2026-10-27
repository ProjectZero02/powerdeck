package net.odysseylabs.powerdeck.ui

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

val Brand = Color(0xFF5B4CF0)
val BrandEnd = Color(0xFF9B5CF6)
val OnGreen = Color(0xFF12A150)

private val Light = lightColorScheme(
    primary = Brand,
    secondary = Color(0xFF7C5CF6),
    tertiary = Color(0xFF0E9F6E),
    background = Color(0xFFF6F7FB),
    surface = Color(0xFFFFFFFF),
    surfaceContainer = Color(0xFFF0F1F8),
)

private val Dark = darkColorScheme(
    primary = Color(0xFF9D91FF),
    secondary = Color(0xFFB69CFF),
    tertiary = Color(0xFF3CCF7A),
    background = Color(0xFF0F1020),
    surface = Color(0xFF15172A),
    surfaceContainer = Color(0xFF1C1E34),
)

/** Uses the wallpaper colours on Android 12+ (Material You), the PowerDeck palette otherwise. */
@Composable
fun PowerDeckTheme(dynamic: Boolean = true, content: @Composable () -> Unit) {
    val dark = isSystemInDarkTheme()
    val scheme = when {
        dynamic && Build.VERSION.SDK_INT >= Build.VERSION_CODES.S ->
            if (dark) dynamicDarkColorScheme(LocalContext.current) else dynamicLightColorScheme(LocalContext.current)
        dark -> Dark
        else -> Light
    }
    MaterialTheme(colorScheme = scheme, content = content)
}
