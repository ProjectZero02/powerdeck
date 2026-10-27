package net.odysseylabs.powerdeck.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.DesktopWindows
import androidx.compose.material.icons.rounded.Dns
import androidx.compose.material.icons.rounded.PowerSettingsNew
import androidx.compose.material.icons.rounded.RestartAlt
import androidx.compose.material.icons.rounded.Settings
import androidx.compose.material.icons.rounded.SystemUpdate
import androidx.compose.material.icons.rounded.VpnLock
import androidx.compose.material.icons.rounded.Widgets
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.delay
import net.odysseylabs.powerdeck.data.PcState
import net.odysseylabs.powerdeck.data.ServerState
import java.time.Duration
import java.time.Instant

/** Callbacks from the home screen. */
data class HomeActions(
    val pcOn: () -> Unit = {},
    val pcShutdown: () -> Unit = {},
    val pcRestart: () -> Unit = {},
    val pcCancel: () -> Unit = {},
    val serverOn: () -> Unit = {},
    val serverOff: () -> Unit = {},
    val setUpServer: () -> Unit = {},
    val retry: () -> Unit = {},
    val openSettings: () -> Unit = {},
    val connectTailscale: () -> Unit = {},
    val update: () -> Unit = {},
    val addWidget: () -> Unit = {},
)

private enum class Tone { On, Off, Busy, Warn }

private data class Confirm(val title: String, val text: String, val action: String, val run: () -> Unit)

