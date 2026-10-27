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
import net.odysseylabs.powerdeck.data.AddLink
import net.odysseylabs.powerdeck.data.AddResult
import net.odysseylabs.powerdeck.data.Check
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.DeviceState
import net.odysseylabs.powerdeck.data.Event
import net.odysseylabs.powerdeck.data.KeyInfo
import net.odysseylabs.powerdeck.data.Outcome
import net.odysseylabs.powerdeck.data.Peer
import net.odysseylabs.powerdeck.data.Presence
import net.odysseylabs.powerdeck.data.Release
import net.odysseylabs.powerdeck.data.Schedule
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
    data class Failed(val message: String) : AddStep
}

/** Someone is using the device: ask before forcing. */
data class ForcePrompt(val device: Device, val restart: Boolean, val presence: Presence)

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
            if (outcome.inUse == null) _messages.send(outcome.message) // otherwise the force prompt shows
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
            o.inUse?.let { p -> _state.update { it.copy(force = ForcePrompt(d, restart, p)) } }
        }
    }

    fun forcePower(p: ForcePrompt) {
        _state.update { it.copy(force = null) }
        act(p.device) { deck.powerOff(p.device, p.restart, force = true) }
    }

    fun dismissForce() = _state.update { it.copy(force = null) }
    fun cancel(d: Device) = act(d) { deck.cancel(d) }
    fun forceOff(d: Device) = act(d) { deck.forceOff(d) }

    // ---- Adding -------------------------------------------------------------------------------

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
                _state.update { it.copy(add = AddStep.Idle, found = it.found?.filterNot { p -> p.address == r.device.host }) }
                _messages.send("Added ${r.device.name}")
                EventsWorker.schedule(getApplication())
                refreshNow()
                find(refresh = true)
            }
            is AddResult.NeedsCode -> _state.update { it.copy(add = AddStep.NeedsCode(r.name, r.retry)) }
            is AddResult.Failed -> _state.update { it.copy(add = AddStep.Failed(r.message)) }
        }
    }

    /** From a scanned QR code or a powerdeck://add link. */
    fun addLink(text: String) {
        val info = AddLink.parse(text) ?: run {
            _state.update { it.copy(add = AddStep.Failed("That isn't a PowerDeck code. Scan the code the helper shows.")) }
            return
        }
        runAdd(info.name) { deck.addFromLink(info) }
    }

    fun addByName(text: String) {
        val host = AddLink.cleanHost(text) ?: run {
            _state.update { it.copy(add = AddStep.Failed("Type the device's Tailscale name, like my-pc, or its 100.x address.")) }
            return
        }
        runAdd(host) { deck.addByName(host) }
    }

    fun addPeer(p: Peer) = runAdd(p.name) { deck.addPeer(p) }

    fun submitCode(code: String) {
        val step = _state.value.add as? AddStep.NeedsCode ?: return
        runAdd(step.name) { step.retry(code) }
    }

    fun resetAdd() = _state.update { it.copy(add = AddStep.Idle) }

    fun scanFailed(reason: String?) = _state.update {
        it.copy(add = AddStep.Failed("Couldn't open the scanner${reason?.let { r -> " ($r)" } ?: ""}. Type the device's name instead."))
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
            deck.withHelper(h) { schedules() }.getOrDefault(emptyList()).filter { it.action == "wake" && it.target.equals(d.name, true) }.map { h to it }
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
                deck.withHelper(host) { addSchedule(Schedule("", "wake", d.name, time, days, false, true)) }.map { host }
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
        }
    }

    fun clearNewKey() = _detail.update { it.copy(newKey = null) }

    fun removeKey(d: Device, k: KeyInfo) {
        viewModelScope.launch {
            deck.withHelper(d) { removeKey(k.id) }.onFailure { _messages.send(it.message ?: "Couldn't remove it.") }
            loadKeys(d)
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

    fun setTile(slot: Int, d: Device?) = viewModelScope.launch {
        deck.store.setTile(slot, d?.id)
        Shortcuts.refreshAll(getApplication())
    }

    // ---- Settings -----------------------------------------------------------------------------

    fun setConfirmShutdown(on: Boolean) = viewModelScope.launch { deck.store.setConfirmShutdown(on) }
    fun setAutoTailscale(on: Boolean) = viewModelScope.launch { deck.store.setAutoTailscale(on) }
    fun setNotifications(on: Boolean) = viewModelScope.launch {
        deck.store.setNotifications(on)
        if (on) EventsWorker.schedule(getApplication()) else EventsWorker.cancel(getApplication())
    }

    fun checkUpdate() {
        viewModelScope.launch {
            val r = Updater.available()
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
