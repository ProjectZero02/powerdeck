package net.odysseylabs.powerdeck.data

import org.json.JSONArray
import org.json.JSONObject
import java.time.Instant

/**
 * A device the phone controls. Usually it runs the PowerDeck helper (`host` set); it can also have a
 * KVM wired to its power button (`kvm` set). See docs/protocol.md.
 */
data class Device(
    val id: String,
    val name: String,
    val host: String?,          // tailnet address of its helper
    val port: Int = 47321,
    val pin: String?,           // helper certificate pin
    val os: String? = null,
    val mac: String? = null,
    val lan: List<String> = emptyList(),
    val hasKey: Boolean = false, // helper on another account: we hold a key for it
    val kvm: KvmLink? = null,
    val guests: Boolean = false, // a Proxmox host (warn about VMs)
    val me: String? = null,      // what this phone is called in the helper's log
    val helperName: String? = null, // the helper's own name, which other helpers know it by
) {
    val hasHelper get() = host != null && pin != null
    /** The name to use when asking other helpers about this device. */
    val fleetName get() = helperName ?: name
    val kind: Kind get() = when {
        guests -> Kind.Server
        os == "windows" -> Kind.Pc
        os == "linux" -> Kind.Server
        else -> Kind.Other
    }

    enum class Kind { Pc, Server, Other }

    fun toJson(): JSONObject = JSONObject()
        .put("id", id).put("name", name).put("host", host).put("port", port).put("pin", pin).put("os", os)
        .put("mac", mac).put("lan", JSONArray(lan)).put("hasKey", hasKey).put("guests", guests)
        .put("kvm", kvm?.toJson()).put("me", me).put("helperName", helperName)

    companion object {
        fun fromJson(o: JSONObject) = Device(
            id = o.getString("id"),
            name = o.optString("name", "Device"),
            host = o.optStringOrNull("host"),
            port = o.optInt("port", 47321),
            pin = o.optStringOrNull("pin"),
            os = o.optStringOrNull("os"),
            mac = o.optStringOrNull("mac"),
            lan = o.optJSONArray("lan")?.strings() ?: emptyList(),
            hasKey = o.optBoolean("hasKey"),
            guests = o.optBoolean("guests"),
            kvm = o.optJSONObject("kvm")?.let(KvmLink::fromJson),
            me = o.optStringOrNull("me"),
            helperName = o.optStringOrNull("helperName"),
        )
    }
}

/** A NanoKVM wired to a device's power button and LED. */
data class KvmLink(
    val kind: String, val address: String, val name: String, val user: String,
    // The power light has been seen on (with the helper agreeing, if there is one). An unwired LED
    // reads "off" forever, so until then a press could switch a running computer off: never press.
    val ledSeen: Boolean = false,
    /** The helper (a device id) that drives this KVM; null: this phone does, directly. */
    val hub: String? = null,
    /** The machine's name on the hub. */
    val machine: String? = null,
    /** What the hub learned about the wiring. */
    val wiring: KvmWiring? = null,
    /** Why the hub couldn't take this KVM over (shown; tried again later). */
    val hubError: String? = null,
) {
    fun toJson(): JSONObject = JSONObject().put("kind", kind).put("address", address).put("name", name).put("user", user).put("ledSeen", ledSeen)
        .put("hub", hub).put("machine", machine).put("wiring", wiring?.toJson()).put("hubError", hubError)

    companion object {
        fun fromJson(o: JSONObject) = KvmLink(o.optString("kind", "nanokvm"), o.getString("address"), o.optString("name"), o.optString("user", "admin"), o.optBoolean("ledSeen"),
            o.optStringOrNull("hub"), o.optStringOrNull("machine"), o.optJSONObject("wiring")?.let(KvmWiring::from), o.optStringOrNull("hubError"))
    }
}

