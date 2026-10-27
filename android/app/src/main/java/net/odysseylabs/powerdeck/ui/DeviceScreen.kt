package net.odysseylabs.powerdeck.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.rounded.ArrowBack
import androidx.compose.material.icons.rounded.CheckCircle
import androidx.compose.material.icons.rounded.ContentCopy
import androidx.compose.material.icons.rounded.Delete
import androidx.compose.material.icons.rounded.ErrorOutline
import androidx.compose.material.icons.rounded.Info
import androidx.compose.material.icons.rounded.MoreVert
import androidx.compose.material.icons.rounded.PowerSettingsNew
import androidx.compose.material.icons.rounded.RestartAlt
import androidx.compose.material.icons.rounded.SystemUpdateAlt
import androidx.compose.material.icons.rounded.WarningAmber
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Checkbox
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.ListItemDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TimePicker
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.rememberTimePickerState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.unit.dp
import net.odysseylabs.powerdeck.data.Check
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.DeviceState
import net.odysseylabs.powerdeck.data.Event
import net.odysseylabs.powerdeck.data.KeyInfo
import net.odysseylabs.powerdeck.data.Peer
import net.odysseylabs.powerdeck.data.Schedule
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle

data class DeviceActions(
    val back: () -> Unit = {},
    val turnOn: () -> Unit = {},
    val shutdown: (installUpdates: Boolean) -> Unit = {},
    val restart: () -> Unit = {},
    val cancel: () -> Unit = {},
    val forceOff: () -> Unit = {},
    val fix: (Check) -> Unit = {},
    val addSchedule: (action: String, time: String, days: List<String>, skipIfInUse: Boolean) -> Unit = { _, _, _, _ -> },
    val removeSchedule: (Device, Schedule) -> Unit = { _, _ -> },
    val loadKeys: () -> Unit = {},
    val addKey: (name: String, power: Boolean, devices: List<String>) -> Unit = { _, _, _ -> },
    val removeKey: (KeyInfo) -> Unit = {},
    val clearNewKey: () -> Unit = {},
    val linkKvm: (deviceId: String?, newName: String, peer: Peer, user: String, password: String, done: (Boolean) -> Unit) -> Unit = { _, _, _, _, _, _ -> },
    val unlinkKvm: () -> Unit = {},
    val setTile: (Int?) -> Unit = {},
    val pinShortcut: () -> Unit = {},
    val rename: (String) -> Unit = {},
    val remove: () -> Unit = {},
    val hint: (String) -> Unit = {},
)

private val DAYS = listOf("mon", "tue", "wed", "thu", "fri", "sat", "sun")
private val timeFmt = DateTimeFormatter.ofLocalizedDateTime(FormatStyle.SHORT).withZone(ZoneId.systemDefault())

@Composable
private fun Section(title: String, content: @Composable () -> Unit) {
    Column(Modifier.fillMaxWidth()) {
        SectionTitle(title)
        Card(shape = RoundedCornerShape(20.dp), colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer), modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.padding(vertical = 4.dp)) { content() }
        }
    }
}

@Composable
private fun Row2(title: String, sub: String? = null, icon: (@Composable () -> Unit)? = null, trailing: (@Composable () -> Unit)? = null) {
    ListItem(
        headlineContent = { Text(title) },
        supportingContent = sub?.let { { Text(it) } },
        leadingContent = icon,
        trailingContent = trailing,
        colors = ListItemDefaults.colors(containerColor = Color.Transparent),
    )
}

