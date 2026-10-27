package net.odysseylabs.powerdeck.ui

import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.material.icons.rounded.Smartphone
import androidx.compose.material.icons.rounded.DesktopWindows
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.ui.text.style.TextOverflow
import net.odysseylabs.powerdeck.data.Settings
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.rotate
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.withLink
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
import androidx.compose.material.icons.rounded.QrCodeScanner
import androidx.compose.material.icons.rounded.Refresh
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
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.contentType
import androidx.compose.ui.semantics.semantics
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

const val INSTALL_GUIDE = "https://github.com/ProjectZero02/powerdeck#set-up-in-3-steps"

data class AddActions(
    val back: (() -> Unit)? = null,
    val scan: () -> Unit = {},
    val byName: (String) -> Unit = {},
    val addPeer: (Peer) -> Unit = {},
    val submitCode: (String) -> Unit = {},
    val dismiss: () -> Unit = {},
    val confirmLink: (name: String) -> Unit = {},
    val setHold: (Boolean) -> Unit = {},
    val setCountdown: (Int) -> Unit = {},
    val find: () -> Unit = {},
    val install: () -> Unit = {},
    val linkKvm: (deviceId: String?, newName: String, peer: Peer, user: String, password: String, done: (Boolean) -> Unit) -> Unit = { _, _, _, _, _, _ -> },
)

@Composable
private fun Step(n: Int, title: AnnotatedString) {
    Row {
        Surface(shape = CircleShape, color = MaterialTheme.colorScheme.primary, modifier = Modifier.size(28.dp)) {
            Text("$n", color = MaterialTheme.colorScheme.onPrimary, textAlign = TextAlign.Center, modifier = Modifier.padding(top = 4.dp), fontWeight = FontWeight.Bold)
        }
        Spacer(Modifier.width(14.dp))
        Text(title, style = MaterialTheme.typography.bodyLarge, modifier = Modifier.weight(1f).padding(top = 3.dp))
    }
}

/** [before] **Install** [after], with Install opening the install guide on GitHub. */
@Composable
private fun installText(before: String, after: String, onInstall: () -> Unit): AnnotatedString {
    val link = TextLinkStyles(SpanStyle(fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.primary))
    return buildAnnotatedString {
        append(before)
        withLink(LinkAnnotation.Clickable("install", link) { onInstall() }) { append("Install") }
        append(after)
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
        bottomBar = {
            Surface(color = MaterialTheme.colorScheme.background) {
                Button(
                    onClick = { a.dismiss(); a.scan() },
                    enabled = !working,
                    modifier = Modifier.fillMaxWidth().navigationBarsPadding().imePadding().padding(horizontal = Margin, vertical = 12.dp).height(56.dp),
                    shape = RoundedCornerShape(18.dp),
                ) {
                    Icon(Icons.Rounded.QrCodeScanner, contentDescription = null)
                    Spacer(Modifier.width(10.dp))
                    Text("Scan the helper's code", style = MaterialTheme.typography.titleMedium)
                }
            }
        },
    ) { pad ->
        Column(
            Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = Margin),
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
                Card(shape = RoundedCornerShape(24.dp), colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer), modifier = Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(18.dp)) {
                        Step(1, AnnotatedString("Put Tailscale on this phone and on each computer, signed in to the same account."))
                        Step(2, installText("", " PowerDeck on each computer.", a.install))
                        Step(3, AnnotatedString("Scan the code it shows. Everything else on your tailnet then shows up here to add with one tap."))
                    }
                }
            } else {
                val found = state.found.orEmpty()
                when {
                    found.isNotEmpty() -> {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            SectionTitle("Found on your tailnet", Modifier.weight(1f))
                            if (state.finding) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                            else IconButton(onClick = a.find) { Icon(Icons.Rounded.Refresh, contentDescription = "Search again") }
                        }
                        found.forEach { p -> FoundRow(p, enabled = !working, onAdd = { if (p.isKvm) kvmFor = p else a.addPeer(p) }) }
                    }
                    else -> Searching(state.finding || state.found == null, a.find)
                }
            }

            OutlinedTextField(
                value = name,
                onValueChange = { name = it },
                label = { Text("Tailscale name or IP") },
                placeholder = { Text("my-pc or 100.x.y.z") },
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
                Text(
                    installText("New computer? ", " PowerDeck on it, then scan the code it shows.", a.install),
                    style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            Text(
                "PowerDeck only talks to your devices over your own Tailscale network. Nothing goes through anyone else's servers.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(bottom = 16.dp),
            )
        }
    }

    if (step is AddStep.NeedsCode) CodeDialog(step.name, onSubmit = a.submitCode, onDismiss = a.dismiss)
    if (step is AddStep.Review) AddSheet(step, first, state.settings, a)
    kvmFor?.let { p -> KvmDialog(p, state.devices, onLink = a.linkKvm, onDismiss = { kvmFor = null }) }
}

