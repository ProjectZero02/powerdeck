package net.odysseylabs.powerdeck.ui

import android.app.StatusBarManager
import android.appwidget.AppWidgetManager
import android.content.ComponentName
import android.content.Context
import android.graphics.drawable.Icon
import android.os.Build
import android.service.quicksettings.TileService
import android.widget.Toast
import androidx.core.content.pm.ShortcutInfoCompat
import androidx.core.content.pm.ShortcutManagerCompat
import androidx.core.graphics.drawable.IconCompat
import androidx.glance.appwidget.updateAll
import net.odysseylabs.powerdeck.R
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.deck
import net.odysseylabs.powerdeck.tiles.PowerTileService
import net.odysseylabs.powerdeck.widget.PowerWidget
import net.odysseylabs.powerdeck.widget.PowerWidgetReceiver

/** Home-screen widget, Quick Settings tiles and launcher shortcuts. */
object Shortcuts {
    private fun widget(ctx: Context) = ComponentName(ctx, PowerWidgetReceiver::class.java)

    fun hasWidget(ctx: Context): Boolean = AppWidgetManager.getInstance(ctx).getAppWidgetIds(widget(ctx)).isNotEmpty()

    /** Asks the launcher to place the widget (one confirmation from the user). */
    fun pinWidget(ctx: Context) {
        val mgr = AppWidgetManager.getInstance(ctx)
        if (mgr.isRequestPinAppWidgetSupported) mgr.requestPinAppWidget(widget(ctx), null, null)
        else Toast.makeText(ctx, "Long-press your home screen → Widgets → PowerDeck", Toast.LENGTH_LONG).show()
    }

    /** Offers a tile for one slot; Android 13+ shows a one-tap "Add tile" prompt. */
    fun requestTile(ctx: Context, slot: Int, label: String, then: () -> Unit = {}) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) {
            Toast.makeText(ctx, "Pull down Quick Settings, tap the pencil, and drag in PowerDeck ${slot + 1}.", Toast.LENGTH_LONG).show()
            return
        }
        val sbm = ctx.getSystemService(StatusBarManager::class.java) ?: return
        sbm.requestAddTileService(PowerTileService.component(ctx, slot), label, Icon.createWithResource(ctx, R.drawable.ic_power), ctx.mainExecutor) { then() }
    }

    /** Gives every device without a tile a free slot, then offers the tiles one after another. */
    fun addTiles(ctx: Context, devices: List<Device>, tiles: List<String?>, assign: (Int, Device) -> Unit) {
        val slots = tiles.toMutableList()
        for (d in devices) {
            if (d.id in slots) continue
            val free = slots.indexOf(null).takeIf { it >= 0 } ?: break
            slots[free] = d.id
            assign(free, d)
        }
        val todo = slots.mapIndexedNotNull { i, id -> devices.firstOrNull { it.id == id }?.let { i to it.name } }
        fun next(i: Int) {
            if (i < todo.size) requestTile(ctx, todo[i].first, todo[i].second) { next(i + 1) }
        }
        if (todo.isEmpty()) Toast.makeText(ctx, "Add a device first.", Toast.LENGTH_SHORT).show() else next(0)
    }

    private fun shortcut(ctx: Context, d: Device) = ShortcutInfoCompat.Builder(ctx, "device-${d.id}")
        .setShortLabel(d.name.take(20))
        .setLongLabel("Power ${d.name}")
        .setIcon(IconCompat.createWithResource(ctx, R.mipmap.ic_launcher))
        .setIntent(QuickActionActivity.intent(ctx, d.id))
        .build()

    /** A home-screen icon that opens the quick power dialog for one device. */
    fun pinDevice(ctx: Context, d: Device) {
        if (ShortcutManagerCompat.isRequestPinShortcutSupported(ctx)) ShortcutManagerCompat.requestPinShortcut(ctx, shortcut(ctx, d), null)
        else Toast.makeText(ctx, "Your launcher can't add shortcuts. Long-press the PowerDeck icon instead.", Toast.LENGTH_LONG).show()
    }

    /** Updates the widget, tiles and long-press shortcuts after anything changed. */
    suspend fun refreshAll(ctx: Context) {
        runCatching { PowerWidget().updateAll(ctx) }
        for (slot in 0 until PowerTileService.SLOTS) TileService.requestListeningState(ctx, PowerTileService.component(ctx, slot))
        val devices = ctx.deck.store.all()
        runCatching {
            val max = ShortcutManagerCompat.getMaxShortcutCountPerActivity(ctx).coerceAtMost(4)
            ShortcutManagerCompat.setDynamicShortcuts(ctx, devices.take(max).map { shortcut(ctx, it) })
        }
    }
}