@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable
fun DeviceScreen(d: Device, state: UiState, detail: Detail, a: DeviceActions, snackbar: SnackbarHostState) {
    val st = state.states[d.id]
    val status = (st as? DeviceState.On)?.status
    val now = rememberNow(status?.pending != null)
    val (line, tone) = describe(st, now)
    val busy = d.id in state.busy
    val hold = state.settings.confirmShutdown
    var menu by remember { mutableStateOf(false) }
    var confirmRemove by remember { mutableStateOf(false) }
    var confirmForce by remember { mutableStateOf(false) }
    var renaming by remember { mutableStateOf(false) }
    var scheduling by remember { mutableStateOf(false) }
    var addingKey by remember { mutableStateOf(false) }
    var linkKvm by remember { mutableStateOf<Peer?>(null) }

    Scaffold(
        snackbarHost = { SnackbarHost(snackbar) },
        containerColor = MaterialTheme.colorScheme.background,
        topBar = {
            TopAppBar(
                title = { Text(d.name) },
                navigationIcon = { IconButton(onClick = a.back) { Icon(Icons.AutoMirrored.Rounded.ArrowBack, contentDescription = "Back") } },
                actions = {
                    IconButton(onClick = { menu = true }) { Icon(Icons.Rounded.MoreVert, contentDescription = "More") }
                    DropdownMenu(menu, onDismissRequest = { menu = false }) {
                        DropdownMenuItem(text = { Text("Rename") }, onClick = { menu = false; renaming = true })
                        DropdownMenuItem(text = { Text("Add to home screen") }, onClick = { menu = false; a.pinShortcut() })
                        if (d.kvm != null) DropdownMenuItem(text = { Text("Force off (hold power 12 s)") }, onClick = { menu = false; confirmForce = true })
                        DropdownMenuItem(text = { Text("Remove from this phone") }, onClick = { menu = false; confirmRemove = true })
                    }
                },
            )
        },
    ) { pad ->
        Column(Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            // ---- State and power
            Card(shape = RoundedCornerShape(24.dp), colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer), modifier = Modifier.fillMaxWidth()) {
                Column(Modifier.padding(18.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        IconTile(d.icon(), 56)
                        Spacer(Modifier.width(14.dp))
                        Column(Modifier.weight(1f)) {
                            Text(d.subtitle(), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                StatusDot(tone)
                                Spacer(Modifier.width(8.dp))
                                Text(line, style = MaterialTheme.typography.titleMedium, color = if (tone == Tone.Warn) MaterialTheme.colorScheme.error else Color.Unspecified)
                            }
                        }
                    }
                    status?.presence?.takeIf { it.active }?.let {
                        Spacer(Modifier.height(10.dp))
                        Text(it.describe(d.name), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    status?.guestsRunning?.takeIf { it > 0 }?.let { n ->
                        Spacer(Modifier.height(6.dp))
                        Text("$n VM${if (n == 1) "" else "s"} running: ${status.guestNames.joinToString(", ")}. A shutdown stops them cleanly first.",
                            style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    if (state.tailnet) Row(Modifier.padding(top = 14.dp), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                        when {
                            status?.pending != null -> OutlinedButton(onClick = a.cancel, enabled = !busy, modifier = Modifier.weight(1f).height(52.dp), shape = RoundedCornerShape(16.dp)) {
                                Text("Cancel", fontWeight = FontWeight.SemiBold)
                            }
                            st is DeviceState.On -> {
                                if (d.hasHelper || d.kvm != null) HoldButton("Shut down", Icons.Rounded.PowerSettingsNew, true, !busy, Modifier.weight(1f), hold = hold,
                                    onHint = { a.hint("Hold to shut down ${d.name}") }) { a.shutdown(false) }
                                if (d.hasHelper) HoldButton("Restart", Icons.Rounded.RestartAlt, false, !busy, Modifier.weight(1f), hold = hold, tonal = true,
                                    onHint = { a.hint("Hold to restart ${d.name}") }, onConfirm = a.restart)
                            }
                            st == DeviceState.Off -> HoldButton("Turn on", Icons.Rounded.PowerSettingsNew, false, !busy, Modifier.weight(1f), hold = false, onConfirm = a.turnOn)
                            else -> Unit
                        }
                    }
                    if (status?.updatesPending == true && status.pending == null) {
                        TextButton(onClick = { a.shutdown(true) }, enabled = !busy) {
                            Icon(Icons.Rounded.SystemUpdateAlt, null, Modifier.size(18.dp))
                            Spacer(Modifier.width(6.dp))
                            Text("Update and shut down")
                        }
                    }
                }
            }

            // ---- Setup checks
            val checks = status?.checks.orEmpty()
            if (checks.isNotEmpty()) Section("Setup") {
                checks.sortedBy { if (it.problem) 0 else if (it.level == "info") 1 else 2 }.forEach { c ->
                    Row2(c.title, c.detail, icon = {
                        when {
                            c.ok -> Icon(Icons.Rounded.CheckCircle, null, tint = OnGreen)
                            c.level == "error" -> Icon(Icons.Rounded.ErrorOutline, null, tint = MaterialTheme.colorScheme.error)
                            c.level == "warn" -> Icon(Icons.Rounded.WarningAmber, null, tint = MaterialTheme.colorScheme.error)
                            else -> Icon(Icons.Rounded.Info, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }, trailing = if (c.fixable && c.problem) ({ TextButton(onClick = { a.fix(c) }, enabled = !busy) { Text("Fix") } }) else null)
                }
            }

            // ---- Schedules
            if (d.hasHelper) Section("Schedules") {
                val list = detail.schedules
                if (list == null) Row2("Loading…")
                else if (list.isEmpty()) Row2("No schedules", "Shut down every night, or wake up before you get home.")
                list?.forEach { (host, s) ->
                    Row2(
                        "${s.action.replaceFirstChar { it.uppercase() }} at ${s.time}",
                        buildList {
                            add(if (s.days.size == 7) "Every day" else s.days.joinToString(" ") { it.replaceFirstChar(Char::uppercase) })
                            if (s.action != "wake" && s.skipIfInUse) add("skipped while in use")
                            if (host.id != d.id) add("kept by ${host.name}")
                        }.joinToString(" · "),
                        trailing = { IconButton(onClick = { a.removeSchedule(host, s) }) { Icon(Icons.Rounded.Delete, contentDescription = "Remove") } },
                    )
                }
                TextButton(onClick = { scheduling = true }, modifier = Modifier.padding(start = 8.dp)) { Text("Add schedule") }
            }

            // ---- Power button (KVM)
            Section("Power button") {
                val k = d.kvm
                if (k != null) {
                    Row2(k.name, "${k.address} · signed in as ${k.user}", trailing = { TextButton(onClick = a.unlinkKvm) { Text("Unlink") } })
                } else {
                    val kvms = state.found.orEmpty().filter { it.isKvm }
                    Row2("No KVM linked", if (kvms.isEmpty()) "A NanoKVM wired to the power button can turn it on even when Wake-on-LAN can't." else null)
                    kvms.forEach { p -> TextButton(onClick = { linkKvm = p }, modifier = Modifier.padding(start = 8.dp)) { Text("Link ${p.name}") } }
                }
            }

            // ---- Quick Settings
            Section("Quick Settings tile") {
                val slot = state.tiles.indexOf(d.id).takeIf { it >= 0 }
                FlowRow(Modifier.padding(horizontal = 16.dp, vertical = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    (0 until state.tiles.size).forEach { i ->
                        val other = state.tiles[i]?.let { id -> state.devices.firstOrNull { it.id == id }?.name }
                        FilterChip(
                            selected = slot == i,
                            onClick = { a.setTile(if (slot == i) null else i) },
                            label = { Text("Tile ${i + 1}${if (other != null && slot != i) " ($other)" else ""}") },
                        )
                    }
                }
            }

            // ---- Agents
            if (d.hasHelper) Section("AI agents and scripts") {
                Row2("Let an agent use PowerDeck", "Through this device (best: one that's always on). Agents can't force, and always leave a 60-second countdown.")
                detail.keys?.forEach { k ->
                    Row2(k.name, k.scopes.joinToString(", ") + if (k.devices.isNotEmpty()) " · ${k.devices.joinToString()}" else "",
                        trailing = { IconButton(onClick = { a.removeKey(k) }) { Icon(Icons.Rounded.Delete, contentDescription = "Remove") } })
                }
                Row(Modifier.padding(start = 8.dp)) {
                    if (detail.keys == null) TextButton(onClick = a.loadKeys) { Text("Show keys") }
                    TextButton(onClick = { addingKey = true }) { Text("Connect an agent") }
                }
            }

            // ---- History
            if (d.hasHelper) Section("History") {
                val ev = detail.events
                when {
                    ev == null && detail.error != null -> Row2("Not available", detail.error)
                    ev == null -> Row2("Loading…")
                    ev.isEmpty() -> Row2("Nothing yet")
                    else -> ev.take(25).forEach { e -> EventRow(e, d) }
                }
            }
            Spacer(Modifier.height(32.dp))
        }
    }

    if (renaming) {
        var name by rememberSaveable { mutableStateOf(d.name) }
        AlertDialog(
            onDismissRequest = { renaming = false },
            title = { Text("Rename") },
            text = { OutlinedTextField(value = name, onValueChange = { name = it.take(40) }, singleLine = true) },
            confirmButton = { Button(onClick = { renaming = false; a.rename(name) }) { Text("Save") } },
            dismissButton = { TextButton(onClick = { renaming = false }) { Text("Cancel") } },
        )
    }
    if (confirmRemove) {
        AlertDialog(
            onDismissRequest = { confirmRemove = false },
            title = { Text("Remove ${d.name}?") },
            text = { Text("It disappears from this phone. The helper stays installed; to uninstall it, use the device's own uninstaller.") },
            confirmButton = { Button(onClick = { confirmRemove = false; a.remove() }) { Text("Remove") } },
            dismissButton = { TextButton(onClick = { confirmRemove = false }) { Text("Cancel") } },
        )
    }
    if (confirmForce) {
        AlertDialog(
            onDismissRequest = { confirmForce = false },
            title = { Text("Force ${d.name} off?") },
            text = { Text("Holds the power button for 12 seconds, like pulling the plug. Unsaved work is lost and disks may need checking. Use only if it's frozen.") },
            confirmButton = {
                Button(onClick = { confirmForce = false; a.forceOff() }, colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error)) { Text("Force off") }
            },
            dismissButton = { TextButton(onClick = { confirmForce = false }) { Text("Cancel") } },
        )
    }
    if (scheduling) ScheduleDialog(d, onSave = { act, t, days, skip -> scheduling = false; a.addSchedule(act, t, days, skip) }, onDismiss = { scheduling = false })
    if (addingKey) KeyDialog(d, state.devices, onCreate = { n, p, devs -> a.addKey(n, p, devs); addingKey = false }, onDismiss = { addingKey = false })
    detail.newKey?.let { NewKeyDialog(d, it, a.clearNewKey) }
    linkKvm?.let { p -> KvmDialog(p, state.devices, onLink = a.linkKvm, onDismiss = { linkKvm = null }, fixedDevice = d) }
}

@Composable
private fun EventRow(e: Event, d: Device) {
    val who = if (d.me != null && e.who == d.me) "You" else e.who
    Row2("$who: ${e.action}", "${timeFmt.format(e.at)} · ${e.result}")
}

@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable
private fun ScheduleDialog(d: Device, onSave: (String, String, List<String>, Boolean) -> Unit, onDismiss: () -> Unit) {
    var action by rememberSaveable { mutableStateOf("shutdown") }
    var days by rememberSaveable { mutableStateOf(DAYS) }
    var skip by rememberSaveable { mutableStateOf(true) }
    val time = rememberTimePickerState(initialHour = 1, initialMinute = 0)
    val actions = listOf("shutdown" to "Shut down", "restart" to "Restart", "wake" to "Turn on")
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Schedule for ${d.name}") },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState())) {
                SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth()) {
                    actions.forEachIndexed { i, (k, label) ->
                        SegmentedButton(selected = action == k, onClick = { action = k }, shape = SegmentedButtonDefaults.itemShape(i, actions.size)) { Text(label) }
                    }
                }
                Spacer(Modifier.height(12.dp))
                TimePicker(time)
                FlowRow(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    DAYS.forEach { day ->
                        FilterChip(selected = day in days, onClick = { days = if (day in days) days - day else DAYS.filter { it in days || it == day } },
                            label = { Text(day.replaceFirstChar(Char::uppercase)) })
                    }
                }
                if (action != "wake") {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text("Skip if someone is using it", Modifier.weight(1f))
                        Switch(checked = skip, onCheckedChange = { skip = it })
                    }
                } else {
                    Text("Sent by another always-on device with the helper on the same network.", style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
        },
        confirmButton = {
            Button(onClick = { onSave(action, "%02d:%02d".format(time.hour, time.minute), days, skip) }, enabled = days.isNotEmpty()) { Text("Save") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun KeyDialog(hub: Device, devices: List<Device>, onCreate: (String, Boolean, List<String>) -> Unit, onDismiss: () -> Unit) {
    var name by rememberSaveable { mutableStateOf("") }
    var power by rememberSaveable { mutableStateOf(false) }
    var picked by rememberSaveable { mutableStateOf(devices.filter { it.hasHelper || it.id == hub.id }.map { it.fleetName }) } // the names helpers know them by
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Connect an agent") },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState())) {
                OutlinedTextField(value = name, onValueChange = { name = it.take(40) }, label = { Text("Agent name (shown in History)") }, singleLine = true,
                    keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Words))
                Spacer(Modifier.height(8.dp))
                Text("Devices it may use", style = MaterialTheme.typography.labelLarge)
                devices.filter { it.hasHelper }.forEach { dev ->
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Checkbox(checked = dev.fleetName in picked, onCheckedChange = { picked = if (it) picked + dev.fleetName else picked - dev.fleetName })
                        Text(dev.name)
                    }
                }
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Column(Modifier.weight(1f)) {
                        Text("Can shut down and restart")
                        Text("Off: it can only check status and turn devices on.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    Switch(checked = power, onCheckedChange = { power = it })
                }
            }
        },
        confirmButton = { Button(onClick = { onCreate(name.trim(), power, picked) }, enabled = name.isNotBlank() && picked.isNotEmpty()) { Text("Create key") } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun NewKeyDialog(hub: Device, key: String, onDone: () -> Unit) {
    val ctx = androidx.compose.ui.platform.LocalContext.current
    val config = """{"mcpServers":{"powerdeck":{"type":"http","url":"http://${hub.host}:47323/mcp","headers":{"Authorization":"Bearer $key"}}}}"""
    AlertDialog(
        onDismissRequest = {},
        title = { Text("Agent key created") },
        text = {
            Column {
                Text("Add this to the agent's MCP settings. It's shown only once, and works only from the first device that uses it.")
                Spacer(Modifier.height(10.dp))
                Text(config, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall)
            }
        },
        confirmButton = {
            Button(onClick = { copyText(ctx, config, sensitive = true) }) {
                Icon(Icons.Rounded.ContentCopy, null, Modifier.size(18.dp))
                Spacer(Modifier.width(6.dp))
                Text("Copy")
            }
        },
        dismissButton = { TextButton(onClick = onDone) { Text("Done") } },
    )
}
