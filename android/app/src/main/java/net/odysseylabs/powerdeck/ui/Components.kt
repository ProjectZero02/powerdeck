package net.odysseylabs.powerdeck.ui

import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.layout.RowScope
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.rounded.ArrowBack
import androidx.compose.material.icons.rounded.DesktopWindows
import androidx.compose.material.icons.rounded.Dns
import androidx.compose.material.icons.rounded.Memory
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.DeviceState
import java.time.Duration
import java.time.Instant

/** "Hold for 3 s to activate": how long Shut down and Restart have to be held. */
const val HOLD_MILLIS = 3000

fun Device.icon(): ImageVector = when (kind) {
    Device.Kind.Pc -> Icons.Rounded.DesktopWindows
    Device.Kind.Server -> Icons.Rounded.Dns
    Device.Kind.Other -> Icons.Rounded.Memory
}

fun Device.kindName(): String = when {
    guests -> "Proxmox server"
    os == "windows" -> "Windows PC"
    os == "linux" -> "Linux"
    else -> "Device"
}

/** "in 45 seconds", "in 2 minutes". */
fun inTime(seconds: Int): String = if (seconds < 90) "in $seconds second${if (seconds == 1) "" else "s"}" else "in ${(seconds + 30) / 60} minutes"

fun uptime(seconds: Long): String {
    val h = seconds / 3600
    val m = (seconds % 3600) / 60
    return when {
        h >= 48 -> "${h / 24} days"
        h > 0 -> "$h h $m min"
        else -> "$m min"
    }
}

/** Dot meanings: green on, grey off, ring unknown, primary in progress, red problem. */
enum class Tone { On, Off, Unknown, Busy, Warn }

/** One line for a device's state, plus its colour. `now` drives the countdown. */
fun describe(state: DeviceState?, now: Long): Pair<String, Tone> = when (state) {
    null, DeviceState.Checking -> "Checking…" to Tone.Unknown
    DeviceState.Off -> "Off" to Tone.Off
    is DeviceState.Problem -> state.message to Tone.Warn
    is DeviceState.On -> {
        val s = state.status
        val p = s?.pending
        when {
            p != null -> {
                val left = Duration.between(Instant.ofEpochMilli(now), p.at).seconds.coerceAtLeast(0)
                "${if (p.action == "restart") "Restarting" else "Shutting down"} in ${left / 60}:${"%02d".format(left % 60)}" to Tone.Busy
            }
            s == null -> "On" to Tone.On
            s.presence?.active == true -> "On · in use${s.presence.user?.let { " by $it" } ?: ""}" to Tone.On
            else -> "On · up ${uptime(s.uptimeSeconds)}" to Tone.On
        }
    }
}

/**
 * The words worth adding next to a device's dot, or null: the dot already says on or off, so only
 * news gets text (in use, a countdown, a problem).
 */
fun note(state: DeviceState?, now: Long): Pair<String, Tone>? = when (state) {
    is DeviceState.Problem -> state.message to Tone.Warn
    is DeviceState.On -> {
        val s = state.status
        val p = s?.pending
        when {
            p != null -> describe(state, now)
            s?.presence?.active == true -> "In use${s.presence.user?.let { " by $it" } ?: ""}" to Tone.On
            else -> null
        }
    }
    else -> null
}

@Composable
fun toneColor(tone: Tone): Color = when (tone) {
    Tone.On -> OnGreen
    Tone.Off, Tone.Unknown -> MaterialTheme.colorScheme.outline
    Tone.Busy -> MaterialTheme.colorScheme.primary
    Tone.Warn -> MaterialTheme.colorScheme.error
}

/** The power-state dot. Screen readers hear the state, since colour alone can't carry it. */
@Composable
fun StatusDot(tone: Tone) {
    val label = when (tone) { Tone.On -> "On"; Tone.Off -> "Off"; Tone.Unknown -> "Unknown"; Tone.Busy -> "Working"; Tone.Warn -> "Problem" }
    val dot = Modifier.size(10.dp).clip(CircleShape)
    Box((if (tone == Tone.Unknown) dot.border(1.5.dp, toneColor(tone), CircleShape) else dot.background(toneColor(tone))).semantics { contentDescription = label })
}

/** The one top bar every screen uses: same height, inset, type and colour. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AppBar(title: String, onBack: (() -> Unit)? = null, actions: @Composable RowScope.() -> Unit = {}) {
    TopAppBar(
        title = { Text(title, maxLines = 1, overflow = TextOverflow.Ellipsis) },
        navigationIcon = { onBack?.let { IconButton(onClick = it) { Icon(Icons.AutoMirrored.Rounded.ArrowBack, contentDescription = "Back") } } },
        actions = actions,
        colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
    )
}

/** Screen-edge margin (Material: 16 dp on phones) and the gap between cards. */
val Margin = 16.dp
val Gap = 12.dp

@Composable
fun IconTile(icon: ImageVector, size: Int = 40) {
    Box(
        Modifier.size(size.dp).clip(RoundedCornerShape((size * 0.3).dp)).background(MaterialTheme.colorScheme.primaryContainer),
        contentAlignment = Alignment.Center,
    ) { Icon(icon, contentDescription = null, tint = MaterialTheme.colorScheme.onPrimaryContainer) }
}

