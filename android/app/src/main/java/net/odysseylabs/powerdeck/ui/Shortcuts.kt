package net.odysseylabs.powerdeck.ui

import android.app.StatusBarManager
import android.appwidget.AppWidgetManager
import android.content.ComponentName
import android.content.Context
import android.graphics.drawable.Icon
import android.os.Build
import android.widget.Toast
import net.odysseylabs.powerdeck.R
import net.odysseylabs.powerdeck.tiles.PcTileService
import net.odysseylabs.powerdeck.tiles.ServerTileService
import net.odysseylabs.powerdeck.widget.PowerWidgetReceiver

/** Home-screen widget and Quick Settings tile helpers. */
object Shortcuts {
    private fun widget(ctx: Context) = ComponentName(ctx, PowerWidgetReceiver::class.java)

    fun hasWidget(ctx: Context): Boolean =
        AppWidgetManager.getInstance(ctx).getAppWidgetIds(widget(ctx)).isNotEmpty()

    /** Asks the launcher to place the widget (one confirmation from the user). */
    fun pinWidget(ctx: Context) {
        val mgr = AppWidgetManager.getInstance(ctx)
        if (mgr.isRequestPinAppWidgetSupported) {
            mgr.requestPinAppWidget(widget(ctx), null, null)
        } else {
            Toast.makeText(ctx, "Long-press your home screen → Widgets → PowerDeck", Toast.LENGTH_LONG).show()
        }
    }

    /** Offers both tiles; Android 13+ shows a one-tap "Add tile" prompt for each. */
    fun addTiles(ctx: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) {
            Toast.makeText(ctx, "Pull down Quick Settings, tap the pencil, and drag in the PowerDeck tiles.", Toast.LENGTH_LONG).show()
            return
        }
        val sbm = ctx.getSystemService(StatusBarManager::class.java) ?: return
        val icon = Icon.createWithResource(ctx, R.drawable.ic_power)
        val tiles = listOf(ComponentName(ctx, ServerTileService::class.java) to "Server", ComponentName(ctx, PcTileService::class.java) to "PC")
        fun request(i: Int) {
            if (i >= tiles.size) return
            sbm.requestAddTileService(tiles[i].first, tiles[i].second, icon, ctx.mainExecutor) { request(i + 1) }
        }
        request(0)
    }
}
