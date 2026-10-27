package net.odysseylabs.powerdeck.ui

import android.app.Application
import androidx.glance.appwidget.updateAll
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.async
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.receiveAsFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import net.odysseylabs.powerdeck.data.Config
import net.odysseylabs.powerdeck.data.Outcome
import net.odysseylabs.powerdeck.data.PairingInfo
import net.odysseylabs.powerdeck.data.PairingLink
import net.odysseylabs.powerdeck.data.PcState
import net.odysseylabs.powerdeck.data.Release
import net.odysseylabs.powerdeck.data.ServerState
import net.odysseylabs.powerdeck.data.Tailscale
import net.odysseylabs.powerdeck.data.Updater
import net.odysseylabs.powerdeck.deck
import net.odysseylabs.powerdeck.widget.PowerWidget

data class UiState(
    val config: Config? = null,
    val pc: PcState = PcState.Checking,
    val server: ServerState = ServerState.Checking,
    val busy: Set<Target> = emptySet(),
    val tailnet: Boolean = true,
    val tailscaleInstalled: Boolean = true,
    val update: Release? = null,
    val pairing: PairingStep = PairingStep.Idle,
    val kvm: KvmStep = KvmStep.Idle,
)

enum class Target { Pc, Server }

sealed interface PairingStep {
    data object Idle : PairingStep
    data class Working(val name: String) : PairingStep
    data class Failed(val message: String) : PairingStep
}

sealed interface KvmStep {
    data object Idle : KvmStep
    data object Working : KvmStep
    data class Failed(val message: String) : KvmStep
    data object Done : KvmStep
}

class MainViewModel(app: Application) : AndroidViewModel(app) {
    private val deck = app.deck
    private val _state = MutableStateFlow(UiState())
    val state: StateFlow<UiState> = _state.asStateFlow()
    private val _messages = Channel<String>(Channel.BUFFERED)
    val messages = _messages.receiveAsFlow()
    private var polling: Job? = null

    init {
        viewModelScope.launch { deck.store.config.collect { c -> _state.update { it.copy(config = c) } } }
        viewModelScope.launch { _state.update { it.copy(update = runCatching { Updater.available() }.getOrNull()) } }
    }

    /** Polls while the screen is visible. */
    fun startPolling() {
        if (polling?.isActive == true) return
        polling = viewModelScope.launch {
            while (isActive) {
                refreshNow()
                delay(5000)
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
        val config = deck.store.current()
        if (config.pc == null) return
        val tailnet = deck.tailnet()
        _state.update { it.copy(tailnet = tailnet, tailscaleInstalled = Tailscale.isInstalled(ctx)) }
        if (!tailnet) {
            _state.update { it.copy(pc = PcState.Problem("Tailscale is off"), server = ServerState.Problem("Tailscale is off")) }
            return
        }
        val pc = viewModelScope.async { deck.pcState() }
        val server = viewModelScope.async { deck.serverState() }
        val (p, s) = pc.await() to server.await()
        _state.update { it.copy(pc = p, server = s) }
        PowerWidget().updateAll(ctx)
    }

    private fun act(target: Target, block: suspend () -> Outcome) {
        if (target in _state.value.busy) return
        viewModelScope.launch {
            _state.update { it.copy(busy = it.busy + target) }
            val outcome = block()
            _messages.send(outcome.message)
            refreshNow()
            _state.update { it.copy(busy = it.busy - target) }
        }
    }

    fun pcOn() = act(Target.Pc) { deck.pcOn() }
    fun pcShutdown() = act(Target.Pc) { deck.pcAction("shutdown") }
    fun pcRestart() = act(Target.Pc) { deck.pcAction("restart") }
    fun pcCancel() = act(Target.Pc) { deck.pcAction("cancel") }
    fun serverOn() = act(Target.Server) { deck.serverOn() }
    fun serverOff() = act(Target.Server) { deck.serverOff() }

    // ---- Onboarding -----------------------------------------------------------------------------

    fun pair(text: String) {
        val info: PairingInfo = PairingLink.parse(text) ?: run {
            _state.update { it.copy(pairing = PairingStep.Failed("That isn't a PowerDeck pairing code. Scan the code shown in PowerDeck on your PC.")) }
            return
        }
        if (_state.value.pairing is PairingStep.Working) return
        viewModelScope.launch {
            _state.update { it.copy(pairing = PairingStep.Working(info.name)) }
            val outcome = deck.pair(info)
            _state.update { it.copy(pairing = if (outcome.ok) PairingStep.Idle else PairingStep.Failed(outcome.message)) }
            if (outcome.ok) {
                _messages.send(outcome.message)
                refreshNow()
            }
        }
    }

    fun clearPairingError() = _state.update { it.copy(pairing = PairingStep.Idle) }

    fun scanFailed(reason: String?) = _state.update {
        it.copy(pairing = PairingStep.Failed("Couldn't open the scanner${reason?.let { r -> " ($r)" } ?: ""}. Paste the pairing link instead."))
    }

    fun connectKvm(address: String, user: String, password: String) {
        viewModelScope.launch {
            _state.update { it.copy(kvm = KvmStep.Working) }
            val outcome = deck.connectKvm(address.trim(), user.trim().ifEmpty { "admin" }, password)
            _state.update { it.copy(kvm = if (outcome.ok) KvmStep.Done else KvmStep.Failed(outcome.message)) }
            if (outcome.ok) refreshNow()
        }
    }

    fun resetKvmStep() = _state.update { it.copy(kvm = KvmStep.Idle) }

    // ---- Settings -------------------------------------------------------------------------------

    fun unpair() {
        viewModelScope.launch {
            deck.unpair()
            _state.update { UiState(config = it.config) }
            _messages.send("Unpaired. Scan a new pairing code to connect again.")
        }
    }

    fun forgetKvm() = viewModelScope.launch { deck.store.clearKvmPassword() }
    fun setConfirmShutdown(on: Boolean) = viewModelScope.launch { deck.store.setConfirmShutdown(on) }
    fun setAutoTailscale(on: Boolean) = viewModelScope.launch { deck.store.setAutoTailscale(on) }

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
            viewModelScope.launch { _messages.send("Allow PowerDeck to install updates, then tap Update again.") }
            return
        }
        viewModelScope.launch {
            _messages.send("Downloading PowerDeck ${r.version}…")
            runCatching { Updater.install(ctx, r) }.onFailure { _messages.send(it.message ?: "Update failed.") }
        }
    }
}
