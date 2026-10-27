package net.odysseylabs.powerdeck.ui

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
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.ContentPaste
import androidx.compose.material.icons.rounded.Lock
import androidx.compose.material.icons.rounded.QrCodeScanner
import androidx.compose.material.icons.rounded.Visibility
import androidx.compose.material.icons.rounded.VisibilityOff
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.autofill.ContentType
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.contentType
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import net.odysseylabs.powerdeck.R

@Composable
private fun StepRow(n: Int, text: String) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Surface(shape = CircleShape, color = MaterialTheme.colorScheme.primary, modifier = Modifier.size(28.dp)) {
            Text("$n", color = MaterialTheme.colorScheme.onPrimary, textAlign = TextAlign.Center, modifier = Modifier.padding(top = 4.dp), fontWeight = FontWeight.Bold)
        }
        Spacer(Modifier.width(14.dp))
        Text(text, style = MaterialTheme.typography.bodyLarge)
    }
}

/** First run: pair with the PC by scanning the code PowerDeck shows there. */
@Composable
fun WelcomeScreen(pairing: PairingStep, onScan: () -> Unit, onPaste: (String) -> Unit, onDismissError: () -> Unit) {
    var showPaste by rememberSaveable { mutableStateOf(false) }
    var pasted by rememberSaveable { mutableStateOf("") }
    Surface(color = MaterialTheme.colorScheme.background, modifier = Modifier.fillMaxSize()) {
        Column(
            Modifier.safeDrawingPadding().verticalScroll(rememberScrollState()).padding(24.dp).imePadding(),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Spacer(Modifier.height(32.dp))
            Image(painterResource(R.drawable.ic_logo), contentDescription = null, modifier = Modifier.size(96.dp))
            Spacer(Modifier.height(20.dp))
            Text("PowerDeck", style = MaterialTheme.typography.displaySmall, fontWeight = FontWeight.SemiBold)
            Spacer(Modifier.height(8.dp))
            Text(
                "Turn your PC and server on and off from anywhere.",
                style = MaterialTheme.typography.bodyLarge,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                textAlign = TextAlign.Center,
            )
            Spacer(Modifier.height(32.dp))
            Card(
                shape = RoundedCornerShape(24.dp),
                colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer),
                modifier = Modifier.fillMaxWidth(),
            ) {
                Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
                    StepRow(1, "Install PowerDeck on your PC and open it.")
                    StepRow(2, "Scan the pairing code it shows.")
                }
            }
            Spacer(Modifier.height(28.dp))
            when (pairing) {
                is PairingStep.Working -> {
                    CircularProgressIndicator()
                    Spacer(Modifier.height(12.dp))
                    Text("Pairing with ${pairing.name}…", style = MaterialTheme.typography.bodyLarge)
                }
                else -> {
                    Button(onClick = { onDismissError(); onScan() }, modifier = Modifier.fillMaxWidth().height(56.dp)) {
                        Icon(Icons.Rounded.QrCodeScanner, contentDescription = null)
                        Spacer(Modifier.width(10.dp))
                        Text("Scan pairing code", style = MaterialTheme.typography.titleMedium)
                    }
                    TextButton(onClick = { showPaste = !showPaste }) {
                        Icon(Icons.Rounded.ContentPaste, contentDescription = null, modifier = Modifier.size(18.dp))
                        Spacer(Modifier.width(6.dp))
                        Text("Paste a pairing link instead")
                    }
                    if (showPaste) {
                        OutlinedTextField(
                            value = pasted,
                            onValueChange = { pasted = it },
                            label = { Text("powerdeck://pair?…") },
                            singleLine = true,
                            modifier = Modifier.fillMaxWidth(),
                            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Go, keyboardType = KeyboardType.Uri),
                            keyboardActions = KeyboardActions(onGo = { onPaste(pasted) }),
                        )
                        Spacer(Modifier.height(8.dp))
                        Button(onClick = { onPaste(pasted) }, enabled = pasted.isNotBlank(), modifier = Modifier.fillMaxWidth()) { Text("Pair") }
                    }
                }
            }
            if (pairing is PairingStep.Failed) {
                Spacer(Modifier.height(16.dp))
                Text(pairing.message, color = MaterialTheme.colorScheme.error, textAlign = TextAlign.Center)
            }
            Spacer(Modifier.height(24.dp))
            Text(
                "PowerDeck talks to your devices only over your own Tailscale network.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                textAlign = TextAlign.Center,
            )
        }
    }
}

