package net.odysseylabs.powerdeck.data

import android.content.Context
import android.os.Build
import android.provider.Settings
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import java.net.InetSocketAddress
import java.net.Socket
import java.util.concurrent.TimeUnit

sealed interface PcState {
    data object Checking : PcState
    data class On(val uptimeSeconds: Long, val pending: PcPending?) : PcState
    data object Off : PcState
    data class Problem(val message: String) : PcState
}

sealed interface ServerState {
    data object Checking : ServerState
    data object NotSetUp : ServerState
    data class On(val proxmoxReady: Boolean?) : ServerState
    data object Off : ServerState
    data class Problem(val message: String) : ServerState
}

/** Result of a button press, phrased for a toast or snackbar. */
data class Outcome(val ok: Boolean, val message: String)

/** All device logic. Screens, the widget and the tiles call into this. */
class PowerDeck(private val ctx: Context, val store: ConfigStore) {
    private val http = OkHttpClient.Builder()
        .connectTimeout(4, TimeUnit.SECONDS)
        .readTimeout(10, TimeUnit.SECONDS)
        .retryOnConnectionFailure(false)
        .build()

    suspend fun tailnet(): Boolean = Tailscale.ensureConnected(ctx, store.current().autoTailscale)

    private suspend fun pcClient(): PcClient? {
        val pc = store.current().pc ?: return null
        val token = store.pcToken() ?: return null
        return PcClient(http, pc.host, pc.port, pc.fingerprint, token)
    }

    // One NanoKVM login is reused for a while instead of logging in on every refresh.
    private var kvmSession: Pair<String, Long>? = null

    private suspend fun <T> withKvm(block: suspend (NanoKvmClient, String) -> T): T? {
        val kvm = kvmClient() ?: return null
        kvmSession?.takeIf { System.currentTimeMillis() - it.second < 10 * 60_000 }?.let { (cookie, _) ->
            try {
                return block(kvm, cookie)
            } catch (e: KvmException.SessionExpired) {
                kvmSession = null
            }
        }
        val cookie = kvm.login()
        kvmSession = cookie to System.currentTimeMillis()
        return block(kvm, cookie)
    }

    private suspend fun kvmClient(): NanoKvmClient? {
        val c = store.current()
        val address = c.nanoKvm ?: return null
        val password = store.kvmPassword() ?: return null
        return NanoKvmClient(http, address, c.nanoKvmUser, password)
    }

    // ---- Pairing ------------------------------------------------------------------------------

    private fun deviceName(): String =
        Settings.Global.getString(ctx.contentResolver, Settings.Global.DEVICE_NAME)?.takeIf { it.isNotBlank() }
            ?: "${Build.MANUFACTURER} ${Build.MODEL}"

    suspend fun pair(info: PairingInfo): Outcome {
        if (!tailnet()) return Outcome(false, "Tailscale isn't connected. Turn it on and try again.")
        return try {
            val result = PcClient(http, info.host, info.port, info.fingerprint, null).pair(info.code, deviceName(), Build.MODEL)
            store.savePairing(info, result)
            store.saveSnapshot(Power.On, null)
            Outcome(true, "Paired with ${result.pc.name}")
        } catch (e: Exception) {
            Outcome(false, e.message ?: "Pairing failed.")
        }
    }

    suspend fun unpair() {
        runCatching { pcClient()?.unpair() }
        store.clearPc()
    }

    /** Saves the NanoKVM login after checking it works. */
    suspend fun connectKvm(address: String, user: String, password: String): Outcome {
        if (!tailnet()) return Outcome(false, "Tailscale isn't connected. Turn it on and try again.")
        return try {
            NanoKvmClient(http, address, user, password).login()
            store.saveKvm(address, user, password)
            kvmSession = null
            Outcome(true, "NanoKVM connected")
        } catch (e: Exception) {
            Outcome(false, e.message ?: "Couldn't log in to the NanoKVM.")
        }
    }

    // ---- Status -------------------------------------------------------------------------------