/** Which of the KVM's cables reach the machine, as the hub learned it: "wired", "not_wired" or "unknown". */
data class KvmWiring(val led: String, val power: String, val reset: String) {
    val ledWired get() = led == WIRED

    fun toJson(): JSONObject = JSONObject().put("led", led).put("power", power).put("reset", reset)

    companion object {
        const val WIRED = "wired"
        const val NOT_WIRED = "not_wired"
        fun from(o: JSONObject) = KvmWiring(o.optString("led", "unknown"), o.optString("power", "unknown"), o.optString("reset", "unknown"))
    }
}

/**
 * A machine as a hub sees it (GET /v2/machines). [led] null: not read. From 2.3 the hub's watch adds
 * [state] "hung", a [reason] for "unknown", [since] and its last look ([evidence]).
 */
data class HubMachine(
    val name: String, val state: String, val led: Boolean?, val ledError: String?, val kvm: String?, val wiring: KvmWiring?, val actions: List<String>,
    val reason: String? = null, val asleep: Boolean = false, val since: Instant? = null, val evidence: HubEvidence? = null,
) {
    val hung get() = state == HUNG

    companion object {
        const val HUNG = "hung"
        fun from(o: JSONObject): HubMachine {
            val k = o.optJSONObject("kvm")
            return HubMachine(o.optString("name"), o.optString("state", "unknown"), if (o.has("led") && !o.isNull("led")) o.optBoolean("led") else null,
                o.optStringOrNull("ledError"), k?.optStringOrNull("name"), k?.optJSONObject("wiring")?.let(KvmWiring::from),
                o.optJSONArray("actions")?.strings() ?: emptyList(),
                o.optStringOrNull("reason"), o.optBoolean("asleep"), o.instant("since"), o.optJSONObject("evidence")?.let(HubEvidence::from))
        }
    }
}

/**
 * The hub's last look at a machine: its helper ("answers", "silent"), the helper's port ("refuses",
 * "accepts", "no_reply"), a ping ("replies", "no_reply", "unavailable") and the power light ("on",
 * "off", "blinking", "unreadable"). Null: not looked at (no helper).
 */
data class HubEvidence(val at: Instant?, val helper: String?, val port: String?, val ping: String?, val led: String?, val ledTrusted: Boolean) {
    companion object {
        fun from(o: JSONObject) = HubEvidence(o.instant("at"), o.optStringOrNull("helper"), o.optStringOrNull("port"), o.optStringOrNull("ping"),
            o.optStringOrNull("led"), o.optBoolean("ledTrusted"))
    }
}

private fun JSONObject.instant(k: String) = optStringOrNull(k)?.let { runCatching { Instant.parse(it) }.getOrNull() }

/** What a hub did for an action: its message, and each step with the power light read. */
data class HubResult(val ok: Boolean, val code: String?, val message: String, val steps: List<String>) {
    companion object {
        fun from(o: JSONObject) = HubResult(
            o.optBoolean("ok", !o.has("error")), o.optStringOrNull("code") ?: o.optStringOrNull("error"), o.optString("message"),
            o.optJSONArray("steps")?.let { a ->
                (0 until a.length()).map { a.getJSONObject(it) }.map { s ->
                    s.optString("do") + (if (s.has("led")) if (s.optBoolean("led")) ": on" else ": off" else "") +
                        (s.optStringOrNull("error")?.let { ": $it" } ?: "")
                }
            } ?: emptyList(),
        )
    }
}

internal fun JSONObject.optStringOrNull(k: String): String? = if (isNull(k)) null else optString(k).takeIf { it.isNotEmpty() }

internal fun JSONArray.strings(): List<String> = (0 until length()).mapNotNull { optString(it).takeIf(String::isNotEmpty) }

// ---- What helpers report (protocol v2) ----

data class Pending(val action: String, val at: Instant, val by: String)

