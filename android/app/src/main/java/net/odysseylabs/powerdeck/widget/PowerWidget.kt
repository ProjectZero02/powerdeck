package net.odysseylabs.powerdeck.widget

import android.content.Context
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
import androidx.glance.action.Action
import androidx.glance.action.clickable
import androidx.glance.appwidget.GlanceAppWidget
import androidx.glance.appwidget.GlanceAppWidgetReceiver
import androidx.glance.appwidget.action.actionStartActivity
import androidx.glance.appwidget.cornerRadius
import androidx.glance.appwidget.provideContent
import androidx.glance.background
import androidx.glance.layout.Alignment
import androidx.glance.layout.Box
import androidx.glance.layout.Column
import androidx.glance.layout.ColumnScope
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
import androidx.glance.unit.ColorProvider
import kotlinx.coroutines.flow.first
import net.odysseylabs.powerdeck.R
import net.odysseylabs.powerdeck.data.Power
import net.odysseylabs.powerdeck.data.Snapshot
import net.odysseylabs.powerdeck.deck
import net.odysseylabs.powerdeck.ui.QuickActionActivity
import net.odysseylabs.powerdeck.ui.Target

/** Home-screen widget: last known state of both devices; tapping one opens the quick action. */
class PowerWidget : GlanceAppWidget() {
    override suspend fun provideGlance(context: Context, id: GlanceId) {
        val store = context.deck.store
        val initial = store.snapshot.first()
        val pcName = store.current().pc?.name ?: "PC"
        provideContent {
            val snap by store.snapshot.collectAsState(initial)
            GlanceTheme { Content(context, snap, pcName) }
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
        min < 90 -> " · ${min} min ago"
        else -> " · ${min / 60} h ago"
    }
}

@Composable
private fun Content(ctx: Context, snap: Snapshot, pcName: String) {
    Column(
        GlanceModifier.fillMaxSize().cornerRadius(24.dp).background(GlanceTheme.colors.widgetBackground).padding(10.dp),
    ) {
        DeviceRow(pcName, snap.pc, snap.at, actionStartActivity(QuickActionActivity.intent(ctx, Target.Pc)))
        Spacer(GlanceModifier.height(8.dp))
        DeviceRow("Server", snap.server, snap.at, actionStartActivity(QuickActionActivity.intent(ctx, Target.Server)))
    }
}

@Composable
private fun ColumnScope.DeviceRow(name: String, power: Power, at: Long, onClick: Action) {
    val on = power == Power.On
    Row(
        GlanceModifier.fillMaxWidth().defaultWeight().cornerRadius(18.dp)
            .background(if (on) GlanceTheme.colors.primaryContainer else GlanceTheme.colors.secondaryContainer)
            .padding(horizontal = 14.dp, vertical = 8.dp)
            .clickable(onClick),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(
            GlanceModifier.size(40.dp).cornerRadius(20.dp).background(if (on) GlanceTheme.colors.primary else GlanceTheme.colors.outline),
            contentAlignment = Alignment.Center,
        ) {
            Image(ImageProvider(R.drawable.ic_power), contentDescription = null, modifier = GlanceModifier.size(22.dp),
                colorFilter = ColorFilter.tint(GlanceTheme.colors.onPrimary))
        }
        Spacer(GlanceModifier.width(12.dp))
        Column {
            Text(name, maxLines = 1, style = TextStyle(color = GlanceTheme.colors.onSurface, fontSize = 15.sp, fontWeight = FontWeight.Medium))
            Text(
                when (power) {
                    Power.On -> "On"
                    Power.Off -> "Off"
                    Power.Unknown -> "Tap to check"
                } + ago(at),
                style = TextStyle(color = GlanceTheme.colors.onSurfaceVariant, fontSize = 13.sp),
            )
        }
    }
}
