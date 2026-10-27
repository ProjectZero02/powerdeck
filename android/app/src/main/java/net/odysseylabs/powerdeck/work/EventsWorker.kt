package net.odysseylabs.powerdeck.work

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import net.odysseylabs.powerdeck.R
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.DeviceState
import net.odysseylabs.powerdeck.data.Event
import net.odysseylabs.powerdeck.data.Power
import net.odysseylabs.powerdeck.data.Tailscale
import net.odysseylabs.powerdeck.deck
import net.odysseylabs.powerdeck.ui.MainActivity
import net.odysseylabs.powerdeck.ui.Shortcuts
import java.time.Instant
import java.util.concurrent.TimeUnit

/**
 * About every 15 minutes (as often as Android allows), while Tailscale is up: reads each device's event
 * log and notifies about what this phone didn't do itself (a schedule, an agent, another person), and
 * about a device that went off with nothing in its log to explain it.
 */
class EventsWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result {
        val ctx = applicationContext
        val deck = ctx.deck
        if (!deck.store.currentSettings().notifications) return Result.success()
        // Never switch the VPN on from the background; just skip this round.
        if (!Tailscale.vpnActive(ctx)) return Result.success()
        val all = deck.store.all()
        val helperNames = all.filter { it.hasHelper }.map { it.name.lowercase() }.toSet()
        for (d in all) {
            val before = deck.store.snapshot(d.id).power
            val st = deck.state(d)
            if (!d.hasHelper) continue
            val events = deck.withHelper(d) { events() }.getOrNull()
            if (events != null) {
                val seen = deck.store.seenEvents(d.id)?.let { runCatching { Instant.parse(it) }.getOrNull() }
                val fresh = events.filter { seen == null || it.at.isAfter(seen) }.sortedBy { it.at }
                // On the first run only remember where the log is; don't replay history.
                if (seen != null) fresh.filter { notable(it, d, helperNames) }.takeLast(3).forEach { notify(ctx, d, describe(it, d)) }
                events.maxOfOrNull { it.at }?.let { deck.store.setSeenEvents(d.id, it.toString()) }
            } else if (before == Power.On && st == DeviceState.Off) {
                notify(ctx, d, "${d.name} went offline.")
            }
        }
        Shortcuts.refreshAll(ctx)
        return Result.success()
    }

    private fun notable(e: Event, d: Device, helpers: Set<String>): Boolean {
        if (d.me != null && (e.who == d.me || e.who.startsWith("${d.me} via "))) return false
        val verb = e.action.substringBefore(' ').lowercase()
        val target = e.action.substringAfter(' ', "").lowercase()
        // A hub relaying to another device: that device's own log reports it.
        if ((verb == "shutdown" || verb == "restart") && target in helpers) return false
        return verb in setOf("shutdown", "restart", "wake", "cancel", "paired") || e.action.startsWith("created key")
    }

    private fun describe(e: Event, d: Device): String {
        val verb = e.action.substringBefore(' ')
        val target = e.action.substringAfter(' ', "").ifBlank { d.name }
        val what = when (verb) {
            "shutdown" -> "shut down $target"
            "restart" -> "restarted $target"
            "wake" -> if (target.contains(':')) "sent a wake signal from ${d.name}" else "woke $target"
            "cancel" -> "cancelled a shutdown on ${d.name}"
            "paired" -> "was added to ${d.name}"
            else -> "${e.action} on ${d.name}"
        }
        val failed = e.result.startsWith("refused") || e.result.startsWith("skipped")
        val verbPhrase = if (failed) "tried to " + what.replaceFirst("restarted", "restart").replaceFirst("woke", "wake") else what
        return "${e.who} $verbPhrase${if (e.result.isNotBlank() && e.result != "ok" && e.result != "scheduled") " (${e.result})" else ""}."
    }

    companion object {
        private const val NAME = "events"
        private const val CHANNEL = "activity"

        fun schedule(ctx: Context) {
            val req = PeriodicWorkRequestBuilder<EventsWorker>(15, TimeUnit.MINUTES)
                .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
                .build()
            WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(NAME, ExistingPeriodicWorkPolicy.KEEP, req)
        }

        fun cancel(ctx: Context) = WorkManager.getInstance(ctx).cancelUniqueWork(NAME)

        private fun notify(ctx: Context, d: Device, text: String) {
            if (Build.VERSION.SDK_INT >= 33 && ctx.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) return
            val nm = ctx.getSystemService(NotificationManager::class.java)
            nm.createNotificationChannel(NotificationChannel(CHANNEL, "Device activity", NotificationManager.IMPORTANCE_DEFAULT))
            val open = PendingIntent.getActivity(
                ctx, d.id.hashCode(),
                Intent(ctx, MainActivity::class.java).putExtra(MainActivity.EXTRA_DEVICE, d.id).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP),
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
            val n = NotificationCompat.Builder(ctx, CHANNEL)
                .setSmallIcon(R.drawable.ic_power)
                .setContentTitle(d.name)
                .setContentText(text)
                .setContentIntent(open)
                .setAutoCancel(true)
                .build()
            NotificationManagerCompat.from(ctx).notify(("${d.id}$text").hashCode(), n)
        }
    }
}
