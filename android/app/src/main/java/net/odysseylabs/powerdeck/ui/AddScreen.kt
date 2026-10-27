package net.odysseylabs.powerdeck.ui

import android.content.Intent
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.rounded.ArrowBack
import androidx.compose.material.icons.rounded.ContentCopy
import androidx.compose.material.icons.rounded.QrCodeScanner
import androidx.compose.material.icons.rounded.Refresh
import androidx.compose.material.icons.rounded.Share
import androidx.compose.material.icons.rounded.SettingsInputComponent
import androidx.compose.material.icons.rounded.Visibility
import androidx.compose.material.icons.rounded.VisibilityOff
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.autofill.ContentType
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.contentType
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import net.odysseylabs.powerdeck.R
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.Peer

const val INSTALL_WINDOWS = "irm https://github.com/ProjectZero02/powerdeck/releases/latest/download/install.ps1 | iex"
const val INSTALL_LINUX = "curl -fsSL https://github.com/ProjectZero02/powerdeck/releases/latest/download/install.sh | sh"

data class AddActions(
    val back: (() -> Unit)? = null,
    val scan: () -> Unit = {},
    val byName: (String) -> Unit = {},
    val addPeer: (Peer) -> Unit = {},
    val submitCode: (String) -> Unit = {},
    val dismiss: () -> Unit = {},
    val confirmLink: () -> Unit = {},
    val find: () -> Unit = {},
    val linkKvm: (deviceId: String?, newName: String, peer: Peer, user: String, password: String, done: (Boolean) -> Unit) -> Unit = { _, _, _, _, _, _ -> },
)

@Composable
private fun Step(n: Int, title: String, body: @Composable () -> Unit = {}) {
    Row {
        Surface(shape = CircleShape, color = MaterialTheme.colorScheme.primary, modifier = Modifier.size(28.dp)) {
            Text("$n", color = MaterialTheme.colorScheme.onPrimary, textAlign = TextAlign.Center, modifier = Modifier.padding(top = 4.dp), fontWeight = FontWeight.Bold)
        }
        Spacer(Modifier.width(14.dp))
        Column(Modifier.weight(1f).padding(top = 3.dp)) {
            Text(title, style = MaterialTheme.typography.bodyLarge, fontWeight = FontWeight.Medium)
            body()
        }
    }
}

/** A copyable command, with a share button so it can be sent to the computer. */
@Composable
fun Command(label: String, cmd: String) {
    val ctx = LocalContext.current
    Column(Modifier.padding(top = 8.dp)) {
        Text(label, style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
        Surface(shape = RoundedCornerShape(12.dp), color = MaterialTheme.colorScheme.surface, modifier = Modifier.fillMaxWidth().padding(top = 4.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(cmd, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall, modifier = Modifier.weight(1f).padding(start = 12.dp, top = 10.dp, bottom = 10.dp))
                IconButton(onClick = { copyText(ctx, cmd) }) { Icon(Icons.Rounded.ContentCopy, contentDescription = "Copy") }
                IconButton(onClick = {
                    ctx.startActivity(Intent.createChooser(Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, cmd), "Send the install command"))
                }) { Icon(Icons.Rounded.Share, contentDescription = "Share") }
            }
        }
    }
}

