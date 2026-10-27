package net.odysseylabs.powerdeck.data

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.net.URLEncoder
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import java.util.Base64
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager

/** base64url SHA-256 of a certificate's DER, as helpers print it. */
fun certPin(cert: X509Certificate): String =
    Base64.getUrlEncoder().withoutPadding().encodeToString(MessageDigest.getInstance("SHA-256").digest(cert.encoded))

/**
 * Trusts exactly one certificate: the helper's, by its pin. No CA is involved, so another device on the
 * tailnet can't pose as it. With no pin yet (a typed name) it accepts the first certificate and reports
 * it through [seen]; Tailscale has already authenticated the address, and the pin is kept from then on.
 */
class PinnedTrustManager(private val pin: String?, private val seen: (String) -> Unit = {}) : X509TrustManager {
    override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        val leaf = chain?.firstOrNull() ?: throw CertificateException("No certificate")
        val got = certPin(leaf)
        if (pin == null) {
            seen(got)
            return
        }
        val want = runCatching { Base64.getUrlDecoder().decode(pin) }.getOrNull() ?: throw CertificateException("Bad pin")
        if (!MessageDigest.isEqual(Base64.getUrlDecoder().decode(got), want)) throw CertificateException("Certificate changed")
    }

    override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) =
        throw CertificateException("Client certificates are not used")

    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
}

/** Why a helper call failed, phrased for the user. */
sealed class HelperException(message: String, cause: Throwable? = null) : IOException(message, cause) {
    class Unreachable(cause: Throwable? = null) : HelperException("No answer.", cause)
    class WrongDevice : HelperException("This device's identity changed. Remove it and add it again.")
    class NotOwner(message: String) : HelperException(message)
    class Unauthorized(message: String) : HelperException(message)
    class Api(val status: Int, val code: String, message: String, val body: JSONObject) : HelperException(message)
}

data class Hello(val name: String, val os: String, val version: String, val apiVersion: Int)

data class KeyInfo(val id: String, val name: String, val kind: String, val scopes: List<String>, val devices: List<String>, val bound: Boolean, val lastUsed: String?)

