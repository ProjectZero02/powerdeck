package net.odysseylabs.powerdeck.ui

import android.content.Context
import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.PowerSettingsNew
import androidx.compose.material.icons.rounded.RestartAlt
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.DeviceState
import net.odysseylabs.powerdeck.data.Outcome
import net.odysseylabs.powerdeck.data.Pending
import net.odysseylabs.powerdeck.data.PowerDeck
import net.odysseylabs.powerdeck.data.Ask
import net.odysseylabs.powerdeck.deck
import kotlinx.coroutines.flow.map
import androidx.compose.runtime.collectAsState

/**
 * The small dialog the widget, tiles and shortcuts open: checks the device, then turns it on straight
 * away, or asks before shutting it down.
 */
class QuickActionActivity : ComponentActivity() {
    companion object {
        private const val EXTRA_DEVICE = "device"
        fun intent(ctx: Context, deviceId: String): Intent =
            Intent(ctx, QuickActionActivity::class.java)
                .setAction("net.odysseylabs.powerdeck.QUICK")
                .putExtra(EXTRA_DEVICE, deviceId)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK)
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val id = intent.getStringExtra(EXTRA_DEVICE) ?: return finish()
        setContent { PowerDeckTheme { QuickAction(id, deck, ::finish) } }
    }
}

private sealed interface QuickStep {
    data object Checking : QuickStep
    data class AskOff(val canRestart: Boolean, val pending: Pending?) : QuickStep
    data object AskOn : QuickStep
    data class AskForce(val restart: Boolean, val ask: Ask) : QuickStep
    data object Working : QuickStep
    data class Done(val outcome: Outcome) : QuickStep
}

@Composable
private fun QuickAction(id: String, deck: PowerDeck, close: () -> Unit) {
    val scope = rememberCoroutineScope()
    val ctx = LocalContext.current
    var step by remember { mutableStateOf<QuickStep>(QuickStep.Checking) }
    var device by remember { mutableStateOf<Device?>(null) }

    fun perform(block: suspend () -> Outcome, restart: Boolean = false) {
        step = QuickStep.Working
        scope.launch {
            val outcome = block()
            if (outcome.ask != null) {
                step = QuickStep.AskForce(restart, outcome.ask)
                return@launch
            }
            step = QuickStep.Done(outcome)
            Shortcuts.refreshAll(ctx)
            delay(if (outcome.ok) 2500 else 6000)
            close()
        }
    }

    LaunchedEffect(Unit) {
        val d = deck.store.get(id) ?: run {
            step = QuickStep.Done(Outcome(false, "That device was removed from PowerDeck."))
            return@LaunchedEffect
        }
        device = d
        if (!deck.tailnet()) {
            step = QuickStep.Done(Outcome(false, "Tailscale isn't connected. Open Tailscale and connect."))
            return@LaunchedEffect
        }
        when (val s = deck.state(d)) {
            is DeviceState.On -> step = QuickStep.AskOff(d.hasHelper, s.status?.pending)
            DeviceState.Off -> step = QuickStep.AskOn
            is DeviceState.Problem -> step = QuickStep.Done(Outcome(false, s.message))
            DeviceState.Checking -> Unit
        }
        Shortcuts.refreshAll(ctx)
    }

    val countdown by remember { deck.store.settings.map { it.countdownSeconds } }.collectAsState(60)
    val s = step
    val d = device
    val name = d?.name ?: "PowerDeck"
    AlertDialog(
        onDismissRequest = { if (s !is QuickStep.Working) close() },
        title = { Text(name) },
        text = {
            when (s) {
                QuickStep.Checking, QuickStep.Working -> Row(verticalAlignment = Alignment.CenterVertically) {
                    CircularProgressIndicator(Modifier.size(22.dp), strokeWidth = 2.5.dp)
                    Spacer(Modifier.width(14.dp))
                    Text(if (s == QuickStep.Checking) "Checking…" else "Working…")
                }
                is QuickStep.AskOff -> Text(
                    when {
                        s.pending != null -> "A ${if (s.pending.action == "restart") "restart" else "shutdown"} is counting down."
                        d?.guests == true -> "It's on. Shut it down? Its VMs stop cleanly first, then it powers off."
                        else -> if (countdown == 0) "It's on. Shut it down now?" else "It's on. Shut it down? Apps close ${inTime(countdown)}, and you can cancel until then."
                    },
                )
                QuickStep.AskOn -> Text("It's off. Turn it on?")
                is QuickStep.AskForce -> Text(s.ask.text)
                is QuickStep.Done -> Text(s.outcome.message, color = if (s.outcome.ok) MaterialTheme.colorScheme.onSurface else MaterialTheme.colorScheme.error)
            }
        },
        confirmButton = {
            if (d != null) when (s) {
                is QuickStep.AskOff -> Column(horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.fillMaxWidth()) {
                    if (s.pending != null) {
                        Button(onClick = { perform({ deck.cancel(d) }) }) { Text("Cancel it") }
                    } else {
                        HoldButton("Hold to shut down", Icons.Rounded.PowerSettingsNew, danger = true, enabled = true, modifier = Modifier.fillMaxWidth()) {
                            perform({ deck.powerOff(d, restart = false) })
                        }
                        if (s.canRestart) {
                            HoldButton("Hold to restart", Icons.Rounded.RestartAlt, danger = false, enabled = true, tonal = true, modifier = Modifier.fillMaxWidth()) {
                                perform({ deck.powerOff(d, restart = true) }, restart = true)
                            }
                        }
                    }
                }
                QuickStep.AskOn -> Button(onClick = { perform({ deck.turnOn(d) }) }) { Text("Turn on") }
                is QuickStep.AskForce -> Button(
                    onClick = { perform({ if (s.ask.kvmPress) deck.powerOff(d, s.restart, pressKvm = true) else deck.powerOff(d, s.restart, force = true) }) },
                    colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error),
                ) { Text(s.ask.confirm) }
                is QuickStep.Done -> TextButton(onClick = close) { Text("OK") }
                else -> Unit
            }
        },
        dismissButton = {
            if (s is QuickStep.AskOff || s is QuickStep.AskForce || s is QuickStep.AskOn) TextButton(onClick = close) { Text("Close") }
        },
    )
}