@Composable
fun InstallSteps() {
    Card(shape = RoundedCornerShape(24.dp), colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer), modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(18.dp)) {
            Step(1, "Put Tailscale on this phone and on each computer, signed in to the same account.")
            Step(2, "Install the PowerDeck helper on each computer:") {
                Command("Windows (PowerShell)", INSTALL_WINDOWS)
                Command("Linux / Proxmox (terminal)", INSTALL_LINUX)
            }
            Step(3, "Scan the code it shows. Everything else on your tailnet shows up here to add with one tap.")
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AddScreen(state: UiState, a: AddActions, snackbar: SnackbarHostState) {
    var name by rememberSaveable { mutableStateOf("") }
    var kvmFor by remember { mutableStateOf<Peer?>(null) }
    val step = state.add
    val working = step is AddStep.Working
    val first = state.devices.isEmpty()
    Scaffold(
        snackbarHost = { SnackbarHost(snackbar) },
        containerColor = MaterialTheme.colorScheme.background,
        topBar = {
            if (!first) AppBar("Add devices", onBack = a.back)
        },
    ) { pad ->
        Column(
            Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = Margin).imePadding(),
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            if (first) {
                Column(Modifier.fillMaxWidth().padding(top = 32.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                    Image(painterResource(R.drawable.ic_logo), contentDescription = null, modifier = Modifier.size(84.dp))
                    Spacer(Modifier.height(16.dp))
                    Text("PowerDeck", style = MaterialTheme.typography.displaySmall)
                    Spacer(Modifier.height(6.dp))
                    Text("Turn your computers on and off from anywhere.", style = MaterialTheme.typography.bodyLarge,
                        color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
                }
                InstallSteps()
            }

            val found = state.found.orEmpty()
            if (found.isNotEmpty() || state.finding) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    SectionTitle("Found on your tailnet", Modifier.weight(1f))
                    if (state.finding) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                    else IconButton(onClick = a.find) { Icon(Icons.Rounded.Refresh, contentDescription = "Search again") }
                }
                found.forEach { p -> FoundRow(p, enabled = !working, onAdd = { if (p.isKvm) kvmFor = p else a.addPeer(p) }) }
            }

            Button(onClick = { a.dismiss(); a.scan() }, enabled = !working, modifier = Modifier.fillMaxWidth().height(56.dp), shape = RoundedCornerShape(18.dp)) {
                Icon(Icons.Rounded.QrCodeScanner, contentDescription = null)
                Spacer(Modifier.width(10.dp))
                Text("Scan the helper's code", style = MaterialTheme.typography.titleMedium)
            }
            OutlinedTextField(
                value = name,
                onValueChange = { name = it },
                label = { Text("Or type its Tailscale name") },
                placeholder = { Text("my-pc") },
                singleLine = true,
                enabled = !working,
                modifier = Modifier.fillMaxWidth(),
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Go, keyboardType = KeyboardType.Uri, autoCorrectEnabled = false),
                keyboardActions = KeyboardActions(onGo = { if (name.isNotBlank()) a.byName(name) }),
                trailingIcon = { if (name.isNotBlank()) TextButton(onClick = { a.byName(name) }, enabled = !working) { Text("Add") } },
            )

            when (step) {
                is AddStep.Working -> Row(verticalAlignment = Alignment.CenterVertically) {
                    CircularProgressIndicator(Modifier.size(22.dp), strokeWidth = 2.5.dp)
                    Spacer(Modifier.width(12.dp))
                    Text("Adding ${step.what}…", style = MaterialTheme.typography.bodyLarge)
                }
                is AddStep.Failed -> Text(step.message, color = MaterialTheme.colorScheme.error)
                else -> Unit
            }
            if (!first) {
                SectionTitle("Not installed yet?")
                Text("Run this on the computer, then scan the code it shows.", style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Command("Windows (PowerShell)", INSTALL_WINDOWS)
                Command("Linux / Proxmox (terminal)", INSTALL_LINUX)
            }
            Text(
                "PowerDeck only talks to your devices over your own Tailscale network. Nothing goes through anyone else's servers.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(bottom = 32.dp),
            )
        }
    }

    if (step is AddStep.NeedsCode) CodeDialog(step.name, onSubmit = a.submitCode, onDismiss = a.dismiss)
    if (step is AddStep.Confirm) AlertDialog(
        onDismissRequest = a.dismiss,
        title = { Text("Add ${step.info.name}?") },
        text = { Text("A link asked PowerDeck to add ${step.info.name} (${step.info.host}). Add it only if it's one of your devices.") },
        confirmButton = { Button(onClick = a.confirmLink) { Text("Add") } },
        dismissButton = { TextButton(onClick = a.dismiss) { Text("Cancel") } },
    )
    kvmFor?.let { p -> KvmDialog(p, state.devices, onLink = a.linkKvm, onDismiss = { kvmFor = null }) }
}

@Composable
private fun FoundRow(p: Peer, enabled: Boolean, onAdd: () -> Unit) {
    Card(shape = RoundedCornerShape(20.dp), colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer), modifier = Modifier.fillMaxWidth()) {
        Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
            val icon = if (p.isKvm) Icons.Rounded.SettingsInputComponent else Device(id = "", name = p.name, host = null, pin = null, os = p.os).icon()
            IconTile(icon)
            Spacer(Modifier.width(16.dp))
            Column(Modifier.weight(1f)) {
                Text(p.name, style = MaterialTheme.typography.titleMedium)
                Text(
                    when {
                        p.kind == "nanokvm" -> "NanoKVM"
                        p.kind == "pikvm" -> "PiKVM"
                        p.os == "windows" -> "Windows PC"
                        else -> p.os?.replaceFirstChar { it.uppercase() } ?: "Device"
                    },
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            FilledTonalButton(onClick = onAdd, enabled = enabled) { Text(if (p.isKvm) "Set up" else "Add") }
        }
    }
}

