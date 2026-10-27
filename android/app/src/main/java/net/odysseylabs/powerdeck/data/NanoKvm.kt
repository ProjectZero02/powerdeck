package net.odysseylabs.powerdeck.data

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.io.IOException
import java.net.URLEncoder
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Base64
import javax.crypto.Cipher
import javax.crypto.spec.IvParameterSpec
import javax.crypto.spec.SecretKeySpec

/**
 * The NanoKVM web UI sends `encodeURIComponent(CryptoJS.AES.encrypt(password, "nanokvm-sipeed-2024"))`:
 * OpenSSL "Salted__" AES-256-CBC with an MD5 EVP_BytesToKey. The key is public, so this only avoids
 * plaintext on the wire; Tailscale (WireGuard) is what actually protects the password.
 */
object NanoKvmCrypto {
    private const val PASSPHRASE = "nanokvm-sipeed-2024"

    fun encryptPassword(password: String, random: SecureRandom = SecureRandom()): String {
        val salt = ByteArray(8).also { random.nextBytes(it) }
        val derived = evpBytesToKey(PASSPHRASE.toByteArray(Charsets.UTF_8), salt)
        val cipher = Cipher.getInstance("AES/CBC/PKCS5Padding")
        cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(derived.copyOfRange(0, 32), "AES"), IvParameterSpec(derived.copyOfRange(32, 48)))
        val sealed = "Salted__".toByteArray(Charsets.US_ASCII) + salt + cipher.doFinal(password.toByteArray(Charsets.UTF_8))
        return URLEncoder.encode(Base64.getEncoder().encodeToString(sealed), "UTF-8")
    }

    internal fun evpBytesToKey(pass: ByteArray, salt: ByteArray): ByteArray {
        val md5 = MessageDigest.getInstance("MD5")
        var out = ByteArray(0)
        var block = ByteArray(0)
        while (out.size < 48) {
            md5.reset()
            md5.update(block)
            md5.update(pass)
            md5.update(salt)
            block = md5.digest()
            out += block
        }
        return out
    }
}

/** Why a NanoKVM call failed, phrased for the user. */
sealed class KvmException(message: String) : IOException(message) {
    class Unreachable : KvmException("Can't reach the NanoKVM. Is Tailscale connected?")
    class BadPassword : KvmException("The NanoKVM rejected the password.")
    class Locked : KvmException("The NanoKVM locked logins after failed attempts. Try again in a few minutes.")
    class SessionExpired : KvmException("The NanoKVM session expired.")
    class Failed(what: String) : KvmException(what)
    class NotTailscaleName(address: String) : KvmException("$address isn't a Tailscale name. Set the KVM up again so PowerDeck reaches it as name.your-tailnet.ts.net (MagicDNS must be on).")
    class Forbidden : KvmException("The NanoKVM refused: this account isn't allowed to do that.")
}

/** NanoKVMs speak plain HTTP, which the app allows only to Tailscale (MagicDNS) names. */
object KvmAddress {
    fun allowed(address: String): Boolean = address.removePrefix("http://").substringBefore('/').substringBefore(':').lowercase().endsWith(".ts.net")
}

/**
 * Talks to the NanoKVM web API over Tailscale: power LED, power button, Wake-on-LAN.
 * Endpoints match NanoKVM firmware 2.x (`/api/auth/login`, `/api/vm/gpio`, `/api/network/wol`).
 */
class NanoKvmClient(
    private val http: OkHttpClient,
    address: String,
    private val username: String,
    private val password: String,
    /** Tests talk to a local fake; the app only ever reaches KVMs by Tailscale name. */
    private val requireTailscaleName: Boolean = true,
) {
    private val host = address.removePrefix("http://").trimEnd('/')
    private val base = "http://$host"
    private val json = "application/json".toMediaType()

    private suspend fun call(request: Request, client: OkHttpClient = http): Pair<JSONObject, okhttp3.Headers> = withContext(Dispatchers.IO) {
        if (requireTailscaleName && !KvmAddress.allowed(host)) throw KvmException.NotTailscaleName(host)
        try {
            client.newCall(request).execute().use { resp ->
                val body = resp.body.string()
                if (resp.code == 401) throw KvmException.SessionExpired()
                if (resp.code == 403) throw KvmException.Forbidden()
                val obj = runCatching { JSONObject(body) }.getOrElse { throw KvmException.Failed("Unexpected NanoKVM reply (HTTP ${resp.code}).") }
                obj to resp.headers
            }
        } catch (e: KvmException) {
            throw e
        } catch (e: IOException) {
            throw KvmException.Unreachable()
        }
    }

    /** Logs in and returns the Cookie header for later calls. */
    suspend fun login(): String {
        val payload = JSONObject().put("username", username).put("password", NanoKvmCrypto.encryptPassword(password))
        val (obj, headers) = call(Request.Builder().url("$base/api/auth/login").post(payload.toString().toRequestBody(json)).build())
        when (obj.optInt("code", -99)) {
            0 -> Unit
            -2 -> throw KvmException.BadPassword()
            -4, -5 -> throw KvmException.Locked()
            else -> throw KvmException.Failed("NanoKVM login failed: ${obj.optString("msg")}")
        }
        // Up to 2.4.x the token is in the body too; from 2.5 it's only the (HttpOnly) cookie.
        val token = obj.optJSONObject("data")?.optString("token")?.takeIf { it.isNotEmpty() }
            ?: headers.values("Set-Cookie").firstNotNullOfOrNull { c ->
                c.split(';').first().trim().takeIf { it.startsWith("nano-kvm-token=") }?.substringAfter('=')
            }
        return if (token != null) "nano-kvm-token=$token" else ""
    }

    private suspend fun api(cookie: String, path: String, body: JSONObject? = null, client: OkHttpClient = http): JSONObject {
        val b = Request.Builder().url(base + path).header("Cookie", cookie)
        if (body != null) b.post(body.toString().toRequestBody(json))
        val (obj, _) = call(b.build(), client)
        if (obj.optInt("code", -99) != 0) throw KvmException.Failed("NanoKVM $path failed: ${obj.optString("msg")}")
        return obj
    }

    /** The server's power LED as wired through the NanoKVM's ATX board. */
    suspend fun powerLedOn(cookie: String): Boolean {
        val data = api(cookie, "/api/vm/gpio").optJSONObject("data")
        // No reading is not "off": a press on a running computer would shut it down.
        if (data == null || !data.has("pwr")) throw KvmException.Failed("The NanoKVM didn't report the power light.")
        return data.getBoolean("pwr")
    }

    /** Presses the power button. 800 ms is a normal press (ACPI shutdown when on). */
    suspend fun pressPower(cookie: String, durationMs: Int = 800) {
        // The NanoKVM answers only after the press ends, so a long hold needs a longer read timeout.
        val client = http.newBuilder().readTimeout(durationMs + 15_000L, java.util.concurrent.TimeUnit.MILLISECONDS).build()
        api(cookie, "/api/vm/gpio", JSONObject().put("type", "power").put("duration", durationMs), client)
    }

    /** Sends a Wake-on-LAN magic packet from the NanoKVM's LAN. */
    suspend fun wake(cookie: String, mac: String) {
        api(cookie, "/api/network/wol", JSONObject().put("mac", mac))
    }
}
