package net.odysseylabs.powerdeck.ui

import android.app.Application
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.receiveAsFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import net.odysseylabs.powerdeck.data.AddInfo
import net.odysseylabs.powerdeck.data.AddLink
import net.odysseylabs.powerdeck.data.AddResult
import net.odysseylabs.powerdeck.data.Check
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.DeviceState
import net.odysseylabs.powerdeck.data.Event
import net.odysseylabs.powerdeck.data.KeyInfo
import net.odysseylabs.powerdeck.data.Outcome
import net.odysseylabs.powerdeck.data.Peer
import net.odysseylabs.powerdeck.data.Ask
import net.odysseylabs.powerdeck.data.Release
import net.odysseylabs.powerdeck.data.Schedule
import net.odysseylabs.powerdeck.data.HelperUpdate
import net.odysseylabs.powerdeck.data.Settings
import net.odysseylabs.powerdeck.data.Tailscale
import net.odysseylabs.powerdeck.data.Updater
import net.odysseylabs.powerdeck.deck
import net.odysseylabs.powerdeck.work.EventsWorker

data class UiState(
    val loaded: Boolean = false,
    val devices: List<Device> = emptyList(),
    val states: Map<String, DeviceState> = emptyMap(),
    val busy: Set<String> = emptySet(),
    val tailnet: Boolean = true,
    val tailscaleInstalled: Boolean = true,
    val settings: Settings = Settings(),
    val tiles: List<String?> = emptyList(),
    val update: Release? = null,
    val add: AddStep = AddStep.Idle,
    val found: List<Peer>? = null,
    val finding: Boolean = false,
    val force: ForcePrompt? = null,
)

sealed interface AddStep {
    data object Idle : AddStep
    data class Working(val what: String) : AddStep
    data class NeedsCode(val name: String, val retry: suspend (String) -> AddResult) : AddStep
    /** An add link opened from outside the app (a web page, a chat): ask before adding. */
    /** A scanned code or a link: name it, then add. [external] links came from another app. */
    data class Review(val info: AddInfo, val external: Boolean) : AddStep
    data class Failed(val message: String) : AddStep
}

/** Someone is using the device: ask before forcing. */
data class ForcePrompt(val device: Device, val restart: Boolean, val ask: Ask)

/** What the device screen shows beyond the live state. */
data class Detail(
    val events: List<Event>? = null,
    val schedules: List<Pair<Device, Schedule>>? = null,
    val keys: List<KeyInfo>? = null,
    val newKey: String? = null,
    val error: String? = null,
)

class MainViewModel(app: Application) : AndroidViewModel(app) {
    private val deck = app.deck
    private val _state = MutableStateFlow(UiState())
    val state: StateFlow<UiState> = _state.asStateFlow()
    private val _detail = MutableStateFlow(Detail())
    val detail: StateFlow<Detail> = _detail.asStateFlow()
    private val _messages = Channel<String>(Channel.BUFFERED)
    val messages = _messages.receiveAsFlow()
    private var polling: Job? = null
    private var fastUntil = 0L
    private val waking = mutableMapOf<String, Long>()

    init {
        viewModelScope.launch {
            deck.store.devices.collect { d -> _state.update { s -> s.copy(loaded = true, devices = d, states = s.states.filterKeys { k -> d.any { it.id == k } }) } }
        }
        viewModelScope.launch { deck.store.settings.collect { st -> _state.update { it.copy(settings = st) } } }
        viewModelScope.launch { deck.store.tiles.collect { t -> _state.update { it.copy(tiles = t) } } }
        viewModelScope.launch { _state.update { it.copy(update = runCatching { Updater.available() }.getOrNull()) } }
    }

    fun say(msg: String) {
        viewModelScope.launch { _messages.send(msg) }
    }

    // ---- Polling ------------------------------------------------------------------------------

    /** Polls while the app is visible: every 5 s, every 2 s for two minutes after an action. */
    fun startPolling() {
        if (polling?.isActive == true) return
        polling = viewModelScope.launch {
            while (isActive) {
                refreshNow()
                delay(if (System.currentTimeMillis() < fastUntil) 2000 else 5000)
            }
        }
    }

    fun stopPolling() {
        polling?.cancel()
        polling = null
    }

    fun refresh() {
        viewModelScope.launch { refreshNow() }
    }

