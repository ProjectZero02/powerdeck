package net.odysseylabs.powerdeck.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.unit.dp
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.DeviceState
import net.odysseylabs.powerdeck.data.HelperStatus

@Composable
fun RenameDialog(current: String, onSave: (String) -> Unit, onDismiss: () -> Unit) {
    var name by rememberSaveable { mutableStateOf(current) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Rename") },
        text = { OutlinedTextField(value = name, onValueChange = { name = it.take(40) }, singleLine = true) },
        confirmButton = { Button(onClick = { onDismiss(); onSave(name) }, enabled = name.isNotBlank()) { Text("Save") } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

/** Why PowerDeck can't uninstall itself from [d] right now, or null when it can. */
fun uninstallBlocker(d: Device, st: DeviceState?): String? {
    if (!d.hasHelper) return null
    val status = (st as? DeviceState.On)?.status ?: return "${d.name} has to be on to uninstall PowerDeck from it."
    return if ("uninstall" in status.caps) null else "Update PowerDeck on ${d.name} to uninstall it from here."
}

/**
 * Delete: remove from this phone only, or also uninstall PowerDeck from the device. Other phones
 * can't reach it after an uninstall, so it's never the default.
 */
@Composable
fun DeleteDialog(d: Device, st: DeviceState?, onRemove: () -> Unit, onUninstall: () -> Unit, onDismiss: () -> Unit) {
    var uninstall by rememberSaveable { mutableStateOf(false) }
    val blocker = uninstallBlocker(d, st)
    // The device may go off while the dialog is open.
    val doUninstall = uninstall && blocker == null
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Delete ${d.name}?") },
        text = {
            Column(Modifier.selectableGroup()) {
                Choice(
                    selected = !doUninstall, enabled = true, onClick = { uninstall = false },
                    title = "Remove from this phone",
                    sub = if (d.hasHelper) "PowerDeck stays on ${d.name}. You can add it again by scanning its code." else "Its KVM login is deleted from this phone.",
                )
                if (d.hasHelper) Choice(
                    selected = doUninstall, enabled = blocker == null, onClick = { uninstall = true },
                    title = "Also uninstall PowerDeck from ${d.name}",
                    sub = blocker ?: "Deletes PowerDeck and its settings, schedules and agent keys from ${d.name}. Other phones can't control it any more.",
                )
            }
        },
        confirmButton = {
            Button(
                onClick = { onDismiss(); if (doUninstall) onUninstall() else onRemove() },
                colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error, contentColor = MaterialTheme.colorScheme.onError),
            ) { Text(if (doUninstall) "Uninstall" else "Remove") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun Choice(selected: Boolean, enabled: Boolean, onClick: () -> Unit, title: String, sub: String) {
    Row(
        Modifier.fillMaxWidth().selectable(selected, enabled = enabled, role = Role.RadioButton, onClick = onClick).padding(vertical = 6.dp),
        verticalAlignment = Alignment.Top,
    ) {
        RadioButton(selected = selected, onClick = null, enabled = enabled, modifier = Modifier.padding(top = 2.dp, end = 12.dp))
        Column {
            Text(title, style = MaterialTheme.typography.bodyLarge,
                color = if (enabled) MaterialTheme.colorScheme.onSurface else MaterialTheme.colorScheme.onSurface.copy(alpha = 0.38f))
            Spacer(Modifier.height(2.dp))
            Text(sub, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

/** The agent keys (on any helper we can see) that may use [d]. Empty: no agent is set up for it. */
fun agentKeysFor(d: Device, states: Map<String, DeviceState>): List<String> =
    states.values.mapNotNull { (it as? DeviceState.On)?.status?.agents }.flatMap { it.keys }
        .filter { it.devices.isEmpty() || d.fleetName in it.devices }.map { it.name }.distinct()

fun HelperStatus.agentsAllowed() = agents?.allowed != false
