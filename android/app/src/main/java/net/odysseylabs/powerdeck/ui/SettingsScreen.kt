package net.odysseylabs.powerdeck.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.rounded.ArrowBack
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.ui.graphics.Color
import androidx.compose.material3.ListItemDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import net.odysseylabs.powerdeck.BuildConfig
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.HelperUpdate
import net.odysseylabs.powerdeck.data.Settings

data class SettingsActions(
    val back: () -> Unit = {},
    val setConfirm: (Boolean) -> Unit = {},
    val setAutoTailscale: (Boolean) -> Unit = {},
    val setNotifications: (Boolean) -> Unit = {},
    val addWidget: () -> Unit = {},
    val addTiles: () -> Unit = {},
    val checkUpdate: () -> Unit = {},
    val updateDevices: () -> Unit = {},
    val setDevicesAutoUpdate: (Boolean) -> Unit = {},
    val openProject: () -> Unit = {},
)

@Composable
private fun Header(title: String) {
    Text(title, style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.primary, modifier = Modifier.padding(start = 16.dp, top = 24.dp, bottom = 4.dp))
}

@Composable
private fun SettingRow(title: String, subtitle: String? = null, onClick: (() -> Unit)? = null, trailing: (@Composable () -> Unit)? = null) {
    ListItem(
        headlineContent = { Text(title) },
        supportingContent = subtitle?.let { { Text(it) } },
        trailingContent = trailing,
        colors = ListItemDefaults.colors(containerColor = Color.Transparent),
        modifier = if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier,
    )
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SettingsScreen(settings: Settings, a: SettingsActions, helpers: List<Pair<Device, HelperUpdate>> = emptyList()) {
    Scaffold(
        containerColor = MaterialTheme.colorScheme.background,
        topBar = {
            AppBar("Settings", onBack = a.back)
        },
    ) { pad ->
        Column(Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState())) {
            Header("Behaviour")
            SettingRow("Hold to shut down", "Press and hold Shut down and Restart", trailing = {
                Switch(checked = settings.confirmShutdown, onCheckedChange = a.setConfirm)
            })
            SettingRow("Turn on Tailscale automatically", "When you press a button", trailing = {
                Switch(checked = settings.autoTailscale, onCheckedChange = a.setAutoTailscale)
            })
            SettingRow("Notifications", "When a schedule or agent acts", trailing = {
                Switch(checked = settings.notifications, onCheckedChange = a.setNotifications)
            })

            Header("Updates")
            val outdated = helpers.filter { it.second.available }
            SettingRow("Update devices now",
                when {
                    helpers.isEmpty() -> "Every computer that's on"
                    outdated.isEmpty() -> "All ${helpers.size} ${if (helpers.size == 1) "device is" else "devices are"} up to date"
                    else -> "${outdated.joinToString(", ") { it.first.name }}: ${outdated.first().second.latest} is ready"
                },
                onClick = a.updateDevices)
            SettingRow("Update devices automatically", "Signed updates, checked every 6 hours", trailing = {
                Switch(checked = helpers.isEmpty() || helpers.all { it.second.auto }, onCheckedChange = a.setDevicesAutoUpdate, enabled = helpers.isNotEmpty())
            })
            SettingRow("PowerDeck ${BuildConfig.VERSION_NAME} on this phone", "Check for an update", onClick = a.checkUpdate)

            Header("Shortcuts")
            SettingRow("Add home-screen widget", "Every device, one tap each", onClick = a.addWidget)
            SettingRow("Add Quick Settings tiles", "Choose a tile on a device's page", onClick = a.addTiles)

            Header("About")
            SettingRow("Help, source code and releases", "github.com/ProjectZero02/powerdeck", onClick = a.openProject)
        }
    }
}