@Composable
private fun CodeDialog(name: String, onSubmit: (String) -> Unit, onDismiss: () -> Unit) {
    var code by rememberSaveable { mutableStateOf("") }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Pairing code for $name") },
        text = {
            Column {
                Text("$name is on another Tailscale account. Enter the 8-character code it shows (PowerDeck window, or run powerdeck-helper qr).")
                Spacer(Modifier.height(12.dp))
                OutlinedTextField(
                    value = code, onValueChange = { code = it.take(9) }, singleLine = true, placeholder = { Text("XXXX-XXXX") },
                    keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Characters, autoCorrectEnabled = false, imeAction = ImeAction.Done),
                    keyboardActions = KeyboardActions(onDone = { onSubmit(code) }),
                )
            }
        },
        confirmButton = { Button(onClick = { onSubmit(code) }, enabled = code.length >= 8) { Text("Add") } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

private const val NEW = "new"

/** Links a KVM: its login, and which device's power button it's wired to. */
@Composable
fun KvmDialog(
    p: Peer,
    devices: List<Device>,
    onLink: (deviceId: String?, newName: String, peer: Peer, user: String, password: String, done: (Boolean) -> Unit) -> Unit,
    onDismiss: () -> Unit,
    fixedDevice: Device? = null,
) {
    var password by rememberSaveable { mutableStateOf("") }
    var user by rememberSaveable { mutableStateOf("admin") }
    var shown by rememberSaveable { mutableStateOf(false) }
    val choices = fixedDevice?.let { listOf(it) } ?: devices.filter { it.kvm == null }
    // Nothing preselected: linking the wrong computer would press the wrong power button.
    var target by rememberSaveable { mutableStateOf(fixedDevice?.id) }
    var newName by rememberSaveable { mutableStateOf("") }
    var working by remember { mutableStateOf(false) }
    AlertDialog(
        onDismissRequest = { if (!working) onDismiss() },
        title = { Text("Set up ${p.name}") },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState())) {
                if (fixedDevice == null) {
                    Text("Which computer's power button is it wired to?", style = MaterialTheme.typography.bodyMedium)
                    choices.forEach { d ->
                        Row(Modifier.fillMaxWidth().selectable(target == d.id) { target = d.id }, verticalAlignment = Alignment.CenterVertically) {
                            RadioButton(selected = target == d.id, onClick = { target = d.id })
                            Text(d.name)
                        }
                    }
                    Row(Modifier.fillMaxWidth().selectable(target == NEW) { target = NEW }, verticalAlignment = Alignment.CenterVertically) {
                        RadioButton(selected = target == NEW, onClick = { target = NEW })
                        Text("A computer without the helper")
                    }
                    if (target == NEW) {
                        OutlinedTextField(value = newName, onValueChange = { newName = it.take(40) }, label = { Text("Its name") }, singleLine = true, modifier = Modifier.fillMaxWidth())
                    }
                    Spacer(Modifier.height(12.dp))
                }
                OutlinedTextField(
                    value = password, onValueChange = { password = it }, label = { Text("${if (p.kind == "pikvm") "PiKVM" else "NanoKVM"} password") }, singleLine = true,
                    visualTransformation = if (shown) VisualTransformation.None else PasswordVisualTransformation(),
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
                    trailingIcon = {
                        IconButton(onClick = { shown = !shown }) {
                            Icon(if (shown) Icons.Rounded.VisibilityOff else Icons.Rounded.Visibility, contentDescription = if (shown) "Hide" else "Show")
                        }
                    },
                    modifier = Modifier.fillMaxWidth().semantics { contentType = ContentType.Password },
                )
                OutlinedTextField(value = user, onValueChange = { user = it }, label = { Text("Username") }, singleLine = true,
                    modifier = Modifier.fillMaxWidth().padding(top = 8.dp).semantics { contentType = ContentType.Username })
                Spacer(Modifier.height(10.dp))
                Text(
                    "Stored only on this phone, encrypted by its secure hardware. PowerDeck checks that the power light matches the computer before linking, and presses the button only when the light confirms it's off (to turn on) or on (to shut down).",
                    style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        },
        confirmButton = {
            Button(
                onClick = {
                    working = true
                    onLink(target.takeIf { it != NEW }, newName, p, user, password) { ok -> working = false; if (ok) onDismiss() }
                },
                enabled = !working && password.isNotEmpty() && target != null && (target != NEW || newName.isNotBlank()),
            ) { if (working) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp) else Text("Connect") }
        },
        dismissButton = { OutlinedButton(onClick = onDismiss, enabled = !working) { Text("Cancel") } },
    )
}
