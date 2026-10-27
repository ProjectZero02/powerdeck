package net.odysseylabs.powerdeck.data

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.io.IOException
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import java.time.Instant
import java.util.Base64
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager

/**
 * Trusts exactly one certificate: the PC's, identified by the SHA-256 pin from the pairing QR code.
 * No CA is involved, so another device on the tailnet can't impersonate the PC.
 */
class PinnedTrustManager(fingerprint: String) : X509TrustManager {
    private val pin: ByteArray = Base64.getUrlDecoder().decode(fingerprint)

    override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        val leaf = chain?.firstOrNull() ?: throw CertificateException("No certificate")
        val digest = MessageDigest.getInstance("SHA-256").digest(leaf.encoded)
        if (!MessageDigest.isEqual(digest, pin)) throw CertificateException("This isn't the PC you paired with")
    }

    override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) =
        throw CertificateException("Client certificates are not used")

    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
}

sealed class PcException(message: String, cause: Throwable? = null) : IOException(message, cause) {
    class Unreachable(cause: Throwable? = null) : PcException("No answer from the PC.", cause)
    class WrongPc : PcException("The PC's identity changed. Pair again.")
    class Unpaired : PcException("This phone isn't paired with the PC any more. Pair again.")
    class Rejected(message: String) : PcException(message)
}

data class PcPending(val action: String, val at: Instant)

data class PcStatusDto(
    val name: String,
    val version: String,
    val uptimeSeconds: Long,
    val pending: PcPending?,
    val mac: String?,
    val nanoKvm: String?,
    val proxmox: String?,
) {
    companion object {
        fun from(o: JSONObject) = PcStatusDto(
            name = o.optString("name"),
            version = o.optString("version"),
            uptimeSeconds = o.optLong("uptimeSeconds"),
            pending = o.optJSONObject("pending")?.let { p -> PcPending(p.optString("action"), Instant.parse(p.optString("at"))) },
            mac = o.optString("mac").takeIf { it.isNotEmpty() },
            nanoKvm = o.optJSONObject("nanokvm")?.optString("address")?.takeIf { it.isNotEmpty() },
            proxmox = o.optJSONObject("proxmox")?.optString("address")?.takeIf { it.isNotEmpty() },
        )
    }
}

data class PairResult(val deviceId: String, val token: String, val pc: PcStatusDto)

/** The PowerDeck service on the PC (protocol v1). */
class PcClient(base: OkHttpClient, host: String, port: Int, fingerprint: String, private val token: String?) {
    private val url = "https://${if (host.contains(':')) "[$host]" else host}:$port"
    private val http: OkHttpClient = run {
        val tm = PinnedTrustManager(fingerprint)
        val ctx = SSLContext.getInstance("TLS").apply { init(null, arrayOf(tm), null) }
        base.newBuilder()
            .sslSocketFactory(ctx.socketFactory, tm)
            .hostnameVerifier { _, _ -> true } // the pin above replaces hostname checks
            .connectTimeout(4, TimeUnit.SECONDS)
            .readTimeout(10, TimeUnit.SECONDS)
            .build()
    }
    private val json = "application/json".toMediaType()

    private suspend fun call(method: String, path: String, body: JSONObject? = null): Pair<Int, JSONObject> = withContext(Dispatchers.IO) {
        val b = Request.Builder().url(url + path)
        token?.let { b.header("Authorization", "Bearer $it") }
        val rb = (body ?: JSONObject()).toString().toRequestBody(json)
        when (method) {
            "GET" -> b.get()
            "DELETE" -> b.delete()
            else -> b.post(rb)
        }
        try {
            http.newCall(b.build()).execute().use { r ->
                val text = r.body.string()
                val obj = if (text.isBlank()) JSONObject() else runCatching { JSONObject(text) }.getOrElse { JSONObject() }
                if (r.code == 401) throw PcException.Unpaired()
                r.code to obj
            }
        } catch (e: PcException) {
            throw e
        } catch (e: IOException) {
            // A pin mismatch surfaces as an SSL error with our CertificateException somewhere below it; when
            // OkHttp then tries another address (e.g. IPv6) it's attached as a suppressed exception.
            throw if (hasCertificateFailure(e)) PcException.WrongPc() else PcException.Unreachable(e)
        }
    }

    private fun hasCertificateFailure(t: Throwable, depth: Int = 0): Boolean =
        depth < 8 && (t is CertificateException || t.suppressed.any { hasCertificateFailure(it, depth + 1) } ||
            (t.cause?.let { hasCertificateFailure(it, depth + 1) } ?: false))

    private fun JSONObject.message(fallback: String) = optString("message").ifEmpty { fallback }

    suspend fun pair(code: String, deviceName: String, deviceModel: String): PairResult {
        val (status, o) = call("POST", "/v1/pair", JSONObject().put("code", code).put("deviceName", deviceName).put("deviceModel", deviceModel))
        if (status != 200) throw PcException.Rejected(o.message("Pairing failed (HTTP $status)."))
        return PairResult(o.getString("deviceId"), o.getString("token"), PcStatusDto.from(o.getJSONObject("pc")))
    }

    suspend fun status(): PcStatusDto {
        val (status, o) = call("GET", "/v1/status")
        if (status != 200) throw PcException.Rejected(o.message("HTTP $status"))
        return PcStatusDto.from(o)
    }

    /** shutdown, restart or cancel. Returns the pending action (null after cancel). */
    suspend fun power(action: String): PcPending? {
        val (status, o) = call("POST", "/v1/power/$action")
        return when (status) {
            200, 202 -> o.optJSONObject("pending")?.let { PcPending(it.optString("action"), Instant.parse(it.optString("at"))) }
            409 -> throw PcException.Rejected(o.message("Already scheduled."))
            else -> throw PcException.Rejected(o.message("The PC refused (HTTP $status)."))
        }
    }

    suspend fun unpair() {
        call("DELETE", "/v1/device")
    }
}
