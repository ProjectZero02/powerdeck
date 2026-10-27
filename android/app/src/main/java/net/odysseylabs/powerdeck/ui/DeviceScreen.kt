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
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.rounded.ArrowBack
import androidx.compose.material.icons.rounded.CheckCircle
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
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Switch
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material.icons.rounded.ExpandLess
import androidx.compose.material.icons.rounded.ExpandMore
import androidx.compose.foundation.selection.toggleable
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.platform.LocalContext
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
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import net.odysseylabs.powerdeck.data.Check
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.DeviceState
import net.odysseylabs.powerdeck.data.Event
import net.odysseylabs.powerdeck.data.HelperStatus
import net.odysseylabs.powerdeck.data.KeyInfo
import net.odysseylabs.powerdeck.data.Peer
import net.odysseylabs.powerdeck.data.KvmWiring
import net.odysseylabs.powerdeck.data.OwnerInfo
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
    val reset: () -> Unit = {},
    val recheckKvm: () -> Unit = {},
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
    val uninstall: () -> Unit = {},
    val setAgents: (Boolean) -> Unit = {},
    val agents: () -> Unit = {},
    val hint: (String) -> Unit = {},
    val updateHelper: () -> Unit = {},
    val pauseUpdates: (Boolean) -> Unit = {},
    val removeOwner: (OwnerInfo) -> Unit = {},
)

private val DAYS = listOf("mon", "tue", "wed", "thu", "fri", "sat", "sun")
private val timeFmt = DateTimeFormatter.ofLocalizedDateTime(FormatStyle.SHORT).withZone(ZoneId.systemDefault())

