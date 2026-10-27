package net.odysseylabs.powerdeck.tiles

import android.app.PendingIntent
import android.content.ComponentName
import android.content.Context
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import net.odysseylabs.powerdeck.data.DeviceState
import net.odysseylabs.powerdeck.data.Power
import net.odysseylabs.powerdeck.data.Tailscale
import net.odysseylabs.powerdeck.deck
import net.odysseylabs.powerdeck.ui.MainActivity
import net.odysseylabs.powerdeck.ui.QuickActionActivity

/**
 * A Quick Settings tile for whichever device is assigned to its slot (chosen on the device's page).
 * Shows the last known state; tapping opens the quick power dialog.
 */
abstract class PowerTileService(private val slot: Int) : TileService() {
    private var scope: CoroutineScope? = null
    private var deviceId: String? = null

    override fun onStartListening() {
        super.onStartListening()
        val s = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate).also { scope = it }
        s.launch {
            val store = deck.store
            val d = store.tiles.first().getOrNull(slot)?.let { store.get(it) }
            deviceId = d?.id
            if (d == null) {
                render("PowerDeck", "Tap to choose", Power.Unknown)
                return@launch
            }
            render(d.name, null, store.snapshot(d.id).power)
            // Refresh only if Tailscale is already up; tiles never switch the VPN on by themselves.
            if (Tailscale.vpnActive(this@PowerTileService)) {
                render(d.name, null, if (deck.state(d) is DeviceState.On) Power.On else Power.Off)
            }
        }
    }

    override fun onStopListening() {
        scope?.cancel()
        scope = null
        super.onStopListening()
    }

    private fun render(label: String, sub: String?, power: Power) {
        val tile = qsTile ?: return
        tile.label = label
        tile.state = if (power == Power.On) Tile.STATE_ACTIVE else Tile.STATE_INACTIVE
        tile.subtitle = sub ?: when (power) {
            Power.On -> "On"
            Power.Off -> "Off"
            Power.Unknown -> "Tap to check"
        }
        tile.updateTile()
    }

    override fun onClick() {
        super.onClick()
        val id = deviceId
        val intent = if (id != null) QuickActionActivity.intent(this, id)
        else android.content.Intent(this, MainActivity::class.java).addFlags(android.content.Intent.FLAG_ACTIVITY_NEW_TASK)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startActivityAndCollapse(PendingIntent.getActivity(this, slot, intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT))
        } else {
            @Suppress("DEPRECATION", "StartActivityAndCollapseDeprecated")
            startActivityAndCollapse(intent)
        }
    }

    companion object {
        const val SLOTS = 4
        private val classes = listOf(Tile1Service::class.java, Tile2Service::class.java, Tile3Service::class.java, Tile4Service::class.java)
        fun component(ctx: Context, slot: Int) = ComponentName(ctx, classes[slot])
    }
}

class Tile1Service : PowerTileService(0)
class Tile2Service : PowerTileService(1)
class Tile3Service : PowerTileService(2)
class Tile4Service : PowerTileService(3)