data class Presence(val active: Boolean, val user: String?, val idleSeconds: Int, val fullscreen: Boolean, val locked: Boolean, val sessions: Int) {
    /** "Alex is using it (active 40 s ago, full-screen app)". */
    fun describe(device: String): String {
        val who = user ?: "Someone"
        val bits = buildList {
            add(if (idleSeconds < 60) "active just now" else "active ${idleSeconds / 60} min ago")
            if (fullscreen) add("full-screen app or game")
        }
        return "$who is using $device (${bits.joinToString(", ")})."
    }

    companion object {
        fun from(o: JSONObject?) = o?.let {
            Presence(it.optBoolean("active"), it.optStringOrNull("user"), it.optInt("idleSeconds"), it.optBoolean("fullscreen"),
                it.optBoolean("locked"), it.optInt("sessions"))
        }
    }
}

data class Check(val id: String, val level: String, val title: String, val detail: String?, val fixable: Boolean) {
    val ok get() = level == "ok"
    val problem get() = level == "warn" || level == "error"

    companion object {
        fun from(o: JSONObject) = Check(o.getString("id"), o.optString("level"), o.optString("title"), o.optStringOrNull("detail"), o.optBoolean("fixable"))
    }
}

data class HelperStatus(
    val name: String,
    val os: String,
    val version: String,
    val uptimeSeconds: Long,
    val pending: Pending?,
    val presence: Presence?,
    val mac: String?,
    val lan: List<String>,
    val wired: Boolean,
    val guestsRunning: Int?,
    val guestNames: List<String>,
    val updatesPending: Boolean,
    val checks: List<Check>,
    val youName: String?,
    /** The helper's clock, which its schedules run on. */
    val utcOffsetMinutes: Int? = null,
    /** The helper's own updates (owners only). */
    val update: HelperUpdate? = null,
    /** Whether AI agents may use this device, and the agent keys this helper holds (owners only). */
    val agents: Agents? = null,
    val caps: List<String> = emptyList(),
    /** The helper still trusts every personal device on its Tailscale account (upgraded, not yet claimed). */
    val ownersLegacy: Boolean = false,
    /** The helpers this one holds a peer key for (owners only; null from helpers before 2.1). */
    val peerKeys: List<String>? = null,
) {
    companion object {
        fun pendingFrom(p: JSONObject?) = p?.let { Pending(it.optString("action"), Instant.parse(it.optString("at")), it.optString("by")) }

        fun from(o: JSONObject): HelperStatus {
            val net = o.optJSONObject("network")
            val g = o.optJSONObject("guests")
            return HelperStatus(
                name = o.optString("name"),
                os = o.optString("os"),
                version = o.optString("version"),
                uptimeSeconds = o.optLong("uptimeSeconds"),
                pending = pendingFrom(o.optJSONObject("pending")),
                presence = Presence.from(o.optJSONObject("presence")),
                mac = net?.optStringOrNull("mac"),
                lan = net?.optJSONArray("lan")?.strings() ?: emptyList(),
                wired = net?.optBoolean("wired") ?: false,
                guestsRunning = g?.optInt("running"),
                guestNames = g?.optJSONArray("names")?.strings() ?: emptyList(),
                updatesPending = o.optBoolean("updatesPending"),
                checks = o.optJSONArray("checks")?.let { a -> (0 until a.length()).map { Check.from(a.getJSONObject(it)) } } ?: emptyList(),
                youName = o.optJSONObject("you")?.optStringOrNull("name"),
                utcOffsetMinutes = if (o.has("utcOffsetMinutes")) o.optInt("utcOffsetMinutes") else null,
                update = o.optJSONObject("update")?.let(HelperUpdate::from),
                agents = o.optJSONObject("agents")?.let(Agents::from),
                caps = o.optJSONArray("caps")?.strings() ?: emptyList(),
                ownersLegacy = o.optJSONObject("owners")?.optBoolean("legacy") ?: false,
                peerKeys = o.optJSONArray("peerKeys")?.strings(),
            )
        }
    }
}

