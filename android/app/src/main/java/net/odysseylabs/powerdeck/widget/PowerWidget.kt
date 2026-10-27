package net.odysseylabs.powerdeck.widget

import android.content.Context
import android.content.Intent
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.glance.ColorFilter
import androidx.glance.GlanceId
import androidx.glance.GlanceModifier
import androidx.glance.GlanceTheme
import androidx.glance.Image
import androidx.glance.ImageProvider
import androidx.glance.action.clickable
import androidx.glance.appwidget.GlanceAppWidget
import androidx.glance.appwidget.GlanceAppWidgetReceiver
import androidx.glance.appwidget.action.actionStartActivity
import androidx.glance.appwidget.cornerRadius
import androidx.glance.appwidget.lazy.LazyColumn
import androidx.glance.appwidget.lazy.items
import androidx.glance.appwidget.provideContent
import androidx.glance.background
import androidx.glance.layout.Alignment
import androidx.glance.layout.Box
import androidx.glance.layout.Column
import androidx.glance.layout.Row
import androidx.glance.layout.Spacer
import androidx.glance.layout.fillMaxSize
import androidx.glance.layout.fillMaxWidth
import androidx.glance.layout.height
import androidx.glance.layout.padding
import androidx.glance.layout.size
import androidx.glance.layout.width
import androidx.glance.text.FontWeight
import androidx.glance.text.Text
import androidx.glance.text.TextStyle
import kotlinx.coroutines.flow.first
import net.odysseylabs.powerdeck.R
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.Power
import net.odysseylabs.powerdeck.data.Snapshot
import net.odysseylabs.powerdeck.deck
import net.odysseylabs.powerdeck.ui.MainActivity
import net.odysseylabs.powerdeck.ui.QuickActionActivity

/** Home-screen widget: every device with its last known state; tapping one opens the quick action. */
class PowerWidget : GlanceAppWidget() {
    override suspend fun provideGlance(context: Context, id: GlanceId) {
        val store = context.deck.store
        val devices0 = store.devices.first()
        val snaps0 = store.snapshots.first()
        provideContent {
            val devices by store.devices.collectAsState(devices0)
            val snaps by store.snapshots.collectAsState(snaps0)
            GlanceTheme { Content(context, devices, snaps) }
        }
    }
}

class PowerWidgetReceiver : GlanceAppWidgetReceiver() {
    override val glanceAppWidget: GlanceAppWidget = PowerWidget()
}

private fun ago(at: Long): String {
    if (at == 0L) return ""
    val min = (System.currentTimeMillis() - at) / 60_000
    return when {
        min < 2 -> " · now"
        min < 90 -> " · $min min ago"
        min < 48 * 60 -> " · ${min / 60} h ago"
        else -> " · ${min / 1440} d ago"
    }
}

@Composable
private fun Content(ctx: Context, devices: List<Device>, snaps: Map<String, Snapshot>) {
    Column(GlanceModifier.fillMaxSize().cornerRadius(24.dp).background(GlanceTheme.colors.widgetBackground).padding(8.dp)) {
        if (devices.isEmpty()) {
            Box(
                GlanceModifier.fillMaxSize().clickable(actionStartActivity(Intent(ctx, MainActivity::class.java))),
                contentAlignment = Alignment.Center,
            ) { Text("Add a device in PowerDeck", style = TextStyle(color = GlanceTheme.colors.onSurface)) }
            return@Column
        }
        LazyColumn {
            items(devices, itemId = { it.id.hashCode().toLong() }) { d ->
                Column {
                    DeviceRow(ctx, d, snaps[d.id] ?: Snapshot(Power.Unknown, 0))
                    Spacer(GlanceModifier.height(6.dp))
                }
            }
        }
    }
}

@Composable
private fun DeviceRow(ctx: Context, d: Device, snap: Snapshot) {
    val on = snap.power == Power.On
    Row(
        GlanceModifier.fillMaxWidth().height(60.dp).cornerRadius(18.dp)
            .background(if (on) GlanceTheme.colors.primaryContainer else GlanceTheme.colors.secondaryContainer)
            .padding(horizontal = 12.dp)
            .clickable(actionStartActivity(QuickActionActivity.intent(ctx, d.id))),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(
            GlanceModifier.size(38.dp).cornerRadius(19.dp).background(if (on) GlanceTheme.colors.primary else GlanceTheme.colors.outline),
            contentAlignment = Alignment.Center,
        ) {
            Image(ImageProvider(R.drawable.ic_power), contentDescription = null, modifier = GlanceModifier.size(20.dp), colorFilter = ColorFilter.tint(GlanceTheme.colors.onPrimary))
        }
        Spacer(GlanceModifier.width(12.dp))
        Column {
            Text(d.name, maxLines = 1, style = TextStyle(color = GlanceTheme.colors.onSurface, fontSize = 15.sp, fontWeight = FontWeight.Medium))
            Text(
                when (snap.power) {
                    Power.On -> "On"
                    Power.Off -> "Off"
                    Power.Unknown -> "Tap to check"
                } + ago(snap.at),
                style = TextStyle(color = GlanceTheme.colors.onSurfaceVariant, fontSize = 13.sp),
            )
        }
    }
}
