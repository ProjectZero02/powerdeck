package net.odysseylabs.powerdeck.data

import android.content.Context
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.intPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map
import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

const val MAX_COUNTDOWN = 600

data class Settings(
    val confirmShutdown: Boolean = true,
    val autoTailscale: Boolean = true,
    val notifications: Boolean = true,
    /** How long a shutdown or restart from this phone counts down (the helper allows 0–600). */
    val countdownSeconds: Int = 60,
)

/** Last known power state of a device, for the widget and tiles (no network round trip). */
data class Snapshot(val power: Power, val at: Long)

private val Context.dataStore: DataStore<Preferences> by preferencesDataStore("powerdeck2")

/**
 * The phone's device list. Devices are a JSON array; secrets (helper keys for devices on another
 * account, KVM passwords) are sealed with the Keystore and stored per device.
 */
class DeviceStore(private val ctx: Context, private val box: SecretBox) {
    private object K {
        val devices = stringPreferencesKey("devices")
        val snapshots = stringPreferencesKey("snapshots")
        val tiles = stringPreferencesKey("tiles")
        val seen = stringPreferencesKey("events.seen")
        val confirm = booleanPreferencesKey("opt.confirmShutdown")
        val autoTs = booleanPreferencesKey("opt.autoTailscale")
        val notify = booleanPreferencesKey("opt.notifications")
        val countdown = intPreferencesKey("opt.countdownSeconds")
        val appUpdate = stringPreferencesKey("app.update") // {"checkedAt": millis, "told": version}
        fun key(id: String) = stringPreferencesKey("secret.key.$id")
        fun kvm(id: String) = stringPreferencesKey("secret.kvm.$id")
    }

    private fun parse(p: Preferences): List<Device> =
        p[K.devices]?.let { s -> runCatching { JSONArray(s).let { a -> (0 until a.length()).map { Device.fromJson(a.getJSONObject(it)) } } }.getOrNull() }
            ?: emptyList()

    private fun map(p: Preferences, k: Preferences.Key<String>): JSONObject = p[k]?.let { runCatching { JSONObject(it) }.getOrNull() } ?: JSONObject()

    val devices: Flow<List<Device>> = ctx.dataStore.data.map(::parse).distinctUntilChanged()

    val settings: Flow<Settings> = ctx.dataStore.data.map { p ->
        Settings(p[K.confirm] ?: true, p[K.autoTs] ?: true, p[K.notify] ?: true, (p[K.countdown] ?: 60).coerceIn(0, MAX_COUNTDOWN))
    }.distinctUntilChanged()

    val snapshots: Flow<Map<String, Snapshot>> = ctx.dataStore.data.map { p ->
        val o = map(p, K.snapshots)
        o.keys().asSequence().associateWith { id ->
            val s = o.getJSONObject(id)
            Snapshot(runCatching { Power.valueOf(s.optString("p")) }.getOrDefault(Power.Unknown), s.optLong("at"))
        }
    }.distinctUntilChanged()

    /** Device ids assigned to the Quick Settings tile slots (index = slot). */
    val tiles: Flow<List<String?>> = ctx.dataStore.data.map { p ->
        val a = p[K.tiles]?.let { runCatching { JSONArray(it) }.getOrNull() } ?: JSONArray()
        (0 until TILE_SLOTS).map { a.optString(it).ifEmpty { null } }
    }.distinctUntilChanged()

    suspend fun all(): List<Device> = devices.first()
    suspend fun get(id: String): Device? = all().firstOrNull { it.id == id }
    suspend fun currentSettings(): Settings = settings.first()

    suspend fun helperKey(id: String): String? = ctx.dataStore.data.first()[K.key(id)]?.let(box::open)
    suspend fun kvmPassword(id: String): String? = ctx.dataStore.data.first()[K.kvm(id)]?.let(box::open)

    private suspend fun write(change: (MutableList<Device>) -> Unit) {
        ctx.dataStore.edit { p ->
            val list = parse(p).toMutableList()
            change(list)
            p[K.devices] = JSONArray(list.map(Device::toJson)).toString()
        }
    }

    /** Adds or replaces a device (matched by id, then by helper pin, then by name). Returns its id. */
    suspend fun upsert(d: Device, key: String? = null): String {
        var id = d.id
        write { list ->
            val i = list.indexOfFirst { it.id == d.id }.takeIf { it >= 0 }
                ?: list.indexOfFirst { d.pin != null && it.pin == d.pin }.takeIf { it >= 0 }
                // Same address and port: the same helper, reinstalled (new certificate). Never by name:
                // two machines can share one, and the new one must not inherit the other's KVM.
                ?: list.indexOfFirst { d.host != null && it.host.equals(d.host, ignoreCase = true) && it.port == d.port }.takeIf { it >= 0 }
            if (i != null) {
                id = list[i].id
                val renamed = list[i].helperName != null && list[i].name != list[i].helperName
                list[i] = d.copy(id = id, name = if (renamed) list[i].name else d.name, kvm = d.kvm ?: list[i].kvm, hasKey = d.hasKey || list[i].hasKey)
            } else {
                list += d
            }
        }
        if (key != null) ctx.dataStore.edit { it[K.key(id)] = box.seal(key) }
        return id
    }