    private suspend fun refreshNow() {
        val ctx = getApplication<Application>()
        val devices = deck.store.all()
        if (devices.isEmpty()) return
        val tailnet = deck.tailnet()
        _state.update { it.copy(tailnet = tailnet, tailscaleInstalled = Tailscale.isInstalled(ctx)) }
        if (!tailnet) {
            _state.update { s -> s.copy(states = devices.associate { it.id to DeviceState.Problem("Tailscale is off") }) }
            return
        }
        val results = devices.map { d -> viewModelScope.async { d.id to deck.state(d) } }.awaitAll()
        for ((id, st) in results) {
            val started = waking[id]
            if (started != null && st is DeviceState.On && st.status != null) {
                waking.remove(id)
                val name = devices.first { it.id == id }.name
                say("$name is on (took ${(System.currentTimeMillis() - started) / 1000} s).")
            }
        }
        _state.update { it.copy(states = it.states + results) }
        Shortcuts.refreshAll(ctx)
    }

    // ---- Power --------------------------------------------------------------------------------

    private fun act(d: Device, block: suspend () -> Outcome) {
        if (d.id in _state.value.busy) return
        viewModelScope.launch {
            _state.update { it.copy(busy = it.busy + d.id) }
            val outcome = block()
            if (outcome.ask == null) _messages.send(outcome.message) // otherwise the prompt shows
            fastUntil = System.currentTimeMillis() + 120_000
            refreshNow()
            _state.update { it.copy(busy = it.busy - d.id) }
        }
    }

    fun turnOn(d: Device) = act(d) {
        deck.turnOn(d).also { if (it.ok && !it.message.contains("already")) waking[d.id] = System.currentTimeMillis() }
    }

    fun powerOff(d: Device, restart: Boolean, installUpdates: Boolean = false) = act(d) {
        deck.powerOff(d, restart, installUpdates = installUpdates).also { o ->
            o.ask?.let { a -> _state.update { it.copy(force = ForcePrompt(d, restart, a)) } }
        }
    }

    fun forcePower(p: ForcePrompt) {
        _state.update { it.copy(force = null) }
        act(p.device) {
            if (p.ask.kvmPress) deck.powerOff(p.device, p.restart, pressKvm = true) else deck.powerOff(p.device, p.restart, force = true)
        }
    }

    fun dismissForce() = _state.update { it.copy(force = null) }
    fun cancel(d: Device) = act(d) { deck.cancel(d) }
    fun forceOff(d: Device) = act(d) { deck.forceOff(d) }

    // ---- Adding -------------------------------------------------------------------------------

    /** The name chosen in the Add sheet, applied once the device is in (also after a pairing code). */
    private var wantedName: String? = null

    private fun runAdd(what: String, block: suspend () -> AddResult) {
        if (_state.value.add is AddStep.Working) return
        viewModelScope.launch {
            _state.update { it.copy(add = AddStep.Working(what)) }
            handle(block())
        }
    }

    private suspend fun handle(r: AddResult) {
        when (r) {
            is AddResult.Added -> {
                val name = wantedName?.trim()?.take(40)?.ifBlank { null }
                wantedName = null
                if (name != null && name != r.device.name) deck.store.update(r.device.id) { it.copy(name = name) }
                _state.update { it.copy(add = AddStep.Idle, found = it.found?.filterNot { p -> p.address == r.device.host }) }
                _messages.send("Added ${name ?: r.device.name}")
                EventsWorker.schedule(getApplication())
                refreshNow()
                find(refresh = true)
            }
            is AddResult.NeedsCode -> _state.update { it.copy(add = AddStep.NeedsCode(r.name, r.retry)) }
            is AddResult.Failed -> {
                wantedName = null
                _state.update { it.copy(add = AddStep.Failed(r.message)) }
            }
        }
    }

    /** From a scanned QR code: shows the Add sheet. */
    fun addLink(text: String) {
        val info = AddLink.parse(text) ?: run {
            _state.update { it.copy(add = AddStep.Failed("That isn't a PowerDeck code. Scan the code the helper shows.")) }
            return
        }
        _state.update { it.copy(add = AddStep.Review(info, external = false)) }
    }

    /** A powerdeck://add link from another app: confirm first, it could come from anywhere. */
    fun openLink(text: String) {
        val info = AddLink.parse(text) ?: run {
            _state.update { it.copy(add = AddStep.Failed("That link isn't a valid PowerDeck code.")) }
            return
        }
        _state.update { it.copy(add = AddStep.Review(info, external = true)) }
    }

