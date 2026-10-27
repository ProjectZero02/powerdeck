package net.odysseylabs.powerdeck.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.rounded.ArrowBack
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import net.odysseylabs.powerdeck.BuildConfig
import net.odysseylabs.powerdeck.data.Config

data class SettingsActions(
    val back: () -> Unit = {},
    val unpair: () -> Unit = {},
    val changeKvm: () -> Unit = {},
    val forgetKvm: () -> Unit = {},
    val setConfirm: (Boolean) -> Unit = {},
    val setAutoTailscale: (Boolean) -> Unit = {},
    val addWidget: () -> Unit = {},
    val addTiles: () -> Unit = {},
    val checkUpdate: () -> Unit = {},
    val openProject: () -> Unit = {},
)

@Composable
private fun Section(title: String) {
    Text(title, style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.primary, modifier = Modifier.padding(start = 16.dp, top = 24.dp, bottom = 4.dp))
}

@Composable
private fun SettingRow(title: String, subtitle: String? = null, onClick: (() -> Unit)? = null, trailing: (@Composable () -> Unit)? = null) {
    ListItem(
        headlineContent = { Text(title) },
        supportingContent = subtitle?.let { { Text(it) } },
        trailingContent = trailing,
        modifier = if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier,
    )
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SettingsScreen(config: Config, a: SettingsActions) {
    var confirmUnpair by remember { mutableStateOf(false) }
    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Settings") },
                navigationIcon = { IconButton(onClick = a.back) { Icon(Icons.AutoMirrored.Rounded.ArrowBack, contentDescription = "Back") } },
            )
        },
    ) { pad ->
        Column(Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState())) {
            Section("PC")
            SettingRow(config.pc?.name ?: "Not paired", config.pc?.let { "${it.host} · ${it.mac ?: "network card unknown"}" })
            SettingRow("Unpair this phone", "Remove this phone from the PC", onClick = { confirmUnpair = true })
            HorizontalDivider()

            Section("Server (NanoKVM)")
            SettingRow(config.nanoKvm ?: "Not set up", if (config.hasKvmPassword) "Connected as ${config.nanoKvmUser}" else "Password not set", onClick = a.changeKvm)
            if (config.hasKvmPassword) SettingRow("Forget NanoKVM password", onClick = a.forgetKvm)
            config.proxmox?.let { SettingRow("Proxmox", it) }
            HorizontalDivider()

            Section("Behaviour")
            SettingRow("Ask before shutting down", trailing = { Switch(checked = config.confirmShutdown, onCheckedChange = a.setConfirm) })
            SettingRow("Turn on Tailscale automatically", "Connects Tailscale when you press a button", trailing = {
                Switch(checked = config.autoTailscale, onCheckedChange = a.setAutoTailscale)
            })
            HorizontalDivider()

            Section("Shortcuts")
            SettingRow("Add home screen widget", "Status and power buttons for both devices", onClick = a.addWidget)
            SettingRow("Add Quick Settings tiles", "PC and Server tiles in the notification shade", onClick = a.addTiles)
            HorizontalDivider()

            Section("About")
            SettingRow("PowerDeck ${BuildConfig.VERSION_NAME}", "Check for updates", onClick = a.checkUpdate)
            SettingRow("Source code and releases", "github.com/ProjectZero02/powerdeck", onClick = a.openProject)
        }
    }
    if (confirmUnpair) {
        AlertDialog(
            onDismissRequest = { confirmUnpair = false },
            title = { Text("Unpair this phone?") },
            text = { Text("You'll need to scan a new pairing code on the PC to use PowerDeck again.") },
            confirmButton = { TextButton(onClick = { confirmUnpair = false; a.unpair() }) { Text("Unpair") } },
            dismissButton = { TextButton(onClick = { confirmUnpair = false }) { Text("Cancel") } },
        )
    }
}
