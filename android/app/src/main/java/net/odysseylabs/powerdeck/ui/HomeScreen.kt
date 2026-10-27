package net.odysseylabs.powerdeck.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.Add
import androidx.compose.material.icons.rounded.Close
import androidx.compose.material.icons.rounded.PowerSettingsNew
import androidx.compose.material.icons.rounded.RestartAlt
import androidx.compose.material.icons.rounded.Settings
import androidx.compose.material.icons.rounded.SystemUpdate
import androidx.compose.material.icons.rounded.TravelExplore
import androidx.compose.material.icons.rounded.VpnLock
import androidx.compose.material.icons.rounded.Widgets
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.DeviceState

/** Callbacks from the home screen. */
data class HomeActions(
    val open: (Device) -> Unit = {},
    val turnOn: (Device) -> Unit = {},
    val shutdown: (Device) -> Unit = {},
    val restart: (Device) -> Unit = {},
    val cancel: (Device) -> Unit = {},
    val retry: () -> Unit = {},
    val add: () -> Unit = {},
    val settings: () -> Unit = {},
    val connectTailscale: () -> Unit = {},
    val update: () -> Unit = {},
    val addWidget: () -> Unit = {},
    val dismissWidgetTip: () -> Unit = {},
    val hint: (String) -> Unit = {},
)

@Composable
fun HomeScreen(state: UiState, a: HomeActions, snackbar: SnackbarHostState, showWidgetTip: Boolean) {
    val counting = state.states.values.any { (it as? DeviceState.On)?.status?.pending != null }
    val now = rememberNow(counting)
    Scaffold(
        snackbarHost = { SnackbarHost(snackbar) },
        containerColor = MaterialTheme.colorScheme.background,
        topBar = {
            AppBar("PowerDeck") { IconButton(onClick = a.settings) { Icon(Icons.Rounded.Settings, contentDescription = "Settings") } }
        },
        floatingActionButton = {
            ExtendedFloatingActionButton(onClick = a.add, icon = { Icon(Icons.Rounded.Add, null) }, text = { Text("Add device") })
        },
    ) { pad ->
        LazyColumn(
            contentPadding = PaddingValues(start = Margin, end = Margin, top = pad.calculateTopPadding() + 4.dp, bottom = pad.calculateBottomPadding() + 96.dp),
            verticalArrangement = Arrangement.spacedBy(Gap),
        ) {
            if (!state.tailnet) {
                item {
                    Banner(
                        Icons.Rounded.VpnLock, "Tailscale is off",
                        if (state.tailscaleInstalled) "PowerDeck needs it to reach your devices." else "Install it and sign in to reach your devices.",
                        if (state.tailscaleInstalled) "Turn on" else "Get Tailscale", a.connectTailscale, warn = true,
                    )
                }
            }
            state.update?.let { r ->
                item { Banner(Icons.Rounded.SystemUpdate, "Update available", "PowerDeck ${r.version} is ready to install.", "Update", a.update) }
            }
            items(state.devices, key = { it.id }) { d ->
                DeviceCard(d, state.states[d.id], d.id in state.busy, state.tailnet, state.settings.confirmShutdown, now, a)
            }
            val found = state.found.orEmpty()
            if (found.isNotEmpty()) {
                item {
                    Banner(
                        Icons.Rounded.TravelExplore, "Found on your tailnet",
                        found.take(3).joinToString(", ") { it.name } + if (found.size > 3) " and ${found.size - 3} more" else "",
                        "Add", a.add,
                    )
                }
            }
            if (showWidgetTip && state.devices.isNotEmpty()) {
                item {
                    Card(shape = RoundedCornerShape(20.dp), colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer)) {
                        Row(Modifier.padding(start = 16.dp, top = 8.dp, bottom = 8.dp, end = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                            Icon(Icons.Rounded.Widgets, null, tint = MaterialTheme.colorScheme.primary)
                            Spacer(Modifier.width(16.dp))
                            Column(Modifier.weight(1f).clickable(onClick = a.addWidget)) {
                                Text("One-tap buttons", style = MaterialTheme.typography.titleSmall)
                                Text("Add a widget or Quick Settings tile", style = MaterialTheme.typography.bodySmall)
                            }
                            IconButton(onClick = a.dismissWidgetTip) { Icon(Icons.Rounded.Close, contentDescription = "Dismiss") }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun DeviceCard(d: Device, st: DeviceState?, busy: Boolean, online: Boolean, hold: Boolean, now: Long, a: HomeActions) {
    // With Tailscale off nothing can be known; the banner above says why, so the cards stay quiet.
    val tone = if (online) describe(st, now).second else Tone.Unknown
    val extra = if (online) note(st, now) else null
    val pending = (st as? DeviceState.On)?.status?.pending
    Card(
        shape = RoundedCornerShape(24.dp),
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer),
        modifier = Modifier.fillMaxWidth().clickable { a.open(d) },
    ) {
        Column(Modifier.padding(16.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.heightIn(min = 40.dp)) {
                IconTile(d.icon())
                Spacer(Modifier.width(16.dp))
                Column(Modifier.weight(1f)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(d.name, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                        Spacer(Modifier.width(8.dp))
                        StatusDot(tone)
                    }
                    extra?.let { (text, t) ->
                        Text(text, style = MaterialTheme.typography.bodyMedium, maxLines = 2, overflow = TextOverflow.Ellipsis,
                            color = if (t == Tone.Warn) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
                if (busy) CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
            }
            if (online) {
                Row(Modifier.padding(top = 16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Buttons(d, st, pending != null, busy, hold, a)
                }
            }
        }
    }
}

@Composable
private fun RowScope.Buttons(d: Device, st: DeviceState?, pending: Boolean, busy: Boolean, hold: Boolean, a: HomeActions) {
    val canOff = d.hasHelper || d.kvm != null
    when {
        pending -> OutlinedButton(onClick = { a.cancel(d) }, enabled = !busy, modifier = Modifier.weight(1f).height(48.dp), shape = RoundedCornerShape(16.dp)) {
            Text("Cancel")
        }
        st is DeviceState.On -> if (canOff) {
            HoldButton("Shut down", Icons.Rounded.PowerSettingsNew, danger = true, enabled = !busy, hold = hold, modifier = Modifier.weight(1f),
                onHint = { a.hint("Hold to shut down ${d.name}") }) { a.shutdown(d) }
            if (d.hasHelper) {
                HoldButton("Restart", Icons.Rounded.RestartAlt, danger = false, enabled = !busy, hold = hold, tonal = true, modifier = Modifier.weight(1f),
                    onHint = { a.hint("Hold to restart ${d.name}") }) { a.restart(d) }
            }
        }
        st is DeviceState.Problem -> OutlinedButton(onClick = a.retry, modifier = Modifier.weight(1f).height(48.dp), shape = RoundedCornerShape(16.dp)) { Text("Try again") }
        st == DeviceState.Off -> HoldButton("Turn on", Icons.Rounded.PowerSettingsNew, danger = false, enabled = !busy, hold = false, modifier = Modifier.weight(1f)) { a.turnOn(d) }
        else -> Unit
    }
}
