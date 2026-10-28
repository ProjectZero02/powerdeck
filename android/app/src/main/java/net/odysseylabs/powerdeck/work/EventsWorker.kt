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
import net.odysseylabs.powerdeck.data.HubRecovery
import net.odysseylabs.powerdeck.data.Power
import net.odysseylabs.powerdeck.data.Tailscale
import net.odysseylabs.powerdeck.data.Updater
import net.odysseylabs.powerdeck.deck
import net.odysseylabs.powerdeck.ui.MainActivity
import net.odysseylabs.powerdeck.ui.QuickActionActivity
import net.odysseylabs.powerdeck.ui.RecoveryText
import net.odysseylabs.powerdeck.ui.recoveryText
import net.odysseylabs.powerdeck.ui.Shortcuts
import kotlinx.coroutines.delay
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
        appUpdate(ctx)
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
                if (seen != null) tell(ctx, d, all, quiet(fresh.filter { notable(it, d, helperNames) }).takeLast(3))
                events.maxOfOrNull { it.at }?.let { deck.store.setSeenEvents(d.id, it.toString()) }
            } else if (before == Power.On && st == DeviceState.Off && confirmedOff { deck.state(d) == DeviceState.Off }) {
                notify(ctx, d, "${d.name} went offline.")
            }
        }
        Shortcuts.refreshAll(ctx)
        return Result.success()
    }

    /**
     * Notifies [events] from [d]'s log. A reset proposed for a machine of this hub comes with Reset and
     * Not now, which work only on an unlocked phone; tapping opens that machine's page.
     */
    private suspend fun tell(ctx: Context, d: Device, all: List<Device>, events: List<Event>) {
        val open = if (events.any { it.result == PROPOSED }) ctx.deck.withHelper(d) { recoveries() }.getOrNull().orEmpty() else emptyList()
        for (e in events) {
            val m = recoveryMachine(e)
            val target = m?.let { name -> all.firstOrNull { it.kvm?.hub == d.id && (it.kvm.machine ?: it.fleetName).equals(name, ignoreCase = true) } }
            val r = if (e.result == PROPOSED) open.firstOrNull { it.proposed && it.machine.equals(m, ignoreCase = true) } else null
            val text = if (r != null && target != null) recoveryText(r, target.name, d.name, System.currentTimeMillis(), d.fleetName) else null
            when {
                text != null -> notifyRecovery(ctx, target!!, r!!, text)
                else -> notify(ctx, target ?: d, describe(e, d), urgent = e.result == NOT_BACK)
            }
        }
    }

    /** Once a day: tell you about a new version of this app (Android installs it only after you tap). */
    private suspend fun appUpdate(ctx: Context) {
        val store = ctx.deck.store
        val (checkedAt, told) = store.appUpdateCheck()
        if (System.currentTimeMillis() - checkedAt < TimeUnit.HOURS.toMillis(24)) return
        val r = runCatching { Updater.available() }.getOrElse { return } // offline: try next round
        store.setAppUpdateCheck(System.currentTimeMillis(), r?.version ?: told)
        if (r == null || r.version == told) return
        if (Build.VERSION.SDK_INT >= 33 && ctx.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) return
        ctx.getSystemService(NotificationManager::class.java)
            .createNotificationChannel(NotificationChannel(UPDATES, "App updates", NotificationManager.IMPORTANCE_LOW))
        val open = PendingIntent.getActivity(ctx, 1, Intent(ctx, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val n = NotificationCompat.Builder(ctx, UPDATES)
            .setSmallIcon(R.drawable.ic_power)
            .setContentTitle("PowerDeck ${r.version} is available")
            .setContentText("Open PowerDeck and tap Update.")
            .setContentIntent(open)
            .setAutoCancel(true)
            .build()
        NotificationManagerCompat.from(ctx).notify(UPDATES.hashCode(), n)
    }

    private fun notable(e: Event, d: Device, helpers: Set<String>): Boolean {
        if (hubWatch(e)) return true
        val mine = d.me != null && (e.who == d.me || e.who.startsWith("${d.me} via "))
        if (recoveryMachine(e) != null) return !(mine && e.result == "confirmed")
        if (mine) return false
        val verb = e.action.substringBefore(' ').lowercase()
        val target = e.action.substringAfter(' ', "").lowercase()
        // A hub relaying to another device: that device's own log reports it.
        if ((verb == "shutdown" || verb == "restart") && target in helpers) return false
        return verb in setOf("shutdown", "restart", "wake", "cancel", "paired") || e.action.startsWith("created key")
    }

    private fun describe(e: Event, d: Device): String {
        if (hubWatch(e)) return e.action + "." // already a sentence: "Workstation looks frozen: …"
        recoveryMachine(e)?.let { m ->
            val what = if (e.action.contains("force-off") || e.action.contains("force off")) "force $m off" else "reset $m"
            return when {
                e.result == PROPOSED -> "${e.who} asks to $what."
                e.result == "confirmed" && e.who == AUTO -> "${d.name} is going to $what on its own: it stayed frozen."
                e.result == "confirmed" -> "${e.who} confirmed: ${d.name} is going to $what."
                e.result.startsWith("failed") -> "${d.name} couldn't $what: ${e.result.substringAfter(": ")}"
                e.result == NOT_BACK -> e.action + ". Force it off and on, or check it in person."
                else -> e.action + "." // "Workstation answers again after the reset", "Workstation is off"
            }
        }
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
        /** A hub (2.3+) saw a machine with a KVM freeze, or answer again after that. */
        internal fun hubWatch(e: Event) =
            e.result == "detected" && e.action.contains(" looks frozen") || e.result == "recovered" && e.action.endsWith(" answers again")

        private const val PROPOSED = "proposed"
        private const val NOT_BACK = "not_back"
        private const val AUTO = "PowerDeck (automatic)"
        private val proposal = Regex("""^(?:propose|confirm) (?:reset|force-off) ([^\s:]+)""")
        private val ran = Regex("""^(?:reset|force-off) ([^\s:]+)$""")

        /**
         * The machine a hub's (2.4+) recovery line is about: proposed, confirmed, back, not back, off
         * or failed; null for any other line (see docs/protocol.md, Recoveries).
         */
        internal fun recoveryMachine(e: Event): String? = when (e.result) {
            PROPOSED, "confirmed" -> proposal.find(e.action)?.groupValues?.get(1)
            "back" -> e.action.substringBefore(" answers again after the ", "").ifEmpty { null }
            NOT_BACK -> e.action.substringBefore(" didn't answer within ", "").ifEmpty { null }
            "off" -> e.action.removeSuffix(" is off").takeIf { it != e.action }
            else -> if (e.result.startsWith("failed")) ran.find(e.action)?.groupValues?.get(1) else null
        }

        /**
         * Drops what a recovery line already says: "looks frozen" when a reset is proposed for that
         * machine, and "answers again" when it's back after one.
         */
        /**
         * Whether a device that just missed one check is really off: [probe] answers off [tries] more times,
         * [pauseMs] apart. A phone waking from Doze often can't reach a peer for a few seconds while
         * Tailscale reconnects, which on its own said "went offline" about a machine that never was.
         */
        internal suspend fun confirmedOff(tries: Int = 2, pauseMs: Long = 20_000, probe: suspend () -> Boolean): Boolean {
            repeat(tries) {
                delay(pauseMs)
                if (!probe()) return false
            }
            return true
        }

        internal fun quiet(events: List<Event>): List<Event> {
            fun machines(result: String) = events.filter { it.result == result }.mapNotNull(::recoveryMachine).map(String::lowercase).toSet()
            val proposed = machines(PROPOSED)
            val back = machines("back")
            return events.filterNot { e ->
                e.result == "detected" && e.action.substringBefore(" looks frozen").lowercase() in proposed ||
                    e.result == "recovered" && e.action.removeSuffix(" answers again").lowercase() in back
            }
        }

        private const val NAME = "events"
        private const val CHANNEL = "activity"
        private const val URGENT = "recovery"
        private const val UPDATES = "updates"

        fun schedule(ctx: Context) {
            val req = PeriodicWorkRequestBuilder<EventsWorker>(15, TimeUnit.MINUTES)
                .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
                .build()
            WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(NAME, ExistingPeriodicWorkPolicy.KEEP, req)
        }

        fun cancel(ctx: Context) = WorkManager.getInstance(ctx).cancelUniqueWork(NAME)

        private fun allowed(ctx: Context) =
            Build.VERSION.SDK_INT < 33 || ctx.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED

        /** [urgent]: a reset or a machine that needs you (its own channel, which alerts). */
        private fun channel(ctx: Context, urgent: Boolean): String {
            val nm = ctx.getSystemService(NotificationManager::class.java)
            if (urgent) nm.createNotificationChannel(NotificationChannel(URGENT, "Frozen machines", NotificationManager.IMPORTANCE_HIGH))
            else nm.createNotificationChannel(NotificationChannel(CHANNEL, "Device activity", NotificationManager.IMPORTANCE_DEFAULT))
            return if (urgent) URGENT else CHANNEL
        }

        private fun openDevice(ctx: Context, d: Device): PendingIntent = PendingIntent.getActivity(
            ctx, d.id.hashCode(),
            Intent(ctx, MainActivity::class.java).putExtra(MainActivity.EXTRA_DEVICE, d.id).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )

        private fun notify(ctx: Context, d: Device, text: String, urgent: Boolean = false) {
            if (!allowed(ctx)) return
            val n = NotificationCompat.Builder(ctx, channel(ctx, urgent))
                .setSmallIcon(R.drawable.ic_power)
                .setContentTitle(d.name)
                .setContentText(text)
                .setStyle(NotificationCompat.BigTextStyle().bigText(text))
                .setContentIntent(openDevice(ctx, d))
                .setAutoCancel(true)
                .build()
            NotificationManagerCompat.from(ctx).notify(("${d.id}$text").hashCode(), n)
        }

        /** A proposed reset or force off of [d], with Reset and Not now (unlocked phone only). */
        private fun notifyRecovery(ctx: Context, d: Device, r: HubRecovery, t: RecoveryText) {
            if (!allowed(ctx)) return
            val id = "recovery ${r.id}".hashCode()
            fun answer(yes: Boolean, label: String) = NotificationCompat.Action.Builder(
                0, label,
                PendingIntent.getActivity(ctx, id + if (yes) 1 else 2, QuickActionActivity.answer(ctx, d.id, r.id, yes, id),
                    PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT),
            ).setAuthenticationRequired(true).build()
            val n = NotificationCompat.Builder(ctx, channel(ctx, urgent = true))
                .setSmallIcon(R.drawable.ic_power)
                .setContentTitle(t.title)
                .setContentText(t.detail?.substringBefore('\n'))
                .setStyle(NotificationCompat.BigTextStyle().bigText(t.detail))
                .setContentIntent(openDevice(ctx, d))
                .addAction(answer(true, if (r.forceOff) "Force off" else "Reset"))
                .addAction(answer(false, "Not now"))
                .setAutoCancel(true)
                .build()
            NotificationManagerCompat.from(ctx).notify(id, n)
        }
    }
}