@Composable
private fun Section(title: String, folded: Boolean? = null, onFold: () -> Unit = {}, content: @Composable () -> Unit) {
    Column(Modifier.fillMaxWidth()) {
        if (folded == null) SectionTitle(title)
        else Row(verticalAlignment = Alignment.CenterVertically) {
            SectionTitle(title, Modifier.weight(1f))
            IconButton(onClick = onFold) {
                Icon(if (folded) Icons.Rounded.ExpandMore else Icons.Rounded.ExpandLess, contentDescription = if (folded) "Show $title" else "Hide $title")
            }
        }
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
    val tone = describe(st, now).second
    val busy = d.id in state.busy
    val hold = state.settings.confirmShutdown
    val ctx = LocalContext.current
    var menu by remember { mutableStateOf(false) }
    var confirmRemove by remember { mutableStateOf(false) }
    var confirmForce by remember { mutableStateOf(false) }
    var confirmReset by remember { mutableStateOf(false) }
    val hub = d.kvm?.hub?.let { id -> state.devices.firstOrNull { it.id == id } }
    var renaming by remember { mutableStateOf(false) }
    var scheduling by remember { mutableStateOf(false) }
    var linkKvm by remember { mutableStateOf<Peer?>(null) }

    Scaffold(
        snackbarHost = { SnackbarHost(snackbar) },
        containerColor = MaterialTheme.colorScheme.background,
        topBar = {
            AppBar(d.name, onBack = a.back) {
                    IconButton(onClick = { menu = true }) { Icon(Icons.Rounded.MoreVert, contentDescription = "More") }
                    DropdownMenu(menu, onDismissRequest = { menu = false }) {
                        DropdownMenuItem(text = { Text("Rename") }, onClick = { menu = false; renaming = true })
                        DropdownMenuItem(text = { Text("Add to home screen") }, onClick = { menu = false; a.pinShortcut() })
                        if (hub != null) DropdownMenuItem(text = { Text("Reset (press reset)") }, onClick = { menu = false; confirmReset = true })
                        if (d.kvm != null) DropdownMenuItem(text = { Text("Force off (hold power 12 s)") }, onClick = { menu = false; confirmForce = true })
                        DropdownMenuItem(text = { Text("Delete", color = MaterialTheme.colorScheme.error) }, onClick = { menu = false; confirmRemove = true })
                    }
            }
        },
    ) { pad ->
        Column(Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = Margin), verticalArrangement = Arrangement.spacedBy(Gap)) {
            // ---- State and power
            Card(shape = RoundedCornerShape(24.dp), colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer), modifier = Modifier.fillMaxWidth()) {
                Column(Modifier.padding(16.dp)) {
                    // Same anatomy as the home card: what it is and its dot, then only the news.
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        IconTile(d.icon())
                        Spacer(Modifier.width(16.dp))
                        Column(Modifier.weight(1f)) {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Text(d.kindName(), style = MaterialTheme.typography.titleMedium)
                                Spacer(Modifier.width(8.dp))
                                StatusDot(tone)
                            }
                            val news = note(st, now) ?: status?.let { ("Up " + uptime(it.uptimeSeconds)) to Tone.On }
                            news?.let { (text, t) ->
                                Text(text, style = MaterialTheme.typography.bodyMedium,
                                    color = if (t == Tone.Warn) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurfaceVariant)
                            }
                        }
                    }
                    status?.guestsRunning?.takeIf { it > 0 }?.let { n ->
                        Spacer(Modifier.height(12.dp))
                        Text("$n VM${if (n == 1) "" else "s"} running: ${status.guestNames.joinToString(", ")}",
                            style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    if (state.tailnet) Row(Modifier.padding(top = 16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        when {
                            status?.pending != null -> OutlinedButton(onClick = a.cancel, enabled = !busy, modifier = Modifier.weight(1f).height(48.dp), shape = RoundedCornerShape(16.dp)) {
                                Text("Cancel")
                            }
                            st is DeviceState.On -> {
                                if (d.hasHelper || d.kvm != null) HoldButton("Shut down", Icons.Rounded.PowerSettingsNew, true, !busy, Modifier.weight(1f), hold = hold,
                                    onHint = { a.hint("Hold to shut down ${d.name}") }) { a.shutdown(false) }
                                // The light is on but the helper is silent: it may be frozen, which only a reset fixes.
                                if (d.hasHelper && status == null && hub != null) FilledTonalButton(onClick = { confirmReset = true }, enabled = !busy,
                                    modifier = Modifier.weight(1f).height(48.dp), shape = RoundedCornerShape(16.dp)) {
                                    Icon(Icons.Rounded.RestartAlt, null, Modifier.size(18.dp))
                                    Spacer(Modifier.width(6.dp))
                                    Text("Reset")
                                }
                                else if (d.hasHelper) HoldButton("Restart", Icons.Rounded.RestartAlt, false, !busy, Modifier.weight(1f), hold = hold, tonal = true,
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

            // ---- PowerDeck helper update
            status?.update?.takeIf { it.available || it.installing }?.let { u ->
                Section("Update") {
                    Row2(if (u.installing) "Updating to ${u.latest}…" else "Version ${u.latest} is ready",
                        when {
                            u.installing -> null
                            u.error != null -> "Last try: ${u.error}"
                            u.pausedUntil != null -> "Automatic updates paused until ${timeFmt.format(u.pausedUntil)}"
                            u.auto && u.autoAt != null -> "Installs on its own ${timeFmt.format(u.autoAt)}"
                            else -> null
                        },
                        icon = { Icon(Icons.Rounded.SystemUpdateAlt, null, tint = MaterialTheme.colorScheme.primary) },
                        trailing = if (u.installing) null else ({ TextButton(onClick = a.updateHelper, enabled = !busy && status.pending == null) { Text("Update") } }))
                    if (u.auto && !u.installing && status.peerKeys != null) { // pausing needs a 2.1 helper
                        TextButton(onClick = { a.pauseUpdates(u.pausedUntil == null) }, enabled = !busy, modifier = Modifier.padding(start = 8.dp)) {
                            Text(if (u.pausedUntil == null) "Pause automatic updates for 7 days" else "Resume automatic updates")
                        }
                    }
                }
            }

            // ---- Setup checks: problems first; folds away (remembered per device), closed when all is well
            val checks = status?.checks.orEmpty()
            if (checks.isNotEmpty()) {
                val prefs = remember { ctx.getSharedPreferences("ui", 0) }
                val problems = checks.count { it.problem }
                var open by remember(d.id, problems > 0) { mutableStateOf(prefs.getBoolean("setupOpen:${d.id}", problems > 0)) }
                val toggle = { open = !open; prefs.edit().putBoolean("setupOpen:${d.id}", open).apply() }
                Section("Setup", folded = !open, onFold = toggle) {
                    if (!open) {
                        Row2(if (problems == 0) "All set" else "$problems need${if (problems == 1) "s" else ""} attention",
                            "${checks.size} checks",
                            icon = {
                                if (problems == 0) Icon(Icons.Rounded.CheckCircle, null, tint = OnGreen)
                                else Icon(Icons.Rounded.WarningAmber, null, tint = MaterialTheme.colorScheme.error)
                            },
                            trailing = { TextButton(onClick = toggle) { Text("Show") } })
                        return@Section
                    }
                    var showPassed by rememberSaveable { mutableStateOf(false) }
                    val passed = checks.filter { it.ok }
                    (checks - passed.toSet()).sortedBy { if (it.problem) 0 else 1 }.plus(if (showPassed) passed else emptyList()).forEach { c ->
                        Row2(c.title, c.detail, icon = {
                            when {
                                c.ok -> Icon(Icons.Rounded.CheckCircle, null, tint = OnGreen)
                                c.level == "error" -> Icon(Icons.Rounded.ErrorOutline, null, tint = MaterialTheme.colorScheme.error)
                                c.level == "warn" -> Icon(Icons.Rounded.WarningAmber, null, tint = MaterialTheme.colorScheme.error)
                                else -> Icon(Icons.Rounded.Info, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
                            }
                        }, trailing = if (c.fixable && c.problem) ({ TextButton(onClick = { a.fix(c) }, enabled = !busy) { Text("Fix") } }) else null)
                    }
                    if (passed.isNotEmpty() && !showPassed) {
                        Row2(if (passed.size == checks.size) "All set" else "${passed.size} more passed",
                            icon = { Icon(Icons.Rounded.CheckCircle, null, tint = OnGreen) },
                            trailing = { TextButton(onClick = { showPassed = true }) { Text("Show") } })
                    }
                }
            }

            // ---- Schedules
            if (d.hasHelper) Section("Schedules") {
                val list = detail.schedules
                if (list == null) Row2("Loading…")
                list?.forEach { (host, s) ->
                    Row2(
                        "${when (s.action) { "shutdown" -> "Shut down"; "restart" -> "Restart"; else -> "Turn on" }} at ${s.time}",
                        buildList {
                            add(if (s.days.size == 7) "Every day" else s.days.joinToString(" ") { it.replaceFirstChar(Char::uppercase) })
                            if (s.action != "wake" && s.skipIfInUse) add("not while in use")
                            if (host.id != d.id) add("kept by ${host.name}")
                            else if (clockNote(d.name, status?.utcOffsetMinutes) != null) add("${d.name}'s time")
                        }.joinToString(" · "),
                        trailing = { IconButton(onClick = { a.removeSchedule(host, s) }) { Icon(Icons.Rounded.Delete, contentDescription = "Remove") } },
                    )
                }
                TextButton(onClick = { scheduling = true }, modifier = Modifier.padding(start = 8.dp)) { Text("Add schedule") }
            }

            // ---- Power button (KVM)
            // Only when there's a KVM to show or to link: most people have none.
            val kvms = state.found.orEmpty().filter { it.isKvm }
            if (d.kvm != null || kvms.isNotEmpty()) Section("Power button") {
                val k = d.kvm
                if (k != null) {
                    Row2(k.name, if (hub != null) "Driven by ${hub.name} · signed in as ${k.user}" else "Signed in as ${k.user}",
                        trailing = { TextButton(onClick = a.unlinkKvm) { Text("Unlink") } })
                    k.hubError?.let { e ->
                        Row2("Only this phone uses it", e, icon = { Icon(Icons.Rounded.WarningAmber, null, tint = MaterialTheme.colorScheme.error) })
                    }
                    k.wiring?.takeIf { hub != null }?.let { w ->
                        WiringRow("Power light", w.led, "Wired", "Reads off while ${d.name} runs: not connected, or the wrong way round",
                            "Checked once ${d.name} is on")
                        WiringRow("Power button", w.power, "Works", "A press did nothing: check the cable", "Checked on the first press")
                        WiringRow("Reset button", w.reset, "Works", "Not connected: a reset forces it off and on instead", "Checked on the first reset")
                        TextButton(onClick = a.recheckKvm, enabled = !busy, modifier = Modifier.padding(start = 8.dp)) { Text("Check again") }
                    }
                } else {
                    Row2("Link a NanoKVM", "Presses the power button when Wake-on-LAN can't")
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

            // ---- Agents: one switch once an agent can reach this device, a Set up button until then
            if (d.hasHelper) Section("AI agents") {
                val keys = agentKeysFor(d, state.states)
                val agents = status?.agents
                if (keys.isEmpty()) {
                    Row2("Allow AI agents to use this device", "Let an assistant like Claude check, wake and shut down your devices",
                        trailing = { FilledTonalButton(onClick = a.agents) { Text("Set up") } })
                } else {
                    val on = agents?.allowed ?: true
                    ListItem(
                        headlineContent = { Text("Allow AI agents to use this device") },
                        supportingContent = {
                            Text(when {
                                agents == null && status != null -> "Only phones on ${d.name}'s Tailscale account can change this"
                                agents == null -> "Turn ${d.name} on to change this"
                                on -> "${keys.joinToString()} can use it. Shutdowns count down 60 s"
                                else -> "Agents can see whether it's on, but can't turn it on or off"
                            })
                        },
                        trailingContent = { Switch(checked = on, onCheckedChange = a.setAgents, enabled = agents != null && !busy) },
                        colors = ListItemDefaults.colors(containerColor = Color.Transparent),
                        modifier = Modifier.toggleable(on, enabled = agents != null && !busy, role = Role.Switch, onValueChange = a.setAgents),
                    )
                    TextButton(onClick = a.agents, modifier = Modifier.padding(start = 8.dp)) { Text("Manage agents") }
                }
            }

            // ---- Owner devices: what controls this helper fully (only its owners see this)
            detail.owners?.takeIf { it.isNotEmpty() }?.let { owners ->
                Section("Owner devices") {
                    owners.forEach { o ->
                        Row2(if (o.you) "${o.name} (this phone)" else o.name, "Full control, settings included",
                            trailing = if (o.you) null else ({
                                IconButton(onClick = { a.removeOwner(o) }, enabled = !busy) { Icon(Icons.Rounded.Delete, contentDescription = "Remove ${o.name}") }
                            }))
                    }
                    Text("Add another phone or computer by scanning ${d.name}'s code.", style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp))
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

    if (renaming) RenameDialog(d.name, onSave = a.rename, onDismiss = { renaming = false })
    if (confirmRemove) DeleteDialog(d, st, onRemove = a.remove, onUninstall = a.uninstall, onDismiss = { confirmRemove = false })
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
    if (confirmReset) {
        AlertDialog(
            onDismissRequest = { confirmReset = false },
            title = { Text("Reset ${d.name}?") },
            text = { Text("${hub?.name ?: "The hub"} presses ${d.name}'s reset button, then checks that it comes back. Unsaved work is lost. Use only if it's frozen.") },
            confirmButton = {
                Button(onClick = { confirmReset = false; a.reset() }, colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error)) { Text("Reset") }
            },
            dismissButton = { TextButton(onClick = { confirmReset = false }) { Text("Cancel") } },
        )
    }
    if (scheduling) ScheduleDialog(d, status, onSave = { act, t, days, skip -> scheduling = false; a.addSchedule(act, t, days, skip) }, onDismiss = { scheduling = false })
    linkKvm?.let { p -> KvmDialog(p, state.devices, onLink = a.linkKvm, onDismiss = { linkKvm = null }, fixedDevice = d) }
}

/** One cable of the KVM, as the hub learned it. */
@Composable
private fun WiringRow(title: String, state: String, wired: String, notWired: String, unknown: String) {
    Row2(title, when (state) {
        KvmWiring.WIRED -> wired
        KvmWiring.NOT_WIRED -> notWired
        else -> unknown
    }, icon = {
        when (state) {
            KvmWiring.WIRED -> Icon(Icons.Rounded.CheckCircle, null, tint = OnGreen)
            KvmWiring.NOT_WIRED -> Icon(Icons.Rounded.WarningAmber, null, tint = MaterialTheme.colorScheme.error)
            else -> Icon(Icons.Rounded.Info, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    })
}

@Composable
private fun EventRow(e: Event, d: Device) {
    val who = if (d.me != null && e.who == d.me) "You" else e.who
    Row2("$who: ${e.action}", "${timeFmt.format(e.at)} · ${e.result}")
}

@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable
private fun ScheduleDialog(d: Device, status: HelperStatus?, onSave: (String, String, List<String>, Boolean) -> Unit, onDismiss: () -> Unit) {
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
                clockNote(d.name, status?.utcOffsetMinutes)?.takeIf { action != "wake" }?.let { // wakes run on the sender's clock
                    Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.tertiary)
                    Spacer(Modifier.height(8.dp))
                }
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
                    if (status?.guestsRunning != null) {
                        Text("Running VMs don't count as in use; they're shut down cleanly first.", style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant)
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

/** Schedules run on the device's clock; say so when it differs from the phone's. */
internal fun clockNote(name: String, offsetMinutes: Int?, phoneOffsetMinutes: Int = java.util.TimeZone.getDefault().getOffset(System.currentTimeMillis()) / 60000): String? {
    if (offsetMinutes == null || offsetMinutes == phoneOffsetMinutes) return null
    fun span(m: Int) = when {
        m < 60 -> "$m min"
        m % 60 == 0 -> "${m / 60} h"
        else -> "${m / 60} h ${m % 60} min"
    }
    val o = kotlin.math.abs(offsetMinutes)
    val zone = "UTC" + (if (offsetMinutes < 0) "−" else "+") + (o / 60) + (if (o % 60 != 0) ":%02d".format(o % 60) else "")
    val diff = phoneOffsetMinutes - offsetMinutes
    return "Times are on $name's clock ($zone), ${span(kotlin.math.abs(diff))} ${if (diff > 0) "behind" else "ahead of"} your phone."
}
