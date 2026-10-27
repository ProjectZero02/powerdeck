package net.odysseylabs.powerdeck.tiles

import android.app.PendingIntent
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import net.odysseylabs.powerdeck.data.PcState
import net.odysseylabs.powerdeck.data.Power
import net.odysseylabs.powerdeck.data.ServerState
import net.odysseylabs.powerdeck.data.Tailscale
import net.odysseylabs.powerdeck.deck
import net.odysseylabs.powerdeck.ui.QuickActionActivity
import net.odysseylabs.powerdeck.ui.Target

/** Quick Settings tile showing a device's power state; tapping opens the quick action dialog. */
abstract class PowerTileService(private val target: Target) : TileService() {
    private var scope: CoroutineScope? = null

    override fun onStartListening() {
        super.onStartListening()
        val s = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate).also { scope = it }
        s.launch {
            val snap = deck.store.snapshot.first()
            render(if (target == Target.Pc) snap.pc else snap.server)
            // Refresh only if Tailscale is already up; tiles never switch the VPN on by themselves.
            if (Tailscale.vpnActive(this@PowerTileService)) {
                val fresh = when (target) {
                    Target.Pc -> if (deck.pcState() is PcState.On) Power.On else Power.Off
                    Target.Server -> when (deck.serverState()) {
                        is ServerState.On -> Power.On
                        ServerState.Off -> Power.Off
                        else -> Power.Unknown
                    }
                }
                render(fresh)
            }
        }
    }

    override fun onStopListening() {
        scope?.cancel()
        scope = null
        super.onStopListening()
    }

    private fun render(power: Power) {
        val tile = qsTile ?: return
        tile.state = if (power == Power.On) Tile.STATE_ACTIVE else Tile.STATE_INACTIVE
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            tile.subtitle = when (power) {
                Power.On -> "On"
                Power.Off -> "Off"
                Power.Unknown -> "Tap to check"
            }
        }
        tile.updateTile()
    }

    override fun onClick() {
        super.onClick()
        val intent = QuickActionActivity.intent(this, target)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startActivityAndCollapse(PendingIntent.getActivity(this, target.ordinal, intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT))
        } else {
            @Suppress("DEPRECATION", "StartActivityAndCollapseDeprecated")
            startActivityAndCollapse(intent)
        }
    }
}

class PcTileService : PowerTileService(Target.Pc)

class ServerTileService : PowerTileService(Target.Server)