    suspend fun update(id: String, change: (Device) -> Device) = write { list ->
        val i = list.indexOfFirst { it.id == id }
        if (i >= 0) list[i] = change(list[i])
    }

    suspend fun move(id: String, by: Int) = write { list ->
        val i = list.indexOfFirst { it.id == id }
        val j = i + by
        if (i >= 0 && j in list.indices) list.add(j, list.removeAt(i))
    }

    suspend fun linkKvm(id: String, kvm: KvmLink, password: String) {
        update(id) { it.copy(kvm = kvm) }
        ctx.dataStore.edit { it[K.kvm(id)] = box.seal(password) }
    }

    suspend fun unlinkKvm(id: String) {
        update(id) { it.copy(kvm = null) }
        ctx.dataStore.edit { it.remove(K.kvm(id)) }
    }

    suspend fun remove(id: String) {
        write { list -> list.removeAll { it.id == id } }
        ctx.dataStore.edit { p ->
            p.remove(K.key(id))
            p.remove(K.kvm(id))
            p[K.snapshots] = map(p, K.snapshots).apply { remove(id) }.toString()
            p[K.seen] = map(p, K.seen).apply { remove(id) }.toString()
            val tiles = p[K.tiles]?.let { runCatching { JSONArray(it) }.getOrNull() }
            if (tiles != null) p[K.tiles] = JSONArray((0 until tiles.length()).map { tiles.optString(it).takeIf { t -> t != id } ?: "" }).toString()
        }
    }

    suspend fun saveSnapshot(id: String, power: Power) {
        ctx.dataStore.edit { p ->
            val o = map(p, K.snapshots)
            val old = o.optJSONObject(id)
            // Unchanged state within a minute: skip the write (polling would otherwise churn the file).
            if (old?.optString("p") == power.name && System.currentTimeMillis() - old.optLong("at") < 60_000) return@edit
            p[K.snapshots] = o.put(id, JSONObject().put("p", power.name).put("at", System.currentTimeMillis())).toString()
        }
    }

    suspend fun snapshot(id: String): Snapshot = snapshots.first()[id] ?: Snapshot(Power.Unknown, 0)

    suspend fun setTile(slot: Int, id: String?) {
        ctx.dataStore.edit { p ->
            val a = p[K.tiles]?.let { runCatching { JSONArray(it) }.getOrNull() } ?: JSONArray()
            val cur = (0 until TILE_SLOTS).map { a.optString(it).ifEmpty { null } }.toMutableList()
            cur[slot] = id
            p[K.tiles] = JSONArray(cur.map { it ?: "" }).toString()
        }
    }

    /** Last event time already notified about, per device (ISO instant). */
    suspend fun seenEvents(id: String): String? = map(ctx.dataStore.data.first(), K.seen).optString(id).ifEmpty { null }

    suspend fun setSeenEvents(id: String, at: String) {
        ctx.dataStore.edit { p -> p[K.seen] = map(p, K.seen).put(id, at).toString() }
    }

    /** When the app last looked for its own update, and which version it last told you about. */
    suspend fun appUpdateCheck(): Pair<Long, String?> = map(ctx.dataStore.data.first(), K.appUpdate).let { it.optLong("checkedAt") to it.optString("told").ifEmpty { null } }

    suspend fun setAppUpdateCheck(at: Long, told: String?) {
        ctx.dataStore.edit { p -> p[K.appUpdate] = JSONObject().put("checkedAt", at).put("told", told ?: "").toString() }
    }

    suspend fun setConfirmShutdown(on: Boolean) = ctx.dataStore.edit { it[K.confirm] = on }
    suspend fun setAutoTailscale(on: Boolean) = ctx.dataStore.edit { it[K.autoTs] = on }
    suspend fun setNotifications(on: Boolean) = ctx.dataStore.edit { it[K.notify] = on }
    suspend fun setCountdown(seconds: Int) = ctx.dataStore.edit { it[K.countdown] = seconds.coerceIn(0, MAX_COUNTDOWN) }

    companion object {
        const val TILE_SLOTS = 4
        fun newId(): String = UUID.randomUUID().toString().substring(0, 8)
    }
}