/** One PowerDeck helper, protocol v2 (docs/protocol.md). */
class HelperClient(
    base: OkHttpClient,
    host: String,
    port: Int,
    pin: String?,
    private val key: String? = null,
    seen: (String) -> Unit = {},
) {
    private val url = "https://${if (host.contains(':')) "[$host]" else host}:$port"
    private val http: OkHttpClient = run {
        val tm = PinnedTrustManager(pin, seen)
        val ctx = SSLContext.getInstance("TLS").apply { init(null, arrayOf(tm), null) }
        base.newBuilder()
            .sslSocketFactory(ctx.socketFactory, tm)
            .hostnameVerifier { _, _ -> true } // the pin replaces hostname checks
            .connectTimeout(4, TimeUnit.SECONDS)
            .readTimeout(15, TimeUnit.SECONDS)
            .build()
    }
    private val json = "application/json".toMediaType()

    private suspend fun call(method: String, path: String, body: JSONObject? = null, timeoutSeconds: Long? = null): JSONObject =
        withContext(Dispatchers.IO) {
            val b = Request.Builder().url(url + path)
            key?.let { b.header("Authorization", "Bearer $it") }
            when (method) {
                "GET" -> b.get()
                "DELETE" -> b.delete()
                "PUT" -> b.put((body ?: JSONObject()).toString().toRequestBody(json))
                else -> b.post((body ?: JSONObject()).toString().toRequestBody(json))
            }
            val client = timeoutSeconds?.let { http.newBuilder().readTimeout(it, TimeUnit.SECONDS).build() } ?: http
            try {
                client.newCall(b.build()).execute().use { r ->
                    val text = r.body.string()
                    val o = if (text.isBlank()) JSONObject() else runCatching { JSONObject(text) }.getOrElse { JSONObject() }
                    if (r.isSuccessful) return@use o
                    val code = o.optString("error")
                    val msg = o.optString("message").ifEmpty { "The device refused (HTTP ${r.code})." }
                    throw when {
                        r.code == 401 && code == "not_owner" -> HelperException.NotOwner(msg)
                        r.code == 401 -> HelperException.Unauthorized(msg)
                        else -> HelperException.Api(r.code, code, msg, o)
                    }
                }
            } catch (e: HelperException) {
                throw e
            } catch (e: IOException) {
                // A pin mismatch surfaces as an SSL error with our CertificateException below it; when OkHttp
                // then tries another address it's attached as a suppressed exception.
                throw if (hasCertificateFailure(e)) HelperException.WrongDevice() else HelperException.Unreachable(e)
            }
        }

    private fun hasCertificateFailure(t: Throwable, depth: Int = 0): Boolean =
        depth < 8 && (t is CertificateException || t.suppressed.any { hasCertificateFailure(it, depth + 1) } ||
            (t.cause?.let { hasCertificateFailure(it, depth + 1) } ?: false))

    private fun <T> JSONArray?.list(f: (JSONObject) -> T): List<T> = if (this == null) emptyList() else (0 until length()).map { f(getJSONObject(it)) }

    suspend fun hello(): Hello = call("GET", "/v2/hello").let {
        Hello(it.optString("name"), it.optString("os"), it.optString("version"), it.optInt("apiVersion"))
    }

    suspend fun status(): HelperStatus = HelperStatus.from(call("GET", "/v2/status"))

    suspend fun peers(refresh: Boolean): List<Peer> =
        call("GET", "/v2/peers" + if (refresh) "?refresh=1" else "", timeoutSeconds = 20).optJSONArray("peers").list(Peer::from)

    /** Shutdown or restart. Throws [HelperException.Api] with code `in_use` or `pending` on 409. */
    suspend fun power(action: String, delaySeconds: Int = 60, force: Boolean = false, installUpdates: Boolean = false): Pending? {
        val body = JSONObject().put("delaySeconds", delaySeconds).put("force", force).put("installUpdates", installUpdates)
        return HelperStatus.pendingFrom(call("POST", "/v2/power/$action", body).optJSONObject("pending"))
    }

    /** Installs the helper's latest release now; it restarts on its own a few seconds later. */
    suspend fun installUpdate(): HelperUpdate = HelperUpdate.from(call("POST", "/v2/update", timeoutSeconds = 45))

    suspend fun setAutoUpdate(on: Boolean) {
        call("POST", "/v2/settings", JSONObject().put("autoUpdate", on))
    }

    /** Whether AI agents may turn this device on and off. */
    suspend fun setAgents(on: Boolean) {
        call("POST", "/v2/settings", JSONObject().put("agents", on))
    }

    /** Uninstalls PowerDeck from the device; [confirm] must be its name. */
    suspend fun uninstall(confirm: String) {
        call("POST", "/v2/uninstall", JSONObject().put("confirm", confirm))
    }

    suspend fun cancel() {
        call("POST", "/v2/power/cancel")
    }

    /** Sends a magic packet from the helper's LANs; returns the interfaces it went out on. */
    suspend fun wake(mac: String): List<String> = call("POST", "/v2/wake", JSONObject().put("mac", mac)).optJSONArray("sentOn")?.strings() ?: emptyList()

    suspend fun fix(checkId: String): Check = Check.from(call("POST", "/v2/checks/${enc(checkId)}/fix", timeoutSeconds = 60).getJSONObject("check"))

    suspend fun events(): List<Event> = call("GET", "/v2/events").optJSONArray("events").list(Event::from)

    suspend fun schedules(): List<Schedule> = call("GET", "/v2/schedules").optJSONArray("schedules").list(Schedule::from)

    suspend fun addSchedule(s: Schedule): String {
        val body = JSONObject().put("action", s.action).put("time", s.time).put("days", JSONArray(s.days))
            .put("skipIfInUse", s.skipIfInUse).put("enabled", s.enabled)
        s.target?.let { body.put("target", it) }
        return call("POST", "/v2/schedules", body).optString("id")
    }

    suspend fun removeSchedule(id: String) {
        call("DELETE", "/v2/schedules/${enc(id)}")
    }

    suspend fun keys(): List<KeyInfo> = call("GET", "/v2/keys").optJSONArray("keys").list {
        KeyInfo(it.optString("id"), it.optString("name"), it.optString("kind"), it.optJSONArray("scopes")?.strings() ?: emptyList(),
            it.optJSONArray("devices")?.strings() ?: emptyList(), it.optBoolean("bound"), it.optStringOrNull("lastUsed"))
    }

    /** Creates an automation key; returns the secret (shown once). */
    suspend fun addKey(name: String, scopes: List<String>, devices: List<String>): String =
        call("POST", "/v2/keys", JSONObject().put("name", name).put("scopes", JSONArray(scopes)).put("devices", JSONArray(devices))).getString("key")

    suspend fun removeKey(id: String) {
        call("DELETE", "/v2/keys/${enc(id)}")
    }

    /**
     * Pairs with the code the helper shows. A phone on the helper's own Tailscale account becomes one
     * of its owner devices and gets no key (null); a phone on another account gets a key.
     */
    suspend fun pair(code: String, deviceName: String): Pair<String?, HelperStatus> {
        val o = call("POST", "/v2/pair", JSONObject().put("code", code).put("deviceName", deviceName))
        return o.optString("key").ifEmpty { null } to HelperStatus.from(o.getJSONObject("status"))
    }

    /** Records this phone as an owner device, ending the "anything on my account" rule of 2.0 helpers. */
    suspend fun claim(deviceName: String) {
        call("POST", "/v2/owners/claim", JSONObject().put("deviceName", deviceName))
    }

    /** Creates a peer key for another of the owner's helpers (status, wake, power); returns the secret. */
    suspend fun addPeerKey(forHelper: String): String =
        call("POST", "/v2/keys", JSONObject().put("name", forHelper).put("kind", "peer")).getString("key")

    /** Stores the key this helper presents to [peer] (its name in this helper's peer list). */
    suspend fun setPeerKey(peer: String, key: String) {
        call("PUT", "/v2/peers/${enc(peer)}/key", JSONObject().put("key", key))
    }

    /** Revokes this phone's own key. */
    suspend fun forgetMe() {
        call("DELETE", "/v2/key")
    }

    private fun enc(s: String) = URLEncoder.encode(s, "UTF-8")
}