    /** Adds the reviewed device, called [name] on this phone. */
    fun confirmLink(name: String) {
        val step = _state.value.add as? AddStep.Review ?: return
        wantedName = name
        runAdd(name.ifBlank { step.info.name }) { deck.addFromLink(step.info) }
    }

    fun addByName(text: String) {
        val host = AddLink.cleanHost(text) ?: run {
            _state.update { it.copy(add = AddStep.Failed("Type the device's Tailscale name, like my-pc, or its 100.x address.")) }
            return
        }
        wantedName = null
        runAdd(host) { deck.addByName(host) }
    }

    fun addPeer(p: Peer) {
        wantedName = null
        runAdd(p.name) { deck.addPeer(p) }
    }

    fun submitCode(code: String) {
        val step = _state.value.add as? AddStep.NeedsCode ?: return
        runAdd(step.name) { step.retry(code) }
    }

    fun resetAdd() {
        if (_state.value.add !is AddStep.Working) wantedName = null
        _state.update { it.copy(add = AddStep.Idle) }
    }

    fun find(refresh: Boolean) {
        if (_state.value.finding || _state.value.devices.none { it.hasHelper }) return
        viewModelScope.launch {
            _state.update { it.copy(finding = true) }
            val peers = runCatching { deck.findPeers(refresh) }.getOrDefault(emptyList())
            _state.update { it.copy(finding = false, found = peers) }
        }
    }

    /** Links a KVM to [deviceId], or with [deviceId] null adds a KVM-only device called [newName]. */
    fun linkKvm(deviceId: String?, newName: String, kind: String, address: String, kvmName: String, user: String, password: String, done: (Boolean) -> Unit) {
        viewModelScope.launch {
            val o = if (deviceId != null) deck.linkKvm(deviceId, kind, address, kvmName, user.ifBlank { "admin" }, password)
            else deck.addKvmOnly(newName.ifBlank { kvmName }, kind, address, kvmName, user.ifBlank { "admin" }, password)
            _messages.send(o.message)
            if (o.ok) {
                _state.update { it.copy(found = it.found?.filterNot { p -> p.address == address }) }
                refreshNow()
            }
            done(o.ok)
        }
    }

    // ---- Device screen ------------------------------------------------------------------------

    fun loadDetail(d: Device) {
        _detail.value = Detail()
        if (!d.hasHelper) return
        viewModelScope.launch {
            val events = deck.withHelper(d) { events() }
            val schedules = loadSchedules(d)
            _detail.update {
                it.copy(
                    events = events.getOrNull()?.sortedByDescending { e -> e.at },
                    schedules = schedules,
                    error = events.exceptionOrNull()?.message,
                )
            }
        }
    }

    /** This device's own schedules plus wake schedules other helpers keep for it. */
    private suspend fun loadSchedules(d: Device): List<Pair<Device, Schedule>> {
        val own = deck.withHelper(d) { schedules() }.getOrDefault(emptyList()).filter { it.action != "wake" }.map { d to it }
        val wakes = deck.wakeHosts(d).flatMap { h ->
            deck.withHelper(h) { schedules() }.getOrDefault(emptyList()).filter { it.action == "wake" && (it.target.equals(d.fleetName, true) || it.target.equals(d.name, true)) }.map { h to it }
        }
        return own + wakes
    }

    fun addSchedule(d: Device, action: String, time: String, days: List<String>, skipIfInUse: Boolean) {
        viewModelScope.launch {
            val r = if (action == "wake") {
                val hosts = deck.wakeHosts(d)
                if (hosts.isEmpty()) {
                    _messages.send("A wake schedule needs another device with the helper on ${d.name}'s network (one that's on at that time).")
                    return@launch
                }
                // Prefer a server: it's the one most likely to be on.
                val host = hosts.sortedByDescending { it.kind == Device.Kind.Server }.first()
                deck.withHelper(host) { addSchedule(Schedule("", "wake", d.fleetName, time, days, false, true)) }.map { host }
            } else {
                deck.withHelper(d) { addSchedule(Schedule("", action, null, time, days, skipIfInUse, true)) }.map { d }
            }
            r.onSuccess { host -> _messages.send("Scheduled${if (host.id != d.id) " (kept by ${host.name})" else ""}.") }
                .onFailure { _messages.send(it.message ?: "Couldn't save the schedule.") }
            _detail.update { it.copy(schedules = loadSchedules(d)) }
        }
    }