/**
 * A button that has to be held for [holdMillis] (the fill shows progress), so a pocket tap can't shut
 * anything down. A plain tap only shows [onHint]. Accessibility services can activate it directly.
 */
@Composable
fun HoldButton(
    label: String,
    icon: ImageVector,
    danger: Boolean,
    enabled: Boolean,
    modifier: Modifier = Modifier,
    holdMillis: Int = HOLD_MILLIS,
    hold: Boolean = true,
    tonal: Boolean = false,
    onHint: () -> Unit = {},
    onConfirm: () -> Unit,
) {
    val haptics = LocalHapticFeedback.current
    val scope = rememberCoroutineScope()
    val progress = remember { Animatable(0f) }
    val container = when {
        !enabled -> MaterialTheme.colorScheme.surfaceVariant
        danger -> MaterialTheme.colorScheme.errorContainer
        tonal -> MaterialTheme.colorScheme.secondaryContainer
        else -> MaterialTheme.colorScheme.primary
    }
    val content = when {
        !enabled -> MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.5f)
        danger -> MaterialTheme.colorScheme.onErrorContainer
        tonal -> MaterialTheme.colorScheme.onSecondaryContainer
        else -> MaterialTheme.colorScheme.onPrimary
    }
    val fill = when {
        danger -> MaterialTheme.colorScheme.error.copy(alpha = 0.35f)
        tonal -> MaterialTheme.colorScheme.secondary.copy(alpha = 0.3f)
        else -> Color.White.copy(alpha = 0.25f)
    }
    Surface(
        shape = RoundedCornerShape(16.dp),
        color = container,
        contentColor = content,
        modifier = modifier
            .height(48.dp)
            .semantics {
                role = Role.Button
                if (enabled) onClick(label) { onConfirm(); true }
            }
            .pointerInput(enabled, hold) {
                if (!enabled) return@pointerInput
                detectTapGestures(
                    onPress = {
                        if (!hold) {
                            if (tryAwaitRelease()) onConfirm()
                            return@detectTapGestures
                        }
                        val job = scope.launch {
                            progress.snapTo(0f)
                            progress.animateTo(1f, tween(holdMillis, easing = LinearEasing))
                            haptics.performHapticFeedback(HapticFeedbackType.LongPress)
                            onConfirm()
                        }
                        val released = tryAwaitRelease()
                        if (job.isActive) {
                            job.cancel()
                            if (released && progress.value < 0.25f) onHint()
                            scope.launch { progress.animateTo(0f, tween(200)) }
                        } else {
                            scope.launch { progress.snapTo(0f) }
                        }
                    },
                )
            },
    ) {
        Box(contentAlignment = Alignment.CenterStart) {
            Box(Modifier.fillMaxHeight().fillMaxWidth(progress.value).background(fill))
            Row(Modifier.fillMaxWidth().padding(horizontal = 14.dp), horizontalArrangement = Arrangement.Center, verticalAlignment = Alignment.CenterVertically) {
                Icon(icon, contentDescription = null, modifier = Modifier.size(20.dp))
                Spacer(Modifier.width(8.dp))
                Text(label, style = MaterialTheme.typography.labelLarge)
            }
        }
    }
}

@Composable
fun Banner(icon: ImageVector, title: String, text: String, button: String?, onClick: () -> Unit, warn: Boolean = false) {
    val container = if (warn) MaterialTheme.colorScheme.errorContainer else MaterialTheme.colorScheme.secondaryContainer
    val content = if (warn) MaterialTheme.colorScheme.onErrorContainer else MaterialTheme.colorScheme.onSecondaryContainer
    Card(shape = RoundedCornerShape(20.dp), colors = CardDefaults.cardColors(containerColor = container, contentColor = content), modifier = Modifier.fillMaxWidth()) {
        Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(icon, contentDescription = null)
            Spacer(Modifier.width(14.dp))
            Column(Modifier.weight(1f)) {
                Text(title, style = MaterialTheme.typography.titleSmall)
                Text(text, style = MaterialTheme.typography.bodySmall)
            }
            if (button != null) {
                Spacer(Modifier.width(8.dp))
                TextButton(onClick = onClick) { Text(button, color = content) }
            }
        }
    }
}

@Composable
fun SectionTitle(text: String, modifier: Modifier = Modifier) {
    Text(text, style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.primary, modifier = modifier.padding(top = 8.dp, bottom = 4.dp))
}

/** Copies text; [sensitive] keeps it out of Android's clipboard preview (Android 13+). */
fun copyText(ctx: android.content.Context, text: String, sensitive: Boolean = false) {
    val clip = android.content.ClipData.newPlainText("PowerDeck", text)
    if (sensitive) clip.description.extras = android.os.PersistableBundle().apply { putBoolean("android.content.extra.IS_SENSITIVE", true) }
    ctx.getSystemService(android.content.ClipboardManager::class.java)?.setPrimaryClip(clip)
}

/** Ticks once a second while [active], for countdowns. */
@Composable
fun rememberNow(active: Boolean): Long {
    var now by remember { mutableLongStateOf(System.currentTimeMillis()) }
    if (active) {
        LaunchedEffect(Unit) {
            while (true) {
                now = System.currentTimeMillis()
                delay(1000)
            }
        }
    }
    return now
}