@Composable
fun HomeScreen(state: UiState, actions: HomeActions, snackbar: SnackbarHostState, showWidgetTip: Boolean) {
    val config = state.config ?: return
    var confirm by remember { mutableStateOf<Confirm?>(null) }
    fun guarded(c: Confirm) = if (config.confirmShutdown) confirm = c else c.run()
    val pcName = config.pc?.name ?: "PC"

    Scaffold(snackbarHost = { SnackbarHost(snackbar) }, containerColor = MaterialTheme.colorScheme.background) { pad ->
        LazyColumn(
            contentPadding = PaddingValues(start = 20.dp, end = 20.dp, top = pad.calculateTopPadding() + 12.dp, bottom = pad.calculateBottomPadding() + 24.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            item {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Column(Modifier.weight(1f)) {
                        Text("PowerDeck", style = MaterialTheme.typography.headlineMedium, fontWeight = FontWeight.SemiBold)
                        Text("Your PC and server, anywhere", style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    IconButton(onClick = actions.openSettings) { Icon(Icons.Rounded.Settings, contentDescription = "Settings") }
                }
            }
            if (!state.tailnet) {
                item {
                    Banner(
                        icon = Icons.Rounded.VpnLock,
                        title = "Tailscale is off",
                        text = if (state.tailscaleInstalled) "PowerDeck reaches your devices through Tailscale." else "Install Tailscale and sign in to reach your devices.",
                        button = if (state.tailscaleInstalled) "Turn on" else "Get Tailscale",
                        onClick = actions.connectTailscale,
                        warn = true,
                    )
                }
            }
            state.update?.let { r ->
                item { Banner(Icons.Rounded.SystemUpdate, "Update available", "PowerDeck ${r.version} is ready to install.", "Update", actions.update) }
            }
            item { PcCard(pcName, state.pc, Target.Pc in state.busy, state.tailnet, actions, ::guarded) }
            item { ServerCard(config.proxmox != null, state.server, Target.Server in state.busy, state.tailnet, actions, ::guarded) }
            if (showWidgetTip) {
                item {
                    Banner(Icons.Rounded.Widgets, "One-tap buttons", "Add PowerDeck to your home screen, or add its tiles to Quick Settings.", "Add widget", actions.addWidget)
                }
            }
        }
    }

    confirm?.let { c ->
        AlertDialog(
            onDismissRequest = { confirm = null },
            title = { Text(c.title) },
            text = { Text(c.text) },
            confirmButton = {
                Button(
                    onClick = { confirm = null; c.run() },
                    colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error),
                ) { Text(c.action) }
            },
            dismissButton = { TextButton(onClick = { confirm = null }) { Text("Cancel") } },
        )
    }
}

private fun uptime(seconds: Long): String {
    val h = seconds / 3600
    val m = (seconds % 3600) / 60
    return when {
        h >= 48 -> "${h / 24} days"
        h > 0 -> "$h h $m min"
        else -> "$m min"
    }
}

@Composable
private fun PcCard(name: String, pc: PcState, busy: Boolean, online: Boolean, a: HomeActions, confirm: (Confirm) -> Unit) {
    val pending = (pc as? PcState.On)?.pending
    var now by remember { mutableLongStateOf(System.currentTimeMillis()) }
    if (pending != null) LaunchedEffect(pending) { while (true) { now = System.currentTimeMillis(); delay(1000) } }
    val (status, tone) = when (pc) {
        PcState.Checking -> "Checking…" to Tone.Busy
        PcState.Off -> "Off" to Tone.Off
        is PcState.Problem -> pc.message to Tone.Warn
        is PcState.On -> if (pending != null) {
            val left = Duration.between(Instant.ofEpochMilli(now), pending.at).seconds.coerceAtLeast(0)
            val verb = if (pending.action == "restart") "Restarting" else "Shutting down"
            "$verb in ${left / 60}:${"%02d".format(left % 60)}" to Tone.Busy
        } else {
            "On · up ${uptime(pc.uptimeSeconds)}" to Tone.On
        }
    }
    DeviceCard(Icons.Rounded.DesktopWindows, name, "Gaming PC", status, tone, busy) {
        when {
            !online -> Unit // the Tailscale banner offers the fix
            pending != null -> OutlinedButton(onClick = a.pcCancel, enabled = !busy, modifier = Modifier.weight(1f)) { Text("Cancel") }
            pc is PcState.On -> {
                PowerButton("Shut down", stop = true, enabled = !busy, modifier = Modifier.weight(1f)) {
                    confirm(Confirm("Shut down $name?", "Windows closes all apps in 30 seconds. Unsaved work is lost.", "Shut down", a.pcShutdown))
                }
                FilledTonalButton(onClick = {
                    confirm(Confirm("Restart $name?", "Windows closes all apps and restarts in 30 seconds.", "Restart", a.pcRestart))
                }, enabled = !busy) {
                    Icon(Icons.Rounded.RestartAlt, contentDescription = null, modifier = Modifier.size(18.dp))
                    Spacer(Modifier.width(6.dp))
                    Text("Restart")
                }
            }
            pc is PcState.Problem -> OutlinedButton(onClick = a.retry, modifier = Modifier.weight(1f)) { Text("Try again") }
            else -> PowerButton("Turn on", stop = false, enabled = !busy && pc !is PcState.Checking, modifier = Modifier.weight(1f), onClick = a.pcOn)
        }
    }
}

@Composable
private fun ServerCard(hasProxmox: Boolean, server: ServerState, busy: Boolean, online: Boolean, a: HomeActions, confirm: (Confirm) -> Unit) {
    val (status, tone) = when (server) {
        ServerState.Checking -> "Checking…" to Tone.Busy
        ServerState.NotSetUp -> "Connect your NanoKVM to control the server" to Tone.Warn
        ServerState.Off -> "Off" to Tone.Off
        is ServerState.Problem -> server.message to Tone.Warn
        is ServerState.On -> when (server.proxmoxReady) {
            true -> "On · Proxmox ready" to Tone.On
            false -> "On · starting up…" to Tone.Busy
            null -> "On" to Tone.On
        }
    }
    DeviceCard(Icons.Rounded.Dns, "Server", if (hasProxmox) "Proxmox · via NanoKVM" else "via NanoKVM", status, tone, busy) {
        if (online) when (server) {
            ServerState.NotSetUp -> Button(onClick = a.setUpServer, modifier = Modifier.weight(1f)) { Text("Set up") }
            is ServerState.On -> PowerButton("Shut down", stop = true, enabled = !busy, modifier = Modifier.weight(1f)) {
                confirm(
                    Confirm(
                        "Shut down the server?",
                        "Proxmox shuts every VM down cleanly first (games, media, monitoring and Claude), then powers off. It takes 1 to 3 minutes.",
                        "Shut down",
                        a.serverOff,
                    ),
                )
            }
            is ServerState.Problem -> OutlinedButton(onClick = a.retry, modifier = Modifier.weight(1f)) { Text("Try again") }
            else -> PowerButton("Turn on", stop = false, enabled = !busy && server != ServerState.Checking, modifier = Modifier.weight(1f), onClick = a.serverOn)
        }
    }
}

@Composable
private fun PowerButton(label: String, stop: Boolean, enabled: Boolean, modifier: Modifier = Modifier, onClick: () -> Unit) {
    val colors = if (stop) {
        ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.errorContainer, contentColor = MaterialTheme.colorScheme.onErrorContainer)
    } else {
        ButtonDefaults.buttonColors()
    }
    Button(onClick = onClick, enabled = enabled, colors = colors, modifier = modifier.height(48.dp)) {
        Icon(Icons.Rounded.PowerSettingsNew, contentDescription = null, modifier = Modifier.size(20.dp))
        Spacer(Modifier.width(8.dp))
        Text(label, fontWeight = FontWeight.SemiBold)
    }
}

