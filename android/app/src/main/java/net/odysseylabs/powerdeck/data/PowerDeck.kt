package net.odysseylabs.powerdeck.data

import android.content.Context
import android.os.Build
import android.provider.Settings
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import okhttp3.OkHttpClient
import java.time.Duration
import java.time.Instant
import java.util.concurrent.TimeUnit

/** Result of adding a device. */
sealed interface AddResult {
    data class Added(val device: Device) : AddResult
    /** The helper belongs to another Tailscale account: ask for the code it shows. */
    data class NeedsCode(val name: String, val retry: suspend (code: String) -> AddResult) : AddResult
    data class Failed(val message: String) : AddResult
}

/** How long before linking two helpers is tried again (it failed, or the key was later refused). */
private const val LINK_RETRY_MS = 5 * 60_000L

/** All device logic. Screens, the widget, tiles and the background worker call into this. */
class PowerDeck(
    private val ctx: Context,
    val store: DeviceStore,
    /** Tests pass one that resolves Tailscale names to local fakes. */
    private val http: OkHttpClient = OkHttpClient.Builder()
        .connectTimeout(4, TimeUnit.SECONDS)
        .readTimeout(15, TimeUnit.SECONDS)
        .retryOnConnectionFailure(false)
        .build(),
    /** Tests stand in for the Tailscale VPN. */
    private val vpn: (suspend () -> Boolean)? = null,
) {

    suspend fun tailnet(): Boolean = vpn?.invoke() ?: Tailscale.ensureConnected(ctx, store.currentSettings().autoTailscale)

    fun phoneName(): String =
        Settings.Global.getString(ctx.contentResolver, Settings.Global.DEVICE_NAME)?.takeIf { it.isNotBlank() }
            ?: "${Build.MANUFACTURER} ${Build.MODEL}"

    suspend fun client(d: Device): HelperClient? {
        val host = d.host ?: return null
        val key = if (d.hasKey) store.helperKey(d.id) ?: return null else null
        return HelperClient(http, host, d.port, d.pin, key)
    }

    /** Runs [block] against the device's helper; errors become a user-facing message. */
    suspend fun <T> withHelper(d: Device, block: suspend HelperClient.() -> T): Result<T> {
        val c = client(d) ?: return Result.failure(IllegalStateException("${d.name} has no PowerDeck helper."))
        return runCatching { c.block() }.recoverCatching { e ->
            throw if (e is HelperException.Unreachable) IllegalStateException("${d.name} isn't answering.") else e
        }
    }

    // ---- NanoKVM sessions ---------------------------------------------------------------------

    private val kvmLock = Mutex()
    // One power action per device at a time (the app, a widget dialog and a tile can overlap): two
    // turn-ons could both read the light as off and press twice, which turns the computer off again.
    private val acting = java.util.concurrent.ConcurrentHashMap<String, Mutex>()
    private val kvmCookies = mutableMapOf<String, Pair<String, Long>>()

    private suspend fun kvmClient(d: Device): NanoKvmClient? {
        val k = d.kvm ?: return null
        val pw = store.kvmPassword(d.id) ?: return null
        return NanoKvmClient(http, k.address, k.user, pw)
    }

    /** Calls the device's KVM, reusing a login for 10 minutes and logging in again once on 401. */
    private suspend fun <T> withKvm(d: Device, block: suspend (NanoKvmClient, String) -> T): T? {
        val kvm = kvmClient(d) ?: return null
        val address = d.kvm!!.address
        val cached = kvmLock.withLock { kvmCookies[address]?.takeIf { System.currentTimeMillis() - it.second < 10 * 60_000 }?.first }
        if (cached != null) {
            try {
                return block(kvm, cached)
            } catch (_: KvmException.SessionExpired) {
                kvmLock.withLock { kvmCookies.remove(address) }
            }
        }
        val cookie = kvm.login()
        kvmLock.withLock { kvmCookies[address] = cookie to System.currentTimeMillis() }
        return block(kvm, cookie)
    }

    /** The power light: from the hub when it drives the KVM, else (or when it doesn't answer) read directly. */
    private suspend fun ledOn(d: Device): Boolean? {
        if (d.kvm?.hub != null) runCatching { hubMachine(d) }.getOrNull()?.let { m ->
            learnWiring(d, m.wiring)
            if (m.led != null) return m.led
        }
        return runCatching { withKvm(d) { k, c -> k.powerLedOn(c) } }.getOrNull()
    }

    // ---- The KVM hub --------------------------------------------------------------------------

    /**
     * The helper that drives this phone's KVMs: the one picked in Settings, or else the one Linux
     * helper this phone owns (an always-on server). Null: this phone drives them itself.
     */
    suspend fun kvmHub(): Device? {
        val owned = store.all().filter { it.hasHelper && !it.hasKey }
        return when (val choice = store.currentSettings().kvmHub) {
            HUB_PHONE -> null
            "" -> owned.filter { it.os == "linux" }.singleOrNull()
            else -> owned.firstOrNull { it.id == choice }
        }
    }

    private val hubLock = Mutex()
    private val hubCache = mutableMapOf<String, Pair<Long, List<HubMachine>>>()

    /** The hub's view of the device, read at most every 5 s (one call serves every device on that hub). */
    private suspend fun hubMachine(d: Device): HubMachine? {
        val k = d.kvm ?: return null
        val hub = k.hub?.let { store.get(it) } ?: return null
        val list = hubLock.withLock {
            val now = System.currentTimeMillis()
            hubCache[hub.id]?.takeIf { now - it.first < 5_000 }?.second
                ?: client(hub)!!.machines(kvmOnly = true).also { hubCache[hub.id] = now to it }
        }
        return list.firstOrNull { it.kvm == k.name }
    }

    private suspend fun learnWiring(d: Device, w: KvmWiring?) {
        val k = d.kvm ?: return
        if (w == null || (w == k.wiring && (k.ledSeen || !w.ledWired))) return
        // A light the hub has seen on is wired: this phone may press too when the hub can't be reached.
        store.update(d.id) { it.copy(kvm = it.kvm?.copy(wiring = w, ledSeen = it.kvm.ledSeen || w.ledWired)) }
    }

    /** Hands a KVM linked on this phone to [hub]. Null when it worked, else why not (also kept on the link). */
    private suspend fun moveToHub(d: Device, hub: Device): String? {
        val k = d.kvm ?: return null
        val pw = store.kvmPassword(d.id) ?: return "Its password isn't on this phone. Link it again."
        val err = try {
            val linked = client(hub)!!.addKvm(k.name, k.address, k.user, pw, d.fleetName, d.mac, d.hasHelper)
            store.setKvmLeftOnHub(hub.id, k.name, false) // back on a hub it was to be removed from
            store.update(d.id) {
                it.copy(kvm = it.kvm?.copy(hub = hub.id, machine = linked.machine, wiring = linked.wiring, hubError = null,
                    ledSeen = it.kvm.ledSeen || linked.wiring?.ledWired == true))
            }
            null
        } catch (e: HelperException.Api) {
            if (e.status == 404 && e.code == "not_found") "${hub.name} needs PowerDeck 2.2 or later to drive KVMs." else e.message
        } catch (e: HelperException.Unreachable) {
            "${hub.name} isn't answering."
        } catch (e: Exception) {
            e.message ?: "${hub.name} couldn't take it over."
        }
        if (err != null) store.update(d.id) { it.copy(kvm = it.kvm?.copy(hubError = err)) }
        return err
    }

    /**
     * Puts every KVM on the hub (linked before 2.2, or the hub changed), or back on this phone when
     * there's no hub. A failed move is tried again after LINK_RETRY_MS.
     */
    private suspend fun placeKvms(states: Map<String, DeviceState>) {
        val held = store.all().mapNotNull { d -> d.kvm?.let { it.hub to it.name } }.toSet()
        for ((hubId, name) in store.kvmsLeftOnHubs()) when {
            (hubId to name) in held -> store.setKvmLeftOnHub(hubId, name, false)
            due("kvm:-$name@$hubId") -> leaveHub(hubId, name)
        }
        val hub = kvmHub()
        for (d in store.all()) {
            val k = d.kvm ?: continue
            if (k.hub == hub?.id && (hub != null || k.hubError == null)) continue
            val old = k.hub
            if (hub == null || hub.id == d.id) { // a hub can't drive the KVM wired to itself
                old?.let { leaveHub(it, k.name) }
                store.update(d.id) { it.copy(kvm = it.kvm?.copy(hub = null, machine = null, hubError = null)) }
                continue
            }
            if (states[hub.id] !is DeviceState.On || !due("kvm:${d.id}>${hub.id}")) continue
            if (moveToHub(d, hub) == null && old != null) leaveHub(old, k.name)
        }
    }

    /**
     * Deletes a KVM (and its password) from a hub that no longer drives it. When the hub doesn't
     * answer, it's asked again on later refreshes, so it doesn't keep driving the KVM.
     */
    private suspend fun leaveHub(hubId: String, name: String) {
        val c = store.get(hubId)?.let { client(it) }
        val again = c != null && try {
            c.removeKvm(name)
            false
        } catch (e: HelperException.Unreachable) {
            true
        } catch (e: HelperException.Api) {
            e.status >= 500 // 404: already gone
        } catch (e: Exception) {
            true
        }
        store.setKvmLeftOnHub(hubId, name, again)
    }

    private suspend fun due(what: String): Boolean = linkLock.withLock {
        val now = System.currentTimeMillis()
        (linkTried[what]?.let { now - it >= LINK_RETRY_MS } ?: true).also { if (it) linkTried[what] = now }
    }

    /**
     * Runs an action through the device's hub. Null when there's no hub or it can't be reached: then
     * the phone acts itself. A refusal comes back as a result, not an exception.
     */
    private suspend fun hubAct(d: Device, action: String, force: Boolean = false, stayOff: Boolean = false, press: Boolean = false): HubResult? {
        val k = d.kvm ?: return null
        val hub = k.hub?.let { store.get(it) } ?: return null
        val c = client(hub) ?: return null
        return try {
            c.machineAction(k.machine ?: d.fleetName, action, force, stayOff, press)
        } catch (e: HelperException.Unreachable) {
            null
        } catch (e: HelperException.Api) {
            if (e.code == "no_kvm") { // the hub lost it (reinstalled): link it again on the next refresh
                store.update(d.id) { it.copy(kvm = it.kvm?.copy(hub = null, machine = null)) }
                return null
            }
            HubResult.from(e.body).copy(ok = false, message = e.message ?: "${hub.name} refused.")
        } finally {
            hubLock.withLock { hubCache.remove(hub.id) }
        }
    }

    private fun HubResult.outcome() = Outcome(ok, message)

    /** Forgets the hub's view of the wiring, so it's learned again (after re-cabling). */
    suspend fun recheckKvm(d: Device): Outcome = guarded {
        val k = d.kvm ?: return@guarded Outcome(false, "${d.name} has no KVM.")
        val hub = k.hub?.let { store.get(it) } ?: return@guarded Outcome(false, "Only a hub learns the wiring. Pick one in Settings.")
        val w = client(hub)!!.recheckKvm(k.name)
        store.update(d.id) { it.copy(kvm = it.kvm?.copy(wiring = w ?: it.kvm.wiring)) }
        Outcome(true, "${hub.name} checks ${k.name}'s wiring again, from the power light and the next presses.")
    }

    suspend fun unlinkKvm(d: Device) {
        val k = d.kvm
        store.unlinkKvm(d.id)
        k?.hub?.let { leaveHub(it, k.name) }
    }

    // ---- Adding devices -----------------------------------------------------------------------

    suspend fun addFromLink(info: AddInfo): AddResult = addHelper(info.host, info.port, info.pin, info.code)

    /** A typed Tailscale name or address. The certificate is pinned on this first contact. */
    suspend fun addByName(host: String): AddResult = addHelper(host, 47321, null, null)

    suspend fun addPeer(p: Peer): AddResult = addHelper(p.address, p.port, p.pin, null)

    private suspend fun addHelper(host: String, port: Int, pin: String?, code: String?): AddResult {
        if (!tailnet()) return AddResult.Failed("Tailscale isn't connected. Turn it on and try again.")
        var seenPin: String? = null
        val probe = HelperClient(http, host, port, pin) { seenPin = it }
        return try {
            val status = probe.status()
            save(host, port, pin ?: seenPin, status, null)
        } catch (e: HelperException.NotOwner) {
            val name = runCatching { probe.hello().name }.getOrNull()?.ifBlank { null } ?: host
            val fixedPin = pin ?: seenPin
            suspend fun withCode(c: String): AddResult {
                val normal = AddLink.normalizeCode(c) ?: return AddResult.Failed("A pairing code has 8 letters and digits, like 7K3Q-M9XD.")
                return try {
                    val (key, status) = HelperClient(http, host, port, fixedPin).pair(normal, phoneName())
                    save(host, port, fixedPin, status, key)
                } catch (e: Exception) {
                    AddResult.Failed(e.message ?: "Pairing failed.")
                }
            }
            if (code != null) withCode(code) else AddResult.NeedsCode(name, ::withCode)
        } catch (e: HelperException.Unreachable) {
            AddResult.Failed("No PowerDeck helper answered at $host. Is it installed and is the device on?")
        } catch (e: Exception) {
            AddResult.Failed(e.message ?: "Couldn't add the device.")
        }
    }

    private suspend fun save(host: String, port: Int, pin: String?, s: HelperStatus, key: String?): AddResult {
        if (pin == null) return AddResult.Failed("The device didn't present a certificate.")
        val d = Device(
            id = DeviceStore.newId(), name = s.name.ifBlank { host }, host = host, port = port, pin = pin, os = s.os,
            mac = s.mac, lan = s.lan, hasKey = key != null, guests = s.guestsRunning != null, me = s.youName,
            helperName = s.name.ifBlank { null },
        )
        val id = store.upsert(d, key)
        store.saveSnapshot(id, Power.On)
        return AddResult.Added(store.get(id) ?: d)
    }

    /** Helpers and KVMs the added helpers found on the tailnet that aren't on this phone yet. */
    suspend fun findPeers(refresh: Boolean): List<Peer> = coroutineScope {
        val devices = store.all()
        val found = devices.filter { it.hasHelper }.map { d -> async { withHelper(d) { peers(refresh) }.getOrDefault(emptyList()) } }.awaitAll().flatten()
        val knownPins = devices.mapNotNull { it.pin }.toSet()
        val knownHosts = devices.flatMap { listOfNotNull(it.host, it.kvm?.address) }.toSet()
        found.distinctBy { it.address }.filter { p -> (p.isHelper || p.isKvm) && p.pin !in knownPins && p.address !in knownHosts && (p.dnsName?.trimEnd('.') !in knownHosts) }
            .sortedWith(compareBy({ !it.isHelper }, { it.name.lowercase() }))
    }

    /** Checks a KVM login and links the KVM to a device's power button. */
    suspend fun linkKvm(deviceId: String, kind: String, address: String, name: String, user: String, password: String): Outcome {
        if (!tailnet()) return Outcome(false, "Tailscale isn't connected. Turn it on and try again.")
        if (!KvmAddress.allowed(address)) return Outcome(false, "$name has no Tailscale name. Turn on MagicDNS in the Tailscale admin console, then try again.")
        return try {
            val kvm = NanoKvmClient(http, address, user, password)
            val led = kvm.powerLedOn(kvm.login())
            // The power light must agree with the computer's helper, or a press later could hit the
            // wrong computer, or shut down a running one whose light isn't wired.
            val target = store.get(deviceId)
            var seen = led
            if (target != null && target.hasHelper) {
                val on = answers(target) != null // a changed identity still counts as running
                if (on && !led) return Outcome(false, "$name reads ${target.name}'s power light as off, but ${target.name} is on. Check that $name's power-LED cable is connected and wired to ${target.name}.")
                if (!on && led) return Outcome(false, "$name reads the power light as on, but ${target.name} isn't answering. Check it's wired to ${target.name}, or try again while ${target.name}'s helper is reachable.")
                seen = on && led
            }
            store.linkKvm(deviceId, KvmLink(kind, address, name, user, ledSeen = seen), password)
            kvmLock.withLock { kvmCookies.remove(address) }
            // The hub drives it from now on; this phone keeps the login for when the hub can't be reached.
            val hub = kvmHub()?.takeIf { it.id != deviceId }
            val hubNote = hub?.let { h -> store.get(deviceId)?.let { moveToHub(it, h) }?.let { " ${h.name} couldn't take it over: $it" } ?: " ${h.name} drives it." } ?: ""
            Outcome(true, (if (seen) "$name linked." else "$name linked. It will press the power button once it has seen the power light on: turn the computer on by hand once.") + hubNote)
        } catch (e: Exception) {
            Outcome(false, e.message ?: "Couldn't log in to the KVM.")
        }
    }

    /** A device with no helper, controlled only through its KVM (e.g. a server you can't install on). */
    suspend fun addKvmOnly(deviceName: String, kind: String, address: String, kvmName: String, user: String, password: String): Outcome {
        val id = store.upsert(Device(id = DeviceStore.newId(), name = deviceName, host = null, pin = null))
        val o = linkKvm(id, kind, address, kvmName, user, password)
        if (!o.ok) store.remove(id)
        return o
    }

    suspend fun remove(d: Device) {
        if (d.hasKey) runCatching { client(d)?.forgetMe() }
        store.remove(d.id)
    }

    // ---- Status -------------------------------------------------------------------------------

    suspend fun state(d: Device): DeviceState {
        val st = if (d.hasHelper) helperState(d) else kvmState(d) ?: DeviceState.Problem("Link a KVM or install the helper")
        store.saveSnapshot(
            d.id,
            when (st) {
                is DeviceState.On -> Power.On
                DeviceState.Off -> Power.Off
                else -> Power.Unknown
            },
        )
        return st
    }

    private suspend fun kvmState(d: Device): DeviceState? = d.kvm?.let {
        when (ledOn(d)) {
            true -> {
                if (!d.hasHelper) seeLed(d) // a helper device is confirmed in helperState, with the helper agreeing
                // The hub watches it: frozen, booting, asleep… (read with the light, so no extra call)
                DeviceState.On(null, if (it.hub != null) runCatching { hubMachine(d) }.getOrNull() else null)
            }
            false -> if (it.ledSeen || d.hasHelper) DeviceState.Off
                else DeviceState.Problem("Turn it on by hand once so PowerDeck can check its power light")
            null -> DeviceState.Problem("Can't reach ${it.name}")
        }
    }

    private suspend fun seeLed(d: Device) {
        if (d.kvm?.ledSeen == false) store.update(d.id) { it.copy(kvm = it.kvm?.copy(ledSeen = true)) }
    }

    private suspend fun helperState(d: Device): DeviceState = try {
        val s = client(d)!!.status()
        learn(d, s)
        // On, by its helper: if the KVM's light agrees, its LED is wired to this computer.
        if (d.kvm?.ledSeen == false && ledOn(d) == true) seeLed(d)
        DeviceState.On(s)
    } catch (e: HelperException.Unreachable) {
        // The helper can't answer while the machine is off (or booting). A KVM's power LED knows better.
        when (val k = kvmState(d)) {
            is DeviceState.On -> k
            null, is DeviceState.Problem -> DeviceState.Off
            else -> k
        }
    } catch (e: Exception) {
        DeviceState.Problem(e.message ?: "Unknown error")
    }

    /** Keeps what the phone knows about a device current (it may have a new network card or name). */
    private suspend fun learn(d: Device, s: HelperStatus) {
        // Follow the helper's name unless the user renamed the device here.
        val renamed = d.helperName != null && d.name != d.helperName
        val next = d.copy(
            name = if (renamed) d.name else s.name.ifBlank { d.name },
            helperName = s.name.ifBlank { d.helperName },
            os = s.os.ifBlank { d.os ?: "" }.ifBlank { null },
            mac = s.mac ?: d.mac,
            lan = s.lan.ifEmpty { d.lan },
            guests = s.guestsRunning != null,
            me = s.youName ?: d.me,
        )
        if (next != d) store.update(d.id) { next.copy(kvm = it.kvm) }
        // A helper upgraded from 2.0 trusts every personal device on its account until one claims
        // it. This phone is one of them (it got in without a key), so it claims it.
        if (s.ownersLegacy && !d.hasKey) runCatching { client(d)?.claim(phoneName()) }
    }

    // ---- Linking helpers ----------------------------------------------------------------------

    private val linkLock = Mutex()
    private val linkTried = mutableMapOf<String, Long>() // "a>b" -> last attempt; tried again after LINK_RETRY_MS

    /**
     * Keeps the owner's helpers able to reach each other, after a refresh: each one gets a peer key
     * (status, wake, power) for every other helper this phone owns, issued by that helper. Also
     * renames KVMs linked by IP address (before 2.1) to their Tailscale names.
     */
    suspend fun linkHelpers(states: Map<String, DeviceState>) {
        val devices = store.all()
        val up = devices.mapNotNull { d -> (states[d.id] as? DeviceState.On)?.status?.let { d to it } }
        val found = up.map { (d, s) -> d to s to runCatching { client(d)!!.peers(false) }.getOrDefault(emptyList()) }
        // Owned: no key needed, and new enough to hold peer keys.
        val owned = up.filter { (d, s) -> !d.hasKey && s.peerKeys != null }
        for ((pair, peers) in found) {
            val (a, sa) = pair
            if (owned.none { it.first.id == a.id }) continue
            for (p in peers.filter { it.isHelper && it.name !in sa.peerKeys!! }) {
                val b = owned.firstOrNull { it.first.id != a.id && it.first.pin == p.pin }?.first ?: continue
                if (!due("${a.id}>${b.id}")) continue
                runCatching {
                    val key = client(b)!!.addPeerKey(sa.name.ifBlank { a.name })
                    client(a)!!.setPeerKey(p.name, key)
                }
            }
        }
        val kvms = found.flatMap { it.second }.filter { it.isKvm && KvmAddress.allowed(it.kvmHost) }
        for (d in devices) {
            val k = d.kvm ?: continue
            if (KvmAddress.allowed(k.address)) continue
            val name = kvms.firstOrNull { it.address == k.address }?.kvmHost ?: continue
            store.update(d.id) { it.copy(kvm = it.kvm?.copy(address = name)) }
        }
        placeKvms(states)
    }

    // ---- Actions ------------------------------------------------------------------------------

    private suspend inline fun guarded(block: () -> Outcome): Outcome {
        if (!tailnet()) return Outcome(false, "Tailscale isn't connected. Open Tailscale and connect.")
        return try {
            block()
        } catch (e: Exception) {
            Outcome(false, e.message ?: "Something went wrong.")
        }
    }

    /**
     * Turns a device on. A KVM press comes first (it works even when Wake-on-LAN doesn't), but only
     * while its power LED says off: the button toggles. Then Wake-on-LAN from whatever is on that LAN.
     */
    suspend fun turnOn(device: Device): Outcome = oneAtATime(device.id) { turnOnNow(store.get(device.id) ?: device) }

    private suspend fun oneAtATime(id: String, block: suspend () -> Outcome): Outcome {
        val m = acting.getOrPut(id) { Mutex() }
        if (!m.tryLock()) return Outcome(false, "Already working on it.")
        return try { block() } finally { m.unlock() }
    }

    private suspend fun turnOnNow(d: Device): Outcome = guarded {
        // The hub checks the helper and the light, presses, and falls back to Wake-on-LAN itself.
        if (d.kvm?.hub != null) hubAct(d, "wake")?.let { r ->
            // It had no way to wake it: try this phone's side (other helpers, the phone's own LAN).
            return@guarded if (!r.ok && r.code == "cant_wake" && d.mac != null) wake(d, d.mac) else r.outcome()
        }
        // A press on a running computer shuts it down, so press only when every sign says it's off:
        // its helper doesn't answer (asked twice) and the power light reads off.
        // A changed identity still means something is running there: never press its button.
        if (d.hasHelper) answers(d)?.let { r ->
            return@guarded r.exceptionOrNull()?.let { e ->
                Outcome(false, "${d.name} is on, but " + (e.message?.replaceFirstChar(Char::lowercase) ?: "its helper answered with an error."))
            }
                ?: Outcome(true, "${d.name} is already on.")
        }
        if (d.kvm != null && d.kvm.ledSeen) {
            when (ledOn(d)) {
                true -> return@guarded Outcome(true, "${d.name}'s power light is on: it's on or still starting.")
                false -> {
                    withKvm(d) { k, c -> k.pressPower(c, 800) }
                    delay(3000)
                    if (ledOn(d) == true) return@guarded Outcome(true, "${d.name} is starting.")
                    // The press didn't take; fall through to Wake-on-LAN.
                }
                null -> Unit // can't read the light: never press blind, use Wake-on-LAN
            }
        }
        val mac = d.mac ?: return@guarded Outcome(false,
            if (d.kvm != null && !d.kvm.ledSeen) "PowerDeck hasn't seen ${d.name}'s power light on yet, so it won't press the button. Turn ${d.name} on by hand once."
            else "${d.name}'s network card isn't known yet. Open PowerDeck once while it's on.")
        wake(d, mac)
    }

    /**
     * Whether the helper answers, asked twice so a dropped packet doesn't read as "off". Success, or a
     * failure for any other answer (changed identity, throttled, an error): something is running
     * there. Null only when it's silent both times. hello needs no key, so a lost key can't hide it.
     */
    private suspend fun answers(d: Device): Result<Unit>? {
        val host = d.host ?: return null
        val c = HelperClient(http, host, d.port, d.pin, null)
        repeat(2) { i ->
            val r = runCatching { c.hello() }
            if (r.isSuccess) return Result.success(Unit)
            r.exceptionOrNull().let { if (it !is HelperException.Unreachable) return Result.failure(it ?: IllegalStateException()) }
            if (i == 0) delay(1500)
        }
        return null
    }

    private suspend fun wake(d: Device, mac: String): Outcome {
        val others = store.all().filter { it.id != d.id }
        // 1. A helper on the same LAN.
        val helpers = others.filter { it.hasHelper }.sortedByDescending { Lan.overlaps(it.lan, d.lan) }
        for (h in helpers) {
            if (d.lan.isNotEmpty() && h.lan.isNotEmpty() && !Lan.overlaps(h.lan, d.lan)) continue
            val sent = runCatching { client(h)!!.wake(mac) }.getOrNull()
            if (!sent.isNullOrEmpty()) return Outcome(true, "Wake signal sent by ${h.name}. ${d.name} usually takes under a minute.")
        }
        // 2. A KVM (its own, then any other).
        for (k in listOf(d) + others) {
            if (k.kvm == null) continue
            if (runCatching { withKvm(k) { kvm, c -> kvm.wake(c, mac) } }.getOrNull() != null) {
                return Outcome(true, "Wake signal sent by ${k.kvm.name}. ${d.name} usually takes under a minute.")
            }
        }
        // 3. The phone itself, when it's on that LAN.
        val mine = Lan.phoneSubnets(ctx)
        if (mine.isNotEmpty() && (d.lan.isEmpty() || Lan.overlaps(mine, d.lan)) && Lan.wake(mac, mine)) {
            return Outcome(true, "Wake signal sent from this phone. ${d.name} usually takes under a minute.")
        }
        return Outcome(false, "Nothing on ${d.name}'s network is on to send the wake signal. Install the helper on an always-on device there, or link a KVM.")
    }

    /** Shuts down or restarts through the helper; falls back to a KVM power-button press. */
    suspend fun powerOff(d: Device, restart: Boolean, force: Boolean = false, installUpdates: Boolean = false, pressKvm: Boolean = false): Outcome =
        oneAtATime(d.id) { powerOffNow(d, restart, force, installUpdates, pressKvm) }

    private suspend fun powerOffNow(d: Device, restart: Boolean, force: Boolean, installUpdates: Boolean, pressKvm: Boolean): Outcome = guarded {
        val verb = if (restart) "restart" else "shutdown"
        // A confirmed press may come minutes after the prompt: if the helper is back, use it instead,
        // so its in-use check still applies.
        val pressKvm = pressKvm && !(d.hasHelper && answers(d)?.isSuccess == true)
        if (d.hasHelper && !pressKvm) {
            try {
                val p = client(d)!!.power(verb, store.settings.first().countdownSeconds, force, installUpdates)
                store.saveSnapshot(d.id, if (restart) Power.On else Power.Off)
                return@guarded Outcome(true, "${d.name} ${if (restart) "restarts" else "shuts down"} ${countdown(p)}. You can cancel until then.")
            } catch (e: HelperException.Api) {
                when (e.code) {
                    "in_use" -> {
                        val who = Presence.from(e.body.optJSONObject("presence"))?.describe(d.name) ?: "Someone is using ${d.name}."
                        val word = if (restart) "Restart" else "Shut down"
                        return@guarded Outcome(false, who, ask = Ask("${d.name} is in use", "$who $word anyway? Unsaved work may be lost.", "$word anyway"))
                    }
                    "pending" -> return@guarded Outcome(false, "A shutdown or restart is already counting down.")
                    else -> throw e
                }
            } catch (e: HelperException.Unreachable) {
                if (d.kvm == null) return@guarded Outcome(true, "${d.name} isn't answering. It's probably already off.")
            }
        }
        if (restart) return@guarded Outcome(false, "${d.name} can only be restarted through its helper, which isn't answering.")
        // Through the hub, which reads the light before and after the press (and asks the helper if it's back).
        if (d.kvm?.hub != null && (!d.hasHelper || pressKvm)) hubAct(d, "shutdown", press = true)?.let { return@guarded it.outcome() }
        when (ledOn(d)) {
            null -> Outcome(false, "Can't reach ${d.kvm?.name ?: "the KVM"}.")
            false -> Outcome(true, "${d.name} is already off.")
            true -> {
                // Its helper would have checked whether someone is using it; without it, ask.
                if (d.hasHelper && !pressKvm) return@guarded Outcome(false, "${d.name}'s helper isn't answering.", ask = Ask(
                    "${d.name}'s helper isn't answering",
                    "Its power light is on, but PowerDeck can't check whether someone is using it. Press its power button? It shuts down cleanly.",
                    "Press power button", kvmPress = true))
                // A short press is an ACPI power-button event: the OS shuts down cleanly (Proxmox stops VMs first).
                withKvm(d) { k, c -> k.pressPower(c, 800) }
                store.saveSnapshot(d.id, Power.Off)
                Outcome(true, "Pressed ${d.name}'s power button. It shuts down cleanly, which can take a few minutes.")
            }
        }
    }

    suspend fun cancel(d: Device): Outcome = guarded {
        try {
            client(d)?.cancel() ?: return@guarded Outcome(false, "${d.name} has no helper.")
            Outcome(true, "Cancelled. ${d.name} stays on.")
        } catch (e: HelperException.Api) {
            if (e.code == "nothing_pending") Outcome(true, "Nothing was scheduled.") else throw e
        }
    }

    /** Emergency: holds the power button for 12 s. Only offered with a KVM, and only while the LED is on. */
    suspend fun forceOff(d: Device, force: Boolean = false): Outcome = oneAtATime(d.id) { forceOffNow(store.get(d.id) ?: d, force) }

    private suspend fun forceOffNow(d: Device, force: Boolean): Outcome = guarded {
        if (d.kvm?.hub != null) hubAct(d, "force-off", force = force, stayOff = true)?.let { r ->
            return@guarded if (r.code == "helper_answers") Outcome(false, r.message, ask = Ask("${d.name} is still answering",
                "Its helper answers, so it isn't frozen. Forcing it off is like pulling the plug: unsaved work is lost. Shut it down normally instead, or force it off anyway?",
                "Force off anyway", hubAction = "force-off"))
            else r.outcome()
        }
        if (ledOn(d) != true) return@guarded Outcome(false, "${d.name}'s power light is off (or the KVM can't be reached).")
        withKvm(d) { k, c -> k.pressPower(c, 12_000) }
        store.saveSnapshot(d.id, Power.Off)
        Outcome(true, "Held ${d.name}'s power button. It's off.")
    }

    /**
     * Presses the reset button through the hub, for a frozen machine. The hub refuses while the
     * machine's helper answers (unless [force]), then checks that it comes back.
     */
    suspend fun reset(d: Device, force: Boolean = false): Outcome = oneAtATime(d.id) { resetNow(store.get(d.id) ?: d, force) }

    private suspend fun resetNow(d: Device, force: Boolean): Outcome = guarded {
        val hub = d.kvm?.hub?.let { store.get(it) } ?: return@guarded Outcome(false, "Only a PowerDeck hub can reset ${d.name}. Pick one in Settings → KVM hub.")
        val r = hubAct(d, "reset", force = force)
            ?: return@guarded Outcome(false, "${hub.name} isn't answering, so it can't reset ${d.name}. Force ${d.name} off instead.")
        if (r.code == "helper_answers") Outcome(false, r.message, ask = Ask("${d.name} is still answering",
            "Its helper answers, so it isn't frozen. A reset is like pressing the reset button: unsaved work is lost. Reset anyway?",
            "Reset anyway", hubAction = "reset"))
        else r.outcome()
    }

    /** Helpers that can send this device's wake signal on a schedule (always-on, on its LAN). */
    suspend fun wakeHosts(d: Device): List<Device> =
        store.all().filter { it.id != d.id && it.hasHelper && (d.lan.isEmpty() || Lan.overlaps(it.lan, d.lan)) }

    private fun countdown(p: Pending?): String {
        val s = p?.let { Duration.between(Instant.now(), it.at).seconds } ?: 60
        return if (s <= 5) "now" else if (s < 90) "in $s seconds" else "in ${(s + 30) / 60} minutes"
    }
}