    fun removeSchedule(d: Device, host: Device, s: Schedule) {
        viewModelScope.launch {
            deck.withHelper(host) { removeSchedule(s.id) }.onFailure { _messages.send(it.message ?: "Couldn't remove it.") }
            _detail.update { it.copy(schedules = loadSchedules(d)) }
        }
    }

    fun fix(d: Device, c: Check) {
        act(d) {
            deck.withHelper(d) { fix(c.id) }.fold(
                { Outcome(it.ok, if (it.ok) "Fixed: ${it.title}" else it.detail ?: it.title) },
                { Outcome(false, it.message ?: "The fix didn't work.") },
            )
        }
    }

    fun loadKeys(d: Device) {
        viewModelScope.launch {
            val r = deck.withHelper(d) { keys() }
            _detail.update { it.copy(keys = r.getOrNull()?.filter { k -> k.kind != "phone" }, error = r.exceptionOrNull()?.message) }
        }
    }

    fun addKey(d: Device, name: String, power: Boolean, devices: List<String>) {
        viewModelScope.launch {
            val scopes = if (power) listOf("status", "wake", "power") else listOf("status", "wake")
            deck.withHelper(d) { addKey(name, scopes, devices) }
                .onSuccess { k -> _detail.update { it.copy(newKey = k) } }
                .onFailure { _messages.send(it.message ?: "Couldn't create the key.") }
            loadKeys(d)
            refreshNow() // the device pages show which devices an agent can reach
        }
    }

    fun clearNewKey() = _detail.update { it.copy(newKey = null) }

    fun removeKey(d: Device, k: KeyInfo) {
        viewModelScope.launch {
            deck.withHelper(d) { removeKey(k.id) }.onFailure { _messages.send(it.message ?: "Couldn't remove it.") }
            loadKeys(d)
            refreshNow()
        }
    }

    fun rename(d: Device, name: String) = viewModelScope.launch { deck.store.update(d.id) { it.copy(name = name.trim().take(40).ifBlank { it.name }) } }
    fun unlinkKvm(d: Device) = viewModelScope.launch { deck.store.unlinkKvm(d.id) }
    fun move(d: Device, by: Int) = viewModelScope.launch { deck.store.move(d.id, by) }

    fun remove(d: Device) {
        viewModelScope.launch {
            deck.remove(d)
            _messages.send("Removed ${d.name}")
            Shortcuts.refreshAll(getApplication())
        }
    }

    /** Removes [d] from this phone after asking its helper to uninstall PowerDeck. */
    fun uninstall(d: Device) {
        val name = (_state.value.states[d.id] as? DeviceState.On)?.status?.name ?: return
        viewModelScope.launch {
            deck.withHelper(d) { uninstall(name) }.fold(
                {
                    deck.store.remove(d.id)
                    _messages.send("Uninstalling PowerDeck from ${d.name}. It's removed from this phone.")
                    Shortcuts.refreshAll(getApplication())
                },
                { _messages.send(it.message ?: "Couldn't reach ${d.name}. Nothing was removed.") },
            )
        }
    }

    /** Lets AI agents turn [d] on and off, or not. They can always see its status. */
    fun setAgents(d: Device, on: Boolean) {
        viewModelScope.launch {
            deck.withHelper(d) { setAgents(on) }.fold(
                { refreshNow() },
                { _messages.send(it.message ?: "Couldn't reach ${d.name}; try again when it's on.") },
            )
        }
    }

    fun setTile(slot: Int, d: Device?) = viewModelScope.launch {
        deck.store.setTile(slot, d?.id)
        Shortcuts.refreshAll(getApplication())
    }

    // ---- Settings -----------------------------------------------------------------------------

    fun setConfirmShutdown(on: Boolean) = viewModelScope.launch { deck.store.setConfirmShutdown(on) }
    fun setCountdown(seconds: Int) = viewModelScope.launch { deck.store.setCountdown(seconds) }
    fun setAutoTailscale(on: Boolean) = viewModelScope.launch { deck.store.setAutoTailscale(on) }
    fun setNotifications(on: Boolean) = viewModelScope.launch {
        deck.store.setNotifications(on)
        if (on) EventsWorker.schedule(getApplication()) else EventsWorker.cancel(getApplication())
    }

    // ---- Helper updates -----------------------------------------------------------------------

    /** Your helpers and their update state, from the last refresh (owners see it; keys don't). */
    fun helperUpdates(s: UiState = _state.value): List<Pair<Device, HelperUpdate>> =
        s.devices.mapNotNull { d -> ((s.states[d.id] as? DeviceState.On)?.status?.update)?.let { d to it } }