@Composable
private fun DeviceCard(
    icon: ImageVector,
    title: String,
    subtitle: String,
    status: String,
    tone: Tone,
    busy: Boolean,
    buttons: @Composable androidx.compose.foundation.layout.RowScope.() -> Unit,
) {
    val dot = when (tone) {
        Tone.On -> OnGreen
        Tone.Off -> MaterialTheme.colorScheme.outline
        Tone.Busy -> MaterialTheme.colorScheme.primary
        Tone.Warn -> MaterialTheme.colorScheme.error
    }
    Card(
        shape = RoundedCornerShape(24.dp),
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Column(Modifier.padding(20.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Box(
                    Modifier.size(48.dp).clip(RoundedCornerShape(14.dp)).background(MaterialTheme.colorScheme.primaryContainer),
                    contentAlignment = Alignment.Center,
                ) { Icon(icon, contentDescription = null, tint = MaterialTheme.colorScheme.onPrimaryContainer) }
                Spacer(Modifier.width(14.dp))
                Column(Modifier.weight(1f)) {
                    Text(title, style = MaterialTheme.typography.titleLarge, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(subtitle, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                if (busy) CircularProgressIndicator(Modifier.size(22.dp), strokeWidth = 2.5.dp)
            }
            Spacer(Modifier.height(16.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.size(10.dp).clip(CircleShape).background(dot))
                Spacer(Modifier.width(8.dp))
                Text(status, style = MaterialTheme.typography.bodyLarge, color = if (tone == Tone.Warn) MaterialTheme.colorScheme.error else Color.Unspecified)
            }
            Row(Modifier.padding(top = 16.dp), horizontalArrangement = Arrangement.spacedBy(10.dp), verticalAlignment = Alignment.CenterVertically, content = buttons)
        }
    }
}

@Composable
fun Banner(icon: ImageVector, title: String, text: String, button: String, onClick: () -> Unit, warn: Boolean = false) {
    val container = if (warn) MaterialTheme.colorScheme.errorContainer else MaterialTheme.colorScheme.secondaryContainer
    val content = if (warn) MaterialTheme.colorScheme.onErrorContainer else MaterialTheme.colorScheme.onSecondaryContainer
    Card(shape = RoundedCornerShape(20.dp), colors = CardDefaults.cardColors(containerColor = container, contentColor = content)) {
        Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(icon, contentDescription = null)
            Spacer(Modifier.width(14.dp))
            Column(Modifier.weight(1f)) {
                Text(title, style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold)
                Text(text, style = MaterialTheme.typography.bodySmall)
            }
            Spacer(Modifier.width(8.dp))
            TextButton(onClick = onClick) { Text(button, color = content, fontWeight = FontWeight.SemiBold) }
        }
    }
}