/** [keys]: agent keys on that helper, each limited to [AgentKey.devices] (empty: all of them). */
data class Agents(val allowed: Boolean, val keys: List<AgentKey>) {
    companion object {
        fun from(o: JSONObject) = Agents(
            o.optBoolean("allowed", true),
            o.optJSONArray("keys")?.let { a -> (0 until a.length()).map { a.getJSONObject(it) }.map { AgentKey(it.optString("name"), it.optJSONArray("devices")?.strings() ?: emptyList()) } } ?: emptyList(),
        )
    }
}

data class AgentKey(val name: String, val devices: List<String>)

/**
 * A helper's self-update state: it checks every 6 hours and, when [auto] is on, installs a release on
 * its own at [autoAt] (3 days after it was published, or when a pause ends).
 */
data class HelperUpdate(
    val available: Boolean, val latest: String?, val installing: Boolean, val auto: Boolean, val error: String? = null,
    val autoAt: Instant? = null, val pausedUntil: Instant? = null,
) {
    companion object {
        fun from(o: JSONObject) = HelperUpdate(o.optBoolean("available"), o.optStringOrNull("latest")?.ifEmpty { null },
            o.optBoolean("installing"), o.optBoolean("auto", true), o.optStringOrNull("error")?.ifEmpty { null },
            o.instant("autoAt"), o.instant("pausedUntil"))
    }
}

/** Something a helper found on the tailnet. */
data class Peer(val kind: String, val name: String, val dnsName: String?, val address: String, val port: Int, val os: String?, val pin: String?) {
    val isHelper get() = kind == "helper"
    val isKvm get() = kind == "nanokvm" // PiKVM is detected but its API isn't supported yet
    /** How the app reaches a KVM: its Tailscale name, since plain HTTP is only allowed to those. */
    val kvmHost get() = dnsName?.trimEnd('.')?.ifEmpty { null } ?: address

    companion object {
        fun from(o: JSONObject) = Peer(o.optString("kind"), o.optString("name"), o.optStringOrNull("dnsName"), o.optString("address"),
            o.optInt("port", 47321), o.optStringOrNull("os"), o.optStringOrNull("pin"))
    }
}

data class Event(val at: Instant, val who: String, val action: String, val result: String) {
    companion object {
        fun from(o: JSONObject) = Event(Instant.parse(o.optString("at")), o.optString("who"), o.optString("action"), o.optString("result"))
    }
}

data class Schedule(val id: String, val action: String, val target: String?, val time: String, val days: List<String>, val skipIfInUse: Boolean, val enabled: Boolean) {
    companion object {
        fun from(o: JSONObject) = Schedule(o.optString("id"), o.optString("action"), o.optStringOrNull("target"), o.optString("time"),
            o.optJSONArray("days")?.strings() ?: emptyList(), o.optBoolean("skipIfInUse", true), o.optBoolean("enabled", true))
    }
}

/** Live state of a device, as shown on its card. */
sealed interface DeviceState {
    data object Checking : DeviceState
    /** [status] null when only a KVM LED says so; [hub] is then what the hub that drives the KVM makes of it. */
    data class On(val status: HelperStatus?, val hub: HubMachine? = null) : DeviceState
    data object Off : DeviceState
    data class Problem(val message: String) : DeviceState
}

/** Last known power states, shown by the widget and tiles without a network round trip. */
enum class Power { On, Off, Unknown }

/** Result of a button press, phrased for the user. `ask` means: confirm, then repeat with force. */
data class Outcome(val ok: Boolean, val message: String, val ask: Ask? = null)

/** A question to put to the user before going ahead anyway. */
data class Ask(
    val title: String, val text: String, val confirm: String, val kvmPress: Boolean = false,
    /** Run this hub action again with force (reset or force-off of a machine whose helper still answers). */
    val hubAction: String? = null,
)