    fun updateHelper(d: Device) = act(d) { installHelperUpdate(d).outcome(d) }

    /** What asking one helper to update came to. */
    private sealed interface HelperUpdateResult {
        data class Updating(val to: String?) : HelperUpdateResult
        data object UpToDate : HelperUpdateResult
        data class Failed(val why: String) : HelperUpdateResult
    }

    private fun HelperUpdateResult.outcome(d: Device) = when (this) {
        is HelperUpdateResult.Updating -> Outcome(true, "${d.name} is updating to ${to ?: "the latest version"}. It's back in about a minute.")
        HelperUpdateResult.UpToDate -> Outcome(true, "${d.name} is up to date.")
        is HelperUpdateResult.Failed -> Outcome(false, "${d.name}: $why")
    }

    private suspend fun installHelperUpdate(d: Device): HelperUpdateResult = deck.withHelper(d) { installUpdate() }.fold(
        { u -> if (u.installing || u.available) HelperUpdateResult.Updating(u.latest) else HelperUpdateResult.UpToDate },
        { HelperUpdateResult.Failed(it.message ?: "couldn't update.") }, // the helper's own reason: counting down, no update server, ...
    )

    /**
     * Helpers too old to update from the app (before 2.0.1); they update themselves every few hours.
     * Judged by version: newer helpers on another account also hide update details (owners only).
     */
    private fun olderHelpers(s: UiState = _state.value): List<Device> =
        s.devices.filter { d -> (s.states[d.id] as? DeviceState.On)?.status?.let { it.update == null && Updater.newer("2.0.1", it.version) } == true }

    fun updateAllHelpers() {
        viewModelScope.launch {
            val list = helperUpdates()
            val older = olderHelpers().map { it.name }
            val olderNote = if (older.isEmpty()) "" else " ${older.joinToString(", ")} ${if (older.size == 1) "has" else "have"} an older helper that updates itself within 6 hours."
            if (list.isEmpty()) {
                _messages.send(if (older.isEmpty()) "Open PowerDeck while your computers are on to update them." else olderNote.trim())
                return@launch
            }
            val results = list.map { (d, _) -> async { d to installHelperUpdate(d) } }.awaitAll()
            val updating = results.filter { it.second is HelperUpdateResult.Updating }.map { it.first.name }
            val failed = results.mapNotNull { (d, r) -> (r as? HelperUpdateResult.Failed)?.let { "${d.name}: ${it.why}" } }
            _messages.send(buildString {
                when {
                    updating.isNotEmpty() -> append("Updating ${updating.joinToString(", ")}.")
                    failed.isEmpty() -> append("Every device that's on is up to date.")
                }
                failed.forEach { append(if (isEmpty()) it else " $it") }
                append(olderNote)
            }.trim())
            fastUntil = System.currentTimeMillis() + 120_000
            refreshNow()
        }
    }

    fun setHelperAutoUpdate(on: Boolean) {
        viewModelScope.launch {
            val list = helperUpdates()
            val failed = list.map { (d, _) -> async { d to deck.withHelper(d) { setAutoUpdate(on) }.isSuccess } }.awaitAll().filter { !it.second }.map { it.first.name }
            _messages.send(when {
                list.isEmpty() -> "Open PowerDeck while your computers are on to change this."
                failed.isNotEmpty() -> "Couldn't reach ${failed.joinToString(", ")}; try again when it's on."
                on -> "Your devices update themselves again."
                else -> "Automatic updates are off. Update from here when you like."
            })
            refreshNow()
        }
    }

    fun checkUpdate() {
        viewModelScope.launch {
            val r = runCatching { Updater.available() }.getOrElse {
                _messages.send("Couldn't check for updates. Try again when you're online.")
                return@launch
            }
            _state.update { it.copy(update = r) }
            _messages.send(if (r != null) "PowerDeck ${r.version} is available." else "PowerDeck is up to date.")
        }
    }

    fun installUpdate() {
        val r = _state.value.update ?: return
        val ctx = getApplication<Application>()
        if (!Updater.canInstall(ctx)) {
            ctx.startActivity(Updater.allowInstallsIntent(ctx))
            say("Allow PowerDeck to install updates, then tap Update again.")
            return
        }
        viewModelScope.launch {
            _messages.send("Downloading PowerDeck ${r.version}…")
            runCatching { Updater.install(ctx, r) }.onFailure { _messages.send(it.message ?: "Update failed.") }
        }
    }
}
