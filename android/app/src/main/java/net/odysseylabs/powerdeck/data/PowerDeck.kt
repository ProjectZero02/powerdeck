package net.odysseylabs.powerdeck.data

import android.content.Context
import android.os.Build
import android.provider.Settings
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

/** All device logic. Screens, the widget, tiles and the background worker call into this. */
class PowerDeck(private val ctx: Context, val store: DeviceStore) {
    private val http = OkHttpClient.Builder()
        .connectTimeout(4, TimeUnit.SECONDS)
        .readTimeout(15, TimeUnit.SECONDS)
        .retryOnConnectionFailure(false)
        .build()

    suspend fun tailnet(): Boolean = Tailscale.ensureConnected(ctx, store.currentSettings().autoTailscale)

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

    private suspend fun ledOn(d: Device): Boolean? = runCatching { withKvm(d) { k, c -> k.powerLedOn(c) } }.getOrNull()

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
        return try {
            NanoKvmClient(http, address, user, password).login()
            store.linkKvm(deviceId, KvmLink(kind, address, name, user), password)
            kvmLock.withLock { kvmCookies.remove(address) }
            Outcome(true, "$name linked")
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
            true -> DeviceState.On(null)
            false -> DeviceState.Off
            null -> DeviceState.Problem("Can't reach ${it.name}")
        }
    }

    private suspend fun helperState(d: Device): DeviceState = try {
        val s = client(d)!!.status()
        learn(d, s)
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
        val next = d.copy(
            name = s.name.ifBlank { d.name },
            os = s.os.ifBlank { d.os ?: "" }.ifBlank { null },
            mac = s.mac ?: d.mac,
            lan = s.lan.ifEmpty { d.lan },
            guests = s.guestsRunning != null,
            me = s.youName ?: d.me,
        )
        if (next != d) store.update(d.id) { next.copy(kvm = it.kvm) }
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
    suspend fun turnOn(d: Device): Outcome = guarded {
        if (d.kvm != null) {
            when (ledOn(d)) {
                true -> return@guarded Outcome(true, "${d.name} is already on.")
                false -> {
                    withKvm(d) { k, c -> k.pressPower(c, 800) }
                    delay(3000)
                    if (ledOn(d) == true) return@guarded Outcome(true, "${d.name} is starting.")
                    // The press didn't take; fall through to Wake-on-LAN.
                }
                null -> Unit
            }
        }
        if (d.hasHelper && runCatching { client(d)!!.hello() }.isSuccess) return@guarded Outcome(true, "${d.name} is already on.")
        val mac = d.mac ?: return@guarded Outcome(false, "${d.name}'s network card isn't known yet. Open PowerDeck once while it's on.")
        wake(d, mac)
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
    suspend fun powerOff(d: Device, restart: Boolean, force: Boolean = false, installUpdates: Boolean = false): Outcome = guarded {
        val verb = if (restart) "restart" else "shutdown"
        if (d.hasHelper) {
            try {
                val p = client(d)!!.power(verb, 60, force, installUpdates)
                store.saveSnapshot(d.id, if (restart) Power.On else Power.Off)
                return@guarded Outcome(true, "${d.name} ${if (restart) "restarts" else "shuts down"} ${countdown(p)}. You can cancel until then.")
            } catch (e: HelperException.Api) {
                when (e.code) {
                    "in_use" -> return@guarded Outcome(false, Presence.from(e.body.optJSONObject("presence"))?.describe(d.name) ?: "Someone is using ${d.name}.",
                        inUse = Presence.from(e.body.optJSONObject("presence")) ?: Presence(true, null, 0, false, false, 0))
                    "pending" -> return@guarded Outcome(false, "A shutdown or restart is already counting down.")
                    else -> throw e
                }
            } catch (e: HelperException.Unreachable) {
                if (d.kvm == null) return@guarded Outcome(true, "${d.name} isn't answering. It's probably already off.")
            }
        }
        if (restart) return@guarded Outcome(false, "${d.name} can only be restarted through its helper, which isn't answering.")
        when (ledOn(d)) {
            null -> Outcome(false, "Can't reach ${d.kvm?.name ?: "the KVM"}.")
            false -> Outcome(true, "${d.name} is already off.")
            true -> {
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
    suspend fun forceOff(d: Device): Outcome = guarded {
        if (ledOn(d) != true) return@guarded Outcome(false, "${d.name}'s power light is off (or the KVM can't be reached).")
        withKvm(d) { k, c -> k.pressPower(c, 12_000) }
        store.saveSnapshot(d.id, Power.Off)
        Outcome(true, "Held ${d.name}'s power button. It's off.")
    }

    /** Helpers that can send this device's wake signal on a schedule (always-on, on its LAN). */
    suspend fun wakeHosts(d: Device): List<Device> =
        store.all().filter { it.id != d.id && it.hasHelper && (d.lan.isEmpty() || Lan.overlaps(it.lan, d.lan)) }

    private fun countdown(p: Pending?): String {
        val s = p?.let { Duration.between(Instant.now(), it.at).seconds } ?: 60
        return if (s <= 5) "now" else if (s < 90) "in $s seconds" else "in ${(s + 30) / 60} minutes"
    }
}
