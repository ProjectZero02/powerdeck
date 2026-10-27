package net.odysseylabs.powerdeck.data

import android.content.Context
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.intPreferencesKey
import androidx.datastore.preferences.core.longPreferencesKey
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map

data class PcConfig(
    val name: String,
    val host: String,
    val port: Int,
    val fingerprint: String,
    val mac: String?,
)

data class Config(
    val pc: PcConfig?,
    val nanoKvm: String?,
    val nanoKvmUser: String,
    val hasKvmPassword: Boolean,
    val proxmox: String?,
    val confirmShutdown: Boolean,
    val autoTailscale: Boolean,
) {
    val serverReady get() = nanoKvm != null && hasKvmPassword
}

/** Last known power states, shown by the widget and tiles without a network round trip. */
data class Snapshot(val pc: Power, val server: Power, val at: Long)

enum class Power { On, Off, Unknown }

private val Context.dataStore: DataStore<Preferences> by preferencesDataStore("powerdeck")

class ConfigStore(private val ctx: Context, private val box: SecretBox) {
    private object K {
        val pcName = stringPreferencesKey("pc.name")
        val pcHost = stringPreferencesKey("pc.host")
        val pcPort = intPreferencesKey("pc.port")
        val pcFp = stringPreferencesKey("pc.fingerprint")
        val pcMac = stringPreferencesKey("pc.mac")
        val pcToken = stringPreferencesKey("pc.token.sealed")
        val kvm = stringPreferencesKey("kvm.address")
        val kvmUser = stringPreferencesKey("kvm.user")
        val kvmPass = stringPreferencesKey("kvm.password.sealed")
        val pve = stringPreferencesKey("proxmox.address")
        val confirm = booleanPreferencesKey("opt.confirmShutdown")
        val autoTs = booleanPreferencesKey("opt.autoTailscale")
        val snapPc = stringPreferencesKey("snap.pc")
        val snapServer = stringPreferencesKey("snap.server")
        val snapAt = longPreferencesKey("snap.at")
    }

    val config: Flow<Config> = ctx.dataStore.data.map { p ->
        val pc = p[K.pcHost]?.let { host ->
            PcConfig(p[K.pcName] ?: "PC", host, p[K.pcPort] ?: 47321, p[K.pcFp] ?: "", p[K.pcMac])
        }
        Config(
            pc = if (p[K.pcToken] != null) pc else null,
            nanoKvm = p[K.kvm],
            nanoKvmUser = p[K.kvmUser] ?: "admin",
            hasKvmPassword = p[K.kvmPass] != null,
            proxmox = p[K.pve],
            confirmShutdown = p[K.confirm] ?: true,
            autoTailscale = p[K.autoTs] ?: true,
        )
    }

    val snapshot: Flow<Snapshot> = ctx.dataStore.data.map { p ->
        Snapshot(
            pc = p[K.snapPc]?.let { runCatching { Power.valueOf(it) }.getOrNull() } ?: Power.Unknown,
            server = p[K.snapServer]?.let { runCatching { Power.valueOf(it) }.getOrNull() } ?: Power.Unknown,
            at = p[K.snapAt] ?: 0,
        )
    }

    suspend fun current(): Config = config.first()

    suspend fun pcToken(): String? = ctx.dataStore.data.first()[K.pcToken]?.let(box::open)

    suspend fun kvmPassword(): String? = ctx.dataStore.data.first()[K.kvmPass]?.let(box::open)

    suspend fun savePairing(info: PairingInfo, result: PairResult) {
        ctx.dataStore.edit { p ->
            p[K.pcName] = result.pc.name.ifBlank { info.name }
            p[K.pcHost] = info.host
            p[K.pcPort] = info.port
            p[K.pcFp] = info.fingerprint
            (result.pc.mac ?: info.mac)?.let { p[K.pcMac] = it }
            p[K.pcToken] = box.seal(result.token)
            (result.pc.nanoKvm ?: info.nanoKvm)?.let { p[K.kvm] = it }
            (result.pc.proxmox ?: info.proxmox)?.let { p[K.pve] = it }
        }
    }

    /** Keeps addresses current when the PC reports a change (e.g. the NanoKVM got a new IP). */
    suspend fun updateFromPc(s: PcStatusDto) {
        ctx.dataStore.edit { p ->
            if (s.name.isNotBlank()) p[K.pcName] = s.name
            s.mac?.let { p[K.pcMac] = it }
            s.nanoKvm?.let { p[K.kvm] = it }
            s.proxmox?.let { p[K.pve] = it }
        }
    }

    suspend fun saveKvm(address: String, user: String, password: String) {
        ctx.dataStore.edit { p ->
            p[K.kvm] = address
            p[K.kvmUser] = user
            p[K.kvmPass] = box.seal(password)
        }
    }

    suspend fun clearKvmPassword() {
        ctx.dataStore.edit { it.remove(K.kvmPass) }
    }

    suspend fun setConfirmShutdown(on: Boolean) {
        ctx.dataStore.edit { it[K.confirm] = on }
    }

    suspend fun setAutoTailscale(on: Boolean) {
        ctx.dataStore.edit { it[K.autoTs] = on }
    }

    suspend fun saveSnapshot(pc: Power?, server: Power?) {
        ctx.dataStore.edit { p ->
            pc?.let { p[K.snapPc] = it.name }
            server?.let { p[K.snapServer] = it.name }
            p[K.snapAt] = System.currentTimeMillis()
        }
    }

    /** Forgets the PC (and everything learned from it). */
    suspend fun clearPc() {
        ctx.dataStore.edit { p ->
            p.remove(K.pcName)
            p.remove(K.pcHost)
            p.remove(K.pcPort)
            p.remove(K.pcFp)
            p.remove(K.pcMac)
            p.remove(K.pcToken)
            p.remove(K.snapPc)
        }
    }
}