/** Server power needs the NanoKVM login; the password stays on this phone, encrypted. */
@Composable
fun KvmSetupScreen(
    address: String,
    user: String,
    step: KvmStep,
    onConnect: (address: String, user: String, password: String) -> Unit,
    onSkip: () -> Unit,
) {
    var addr by rememberSaveable { mutableStateOf(address) }
    var name by rememberSaveable { mutableStateOf(user) }
    var password by rememberSaveable { mutableStateOf("") }
    var shown by rememberSaveable { mutableStateOf(false) }
    var advanced by rememberSaveable { mutableStateOf(address.isBlank()) }
    val working = step is KvmStep.Working
    Surface(color = MaterialTheme.colorScheme.background, modifier = Modifier.fillMaxSize()) {
        Column(Modifier.safeDrawingPadding().verticalScroll(rememberScrollState()).padding(24.dp).imePadding()) {
            Spacer(Modifier.height(24.dp))
            Surface(shape = RoundedCornerShape(18.dp), color = MaterialTheme.colorScheme.primaryContainer, modifier = Modifier.size(64.dp)) {
                Icon(Icons.Rounded.Lock, contentDescription = null, tint = MaterialTheme.colorScheme.onPrimaryContainer, modifier = Modifier.padding(16.dp))
            }
            Spacer(Modifier.height(20.dp))
            Text("Server power", style = MaterialTheme.typography.headlineMedium, fontWeight = FontWeight.SemiBold)
            Spacer(Modifier.height(8.dp))
            Text(
                if (address.isNotBlank()) "Your PC found the NanoKVM at $address. Enter its password so PowerDeck can press the server's power button and wake your PC."
                else "Enter your NanoKVM's Tailscale address and password so PowerDeck can press the server's power button and wake your PC.",
                style = MaterialTheme.typography.bodyLarge,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(Modifier.height(24.dp))
            OutlinedTextField(
                value = password,
                onValueChange = { password = it },
                label = { Text("NanoKVM password") },
                singleLine = true,
                visualTransformation = if (shown) VisualTransformation.None else PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, imeAction = ImeAction.Done),
                keyboardActions = KeyboardActions(onDone = { if (password.isNotEmpty()) onConnect(addr, name, password) }),
                trailingIcon = {
                    IconButton(onClick = { shown = !shown }) {
                        Icon(if (shown) Icons.Rounded.VisibilityOff else Icons.Rounded.Visibility, contentDescription = if (shown) "Hide" else "Show")
                    }
                },
                enabled = !working,
                modifier = Modifier.fillMaxWidth().semantics { contentType = ContentType.Password },
            )
            if (advanced) {
                Spacer(Modifier.height(12.dp))
                OutlinedTextField(
                    value = addr, onValueChange = { addr = it }, label = { Text("NanoKVM address") }, singleLine = true, enabled = !working,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri), modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(12.dp))
                OutlinedTextField(
                    value = name, onValueChange = { name = it }, label = { Text("Username") }, singleLine = true, enabled = !working,
                    modifier = Modifier.fillMaxWidth().semantics { contentType = ContentType.Username },
                )
            } else {
                TextButton(onClick = { advanced = true }) { Text("Change address or username") }
            }
            if (step is KvmStep.Failed) {
                Spacer(Modifier.height(12.dp))
                Text(step.message, color = MaterialTheme.colorScheme.error)
            }
            Spacer(Modifier.height(24.dp))
            Button(
                onClick = { onConnect(addr, name, password) },
                enabled = !working && password.isNotEmpty() && addr.isNotBlank(),
                modifier = Modifier.fillMaxWidth().height(56.dp),
            ) {
                if (working) CircularProgressIndicator(Modifier.size(22.dp), color = Color.White, strokeWidth = 2.5.dp) else Text("Connect", style = MaterialTheme.typography.titleMedium)
            }
            TextButton(onClick = onSkip, enabled = !working, modifier = Modifier.align(Alignment.CenterHorizontally)) { Text("Skip for now") }
            Spacer(Modifier.height(16.dp))
            Text(
                "The password is stored only on this phone, encrypted with a key that never leaves its secure hardware.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}