/** "Add My PC": name it for this phone. The first time, also how its buttons behave. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun AddSheet(step: AddStep.Review, first: Boolean, settings: Settings, a: AddActions) {
    var name by rememberSaveable(step.info.host) { mutableStateOf(step.info.name) }
    val sheet = rememberModalBottomSheetState(skipPartiallyExpanded = true)
    ModalBottomSheet(onDismissRequest = a.dismiss, sheetState = sheet) {
        Column(
            Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(horizontal = Margin).padding(bottom = 24.dp).imePadding(),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                IconTile(Icons.Rounded.DesktopWindows)
                Spacer(Modifier.width(16.dp))
                Column(Modifier.weight(1f)) {
                    Text("Add ${name.ifBlank { step.info.name }}", style = MaterialTheme.typography.titleLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(step.info.host, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                        maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            }
            if (step.external) {
                Text("A link from another app asked PowerDeck to add this. Add it only if it's one of your devices.",
                    style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.error)
            }
            OutlinedTextField(
                value = name,
                onValueChange = { name = it.take(40) },
                label = { Text("Name") },
                supportingText = { Text("Only on this phone. You can change it later.") },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
                keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Words, imeAction = ImeAction.Done),
            )
            if (first) {
                SectionTitle("Buttons")
                Card(shape = RoundedCornerShape(20.dp), colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer)) {
                    ButtonSettings(settings, a.setHold, a.setCountdown)
                }
                Text("These apply to every device. Change them any time in Settings.",
                    style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End)) {
                TextButton(onClick = a.dismiss) { Text("Cancel") }
                Button(onClick = { a.confirmLink(name.trim()) }, enabled = name.isNotBlank()) { Text("Add") }
            }
        }
    }
}

/** Radar while the helpers look around the tailnet; "Search again" once they've found nothing new. */
@Composable
private fun Searching(active: Boolean, onAgain: () -> Unit) {
    Column(Modifier.fillMaxWidth().padding(vertical = 8.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        Radar(active, Modifier.size(168.dp))
        Spacer(Modifier.height(12.dp))
        if (active) {
            Text("Searching for devices on your tailnet", style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center)
            Text("Computers with PowerDeck and NanoKVMs show up here.", style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
        } else {
            Text("Nothing new on your tailnet", style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center)
            Text("Install PowerDeck on a computer and it shows up here.", style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
            TextButton(onClick = onAgain) {
                Icon(Icons.Rounded.Refresh, contentDescription = null, modifier = Modifier.size(18.dp))
                Spacer(Modifier.width(6.dp))
                Text("Search again")
            }
        }
    }
}

@Composable
private fun Radar(active: Boolean, modifier: Modifier = Modifier) {
    val brand = MaterialTheme.colorScheme.primary
    val ring = MaterialTheme.colorScheme.outlineVariant
    val t = rememberInfiniteTransition(label = "radar")
    val pulse by t.animateFloat(0f, 1f, infiniteRepeatable(tween(2400, easing = LinearEasing)), label = "pulse")
    val sweep by t.animateFloat(0f, 360f, infiniteRepeatable(tween(3000, easing = LinearEasing)), label = "sweep")
    Box(modifier, contentAlignment = Alignment.Center) {
        Canvas(Modifier.fillMaxSize()) {
            val r = size.minDimension / 2
            for (i in 1..3) drawCircle(ring, r * i / 3f, style = Stroke(1.dp.toPx()))
            if (active) {
                for (i in 0..2) {
                    val p = (pulse + i / 3f) % 1f
                    drawCircle(brand.copy(alpha = 0.35f * (1f - p)), r * (0.25f + 0.75f * p), style = Stroke(2.dp.toPx()))
                }
                rotate(sweep) {
                    drawArc(
                        Brush.sweepGradient(0.75f to Color.Transparent, 1f to brand.copy(alpha = 0.35f)),
                        startAngle = 0f, sweepAngle = 360f, useCenter = true,
                    )
                }
            }
        }
        Surface(shape = CircleShape, color = MaterialTheme.colorScheme.primaryContainer, modifier = Modifier.size(52.dp)) {
            Box(contentAlignment = Alignment.Center) {
                Icon(Icons.Rounded.Smartphone, contentDescription = null, tint = MaterialTheme.colorScheme.onPrimaryContainer)
            }
        }
    }
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