    suspend fun pcState(): PcState {
        val client = pcClient() ?: return PcState.Problem("Not paired")
        return try {
            val s = client.status()
            store.updateFromPc(s)
            store.saveSnapshot(Power.On, null)
            PcState.On(s.uptimeSeconds, s.pending)
        } catch (e: PcException.Unreachable) {
            store.saveSnapshot(Power.Off, null)
            PcState.Off
        } catch (e: Exception) {
            PcState.Problem(e.message ?: "Unknown error")
        }
    }

    suspend fun serverState(): ServerState {
        return try {
            val on = withKvm { kvm, cookie -> kvm.powerLedOn(cookie) } ?: return ServerState.NotSetUp
            store.saveSnapshot(null, if (on) Power.On else Power.Off)
            if (on) ServerState.On(proxmoxReady()) else ServerState.Off
        } catch (e: Exception) {
            store.saveSnapshot(null, Power.Unknown)
            ServerState.Problem(e.message ?: "Unknown error")
        }
    }

    /** Whether the Proxmox web UI port answers, i.e. the server has finished booting. */
    private suspend fun proxmoxReady(): Boolean? {
        val addr = store.current().proxmox ?: return null
        val host = addr.substringBeforeLast(':')
        val port = addr.substringAfterLast(':', "8006").toIntOrNull() ?: 8006
        return withContext(Dispatchers.IO) {
            runCatching { Socket().use { it.connect(InetSocketAddress(host, port), 1500); true } }.getOrDefault(false)
        }
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

    suspend fun pcOn(): Outcome = guarded {
        val mac = store.current().pc?.mac ?: return@guarded Outcome(false, "The PC's network card isn't known yet. Re-pair the PC.")
        withKvm { kvm, cookie -> kvm.wake(cookie, mac) }
            ?: return@guarded Outcome(false, "Connect your NanoKVM first: it sends the wake signal.")
        Outcome(true, "Wake signal sent. The PC takes about 30 seconds to start.")
    }

    suspend fun pcAction(action: String): Outcome = guarded {
        val client = pcClient() ?: return@guarded Outcome(false, "Not paired with a PC.")
        try {
            client.power(action)
            store.saveSnapshot(if (action == "cancel") Power.On else Power.Off, null)
            Outcome(
                true,
                when (action) {
                    "shutdown" -> "The PC shuts down in 30 seconds."
                    "restart" -> "The PC restarts in 30 seconds."
                    else -> "Cancelled."
                },
            )
        } catch (e: PcException.Unreachable) {
            Outcome(false, if (action == "cancel") "The PC isn't answering." else "The PC is already off.")
        }
    }

    suspend fun serverOn(): Outcome = guarded {
        val kvm = kvmClient() ?: return@guarded Outcome(false, "Connect your NanoKVM first.")
        val cookie = kvm.login().also { kvmSession = it to System.currentTimeMillis() }
        // The power button toggles: never press it when the server is already on.
        if (kvm.powerLedOn(cookie)) return@guarded Outcome(true, "The server is already on.")
        kvm.pressPower(cookie)
        delay(4000)
        if (kvm.powerLedOn(cookie)) {
            store.saveSnapshot(null, Power.On)
            Outcome(true, "The server is starting. Services are back in about 2 minutes.")
        } else {
            Outcome(false, "Pressed the power button, but the power light is still off. Check the NanoKVM.")
        }
    }

    suspend fun serverOff(): Outcome = guarded {
        val kvm = kvmClient() ?: return@guarded Outcome(false, "Connect your NanoKVM first.")
        val cookie = kvm.login().also { kvmSession = it to System.currentTimeMillis() }
        if (!kvm.powerLedOn(cookie)) return@guarded Outcome(true, "The server is already off.")
        // A short press is an ACPI power-button event: Proxmox shuts every VM down cleanly first.
        kvm.pressPower(cookie)
        store.saveSnapshot(null, Power.Off)
        Outcome(true, "The server is shutting down. It takes 1 to 3 minutes.")
    }
}
