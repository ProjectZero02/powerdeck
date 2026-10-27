package net.odysseylabs.powerdeck.ui

import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.service.quicksettings.TileService
import android.content.ComponentName
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
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
import androidx.compose.ui.unit.dp
import androidx.glance.appwidget.updateAll
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import net.odysseylabs.powerdeck.data.Outcome
import net.odysseylabs.powerdeck.data.PcState
import net.odysseylabs.powerdeck.data.PowerDeck
import net.odysseylabs.powerdeck.data.ServerState
import net.odysseylabs.powerdeck.deck
import net.odysseylabs.powerdeck.tiles.PcTileService
import net.odysseylabs.powerdeck.tiles.ServerTileService
import net.odysseylabs.powerdeck.widget.PowerWidget

/**
 * The small dialog the widget and Quick Settings tiles open: checks the device, then turns it on
 * straight away, or asks before shutting it down.
 */
class QuickActionActivity : ComponentActivity() {
    companion object {
        private const val EXTRA_TARGET = "target"
        fun intent(ctx: Context, target: Target): Intent =
            Intent(ctx, QuickActionActivity::class.java)
                .putExtra(EXTRA_TARGET, target.name)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK)
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val target = runCatching { Target.valueOf(intent.getStringExtra(EXTRA_TARGET)!!) }.getOrNull() ?: return finish()
        setContent { PowerDeckTheme { QuickAction(target, deck, ::finish) } }
    }

    override fun onStop() {
        super.onStop()
        TileService.requestListeningState(this, ComponentName(this, PcTileService::class.java))
        TileService.requestListeningState(this, ComponentName(this, ServerTileService::class.java))
    }
}

private sealed interface QuickStep {
    data object Checking : QuickStep
    data class AskOff(val restartable: Boolean, val pending: Boolean) : QuickStep
    data object Working : QuickStep
    data class Done(val outcome: Outcome) : QuickStep
}

@Composable
private fun QuickAction(target: Target, deck: PowerDeck, close: () -> Unit) {
    val scope = rememberCoroutineScope()
    val ctx = androidx.compose.ui.platform.LocalContext.current
    var step by remember { mutableStateOf<QuickStep>(QuickStep.Checking) }
    var name by remember { mutableStateOf(if (target == Target.Pc) "PC" else "Server") }

    fun perform(block: suspend () -> Outcome) {
        step = QuickStep.Working
        scope.launch {
            val outcome = block()
            step = QuickStep.Done(outcome)
            PowerWidget().updateAll(ctx)
            delay(if (outcome.ok) 2200 else 6000)
            close()
        }
    }

    LaunchedEffect(Unit) {
        val config = deck.store.current()
        if (target == Target.Pc) name = config.pc?.name ?: "PC"
        if (!deck.tailnet()) {
            step = QuickStep.Done(Outcome(false, "Tailscale isn't connected. Open Tailscale and connect."))
            return@LaunchedEffect
        }
        when (target) {
            Target.Pc -> when (val s = deck.pcState()) {
                is PcState.On -> if (s.pending == null && !config.confirmShutdown) perform { deck.pcAction("shutdown") } else step = QuickStep.AskOff(true, s.pending != null)
                PcState.Off -> perform { deck.pcOn() }
                is PcState.Problem -> step = QuickStep.Done(Outcome(false, s.message))
                PcState.Checking -> Unit
            }
            Target.Server -> when (val s = deck.serverState()) {
                is ServerState.On -> if (!config.confirmShutdown) perform { deck.serverOff() } else step = QuickStep.AskOff(false, false)
                ServerState.Off -> perform { deck.serverOn() }
                ServerState.NotSetUp -> step = QuickStep.Done(Outcome(false, "Open PowerDeck and connect your NanoKVM first."))
                is ServerState.Problem -> step = QuickStep.Done(Outcome(false, s.message))
                ServerState.Checking -> Unit
            }
        }
        PowerWidget().updateAll(ctx)
    }

    val s = step
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
                        s.pending -> "A shutdown is already counting down."
                        target == Target.Pc -> "It's on. Shut it down? Windows closes all apps in 30 seconds."
                        else -> "It's on. Shut it down? Proxmox stops every VM cleanly first (1 to 3 minutes)."
                    },
                )
                is QuickStep.Done -> Text(s.outcome.message, color = if (s.outcome.ok) MaterialTheme.colorScheme.onSurface else MaterialTheme.colorScheme.error)
            }
        },
        confirmButton = {
            if (s is QuickStep.AskOff) {
                Column(horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    if (s.pending) {
                        Button(onClick = { perform { deck.pcAction("cancel") } }) { Text("Cancel shutdown") }
                    } else {
                        Button(
                            onClick = { perform { if (target == Target.Pc) deck.pcAction("shutdown") else deck.serverOff() } },
                            colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error),
                        ) { Text("Shut down") }
                        if (s.restartable) TextButton(onClick = { perform { deck.pcAction("restart") } }) { Text("Restart instead") }
                    }
                }
            } else if (s is QuickStep.Done) {
                TextButton(onClick = close) { Text("OK") }
            }
        },
        dismissButton = {
            if (s is QuickStep.AskOff) TextButton(onClick = close) { Text("Close") }
        },
    )
}
