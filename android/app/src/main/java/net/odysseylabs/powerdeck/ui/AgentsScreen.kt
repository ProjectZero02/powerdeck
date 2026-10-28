package net.odysseylabs.powerdeck.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.rounded.OpenInNew
import androidx.compose.material.icons.rounded.Add
import androidx.compose.material.icons.rounded.ContentCopy
import androidx.compose.material.icons.rounded.Delete
import androidx.compose.material.icons.rounded.Key
import androidx.compose.material.icons.rounded.Shield
import androidx.compose.material.icons.rounded.SmartToy
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Checkbox
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.ListItemDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.KeyInfo

const val AGENTS_GUIDE = "https://github.com/ProjectZero02/powerdeck/blob/main/docs/integrating-an-assistant.md"

data class AgentsActions(
    val back: () -> Unit = {},
    val loadKeys: () -> Unit = {},
    val addKey: (name: String, power: Boolean, devices: List<String>) -> Unit = { _, _, _ -> },
    val removeKey: (KeyInfo) -> Unit = {},
    val clearNewKey: () -> Unit = {},
    val guide: () -> Unit = {},
)

/** How to connect an AI agent through [hub], and the agent keys it holds. */
@Composable
fun AgentsScreen(hub: Device, state: UiState, detail: Detail, a: AgentsActions, snackbar: SnackbarHostState) {
    var adding by remember { mutableStateOf(false) }
    LaunchedEffect(hub.id) { a.loadKeys() }
    Scaffold(
        snackbarHost = { SnackbarHost(snackbar) },
        containerColor = MaterialTheme.colorScheme.background,
        topBar = { AppBar("AI agents", onBack = a.back) },
    ) { pad ->
        Column(
            Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = Margin),
            verticalArrangement = Arrangement.spacedBy(Gap),
        ) {
            Column(Modifier.fillMaxWidth().padding(vertical = 12.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                Surface(shape = CircleShape, color = MaterialTheme.colorScheme.primaryContainer, modifier = Modifier.size(64.dp)) {
                    Icon(Icons.Rounded.SmartToy, null, tint = MaterialTheme.colorScheme.onPrimaryContainer, modifier = Modifier.padding(16.dp))
                }
                Spacer(Modifier.height(12.dp))
                Text("Let an AI agent use your devices", style = MaterialTheme.typography.titleLarge, textAlign = TextAlign.Center)
                Spacer(Modifier.height(4.dp))
                Text(
                    "Assistants like Claude connect to PowerDeck over MCP. They can see whether your devices are on, turn them on, and, if you allow it, shut them down.",
                    style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center,
                )
            }

            Card(shape = RoundedCornerShape(24.dp), colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer)) {
                Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(14.dp)) {
                    Numbered(1, "Create a key for the agent below.")
                    Numbered(2, "Paste the settings it shows into the agent's MCP settings. The agent has to run on a device on your tailnet.")
                    Numbered(3, "Ask it something like “is my PC on?”")
                }
            }

            SectionTitle("Keys on ${hub.name}")
            Card(shape = RoundedCornerShape(20.dp), colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer)) {
                Column(Modifier.padding(vertical = 4.dp)) {
                    val keys = detail.keys
                    when {
                        keys == null && detail.error != null -> Item("Couldn't load the keys", detail.error)
                        keys == null -> Item("Loading…")
                        keys.isEmpty() -> Item("No agents yet")
                        else -> keys.forEach { k ->
                            Item(k.name, buildList {
                                add(if ("power" in k.scopes) "Can shut down and restart" else "Can turn on")
                                if (k.devices.isNotEmpty()) add(k.devices.joinToString())
                                if (k.lastUsed == null) add("not used yet")
                            }.joinToString(" · "), icon = Icons.Rounded.Key) {
                                IconButton(onClick = { a.removeKey(k) }) { Icon(Icons.Rounded.Delete, contentDescription = "Remove ${k.name}") }
                            }
                        }
                    }
                    Button(onClick = { adding = true }, modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                        Icon(Icons.Rounded.Add, null, Modifier.size(18.dp))
                        Spacer(Modifier.width(6.dp))
                        Text("Create a key")
                    }
                }
            }

            SectionTitle("Built-in limits")
            Card(shape = RoundedCornerShape(20.dp), colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer)) {
                Column(Modifier.padding(vertical = 4.dp)) {
                    Item("A key works from one device", "The first Tailscale device that uses it. A copied key is useless elsewhere.", Icons.Rounded.Shield)
                    Item("Shutdowns count down 60 s", "And are refused while someone is using the device.", Icons.Rounded.Shield)
                    Item("Off with one switch", "Turn off “Allow AI agents” on a device and agents can only see whether it's on.", Icons.Rounded.Shield)
                }
            }
            TextButton(onClick = a.guide) {
                Text("Full guide on GitHub")
                Spacer(Modifier.width(6.dp))
                Icon(Icons.AutoMirrored.Rounded.OpenInNew, null, Modifier.size(16.dp))
            }
            Spacer(Modifier.height(24.dp))
        }
    }
    if (adding) KeyDialog(hub, state.devices, onCreate = { n, p, devs -> a.addKey(n, p, devs); adding = false }, onDismiss = { adding = false })
    detail.newKey?.let { NewKeyDialog(hub, it, a.clearNewKey) }
}

@Composable
private fun Numbered(n: Int, text: String) {
    Row {
        Surface(shape = CircleShape, color = MaterialTheme.colorScheme.primary, modifier = Modifier.size(26.dp)) {
            Text("$n", color = MaterialTheme.colorScheme.onPrimary, textAlign = TextAlign.Center, fontWeight = FontWeight.Bold, modifier = Modifier.padding(top = 3.dp))
        }
        Spacer(Modifier.width(14.dp))
        Text(text, style = MaterialTheme.typography.bodyLarge, modifier = Modifier.weight(1f).padding(top = 2.dp))
    }
}

@Composable
private fun Item(title: String, sub: String? = null, icon: androidx.compose.ui.graphics.vector.ImageVector? = null, trailing: (@Composable () -> Unit)? = null) {
    ListItem(
        headlineContent = { Text(title) },
        supportingContent = sub?.let { { Text(it) } },
        leadingContent = icon?.let { { Icon(it, null, tint = MaterialTheme.colorScheme.primary) } },
        trailingContent = trailing,
        colors = ListItemDefaults.colors(containerColor = Color.Transparent),
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
