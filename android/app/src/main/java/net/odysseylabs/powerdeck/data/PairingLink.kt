package net.odysseylabs.powerdeck.data

import java.net.URI
import java.net.URLDecoder
import java.util.Base64

/** Everything the PC's pairing QR code carries (see docs/protocol.md). */
data class PairingInfo(
    val name: String,
    val host: String,
    val port: Int,
    val fingerprint: String,
    val code: String,
    val mac: String?,
    val nanoKvm: String?,
    val proxmox: String?,
)

object PairingLink {
    private val codeRe = Regex("^[a-z2-7]{26}$")
    private val macRe = Regex("^[0-9a-f]{2}(:[0-9a-f]{2}){5}$")
    private val hostRe = Regex("^[A-Za-z0-9.:\\-\\[\\]]{1,253}$")

    /** Parses `powerdeck://pair?...`; returns null for anything that isn't a valid v1 pairing link. */
    fun parse(text: String): PairingInfo? {
        val uri = runCatching { URI(text.trim()) }.getOrNull() ?: return null
        if (!uri.scheme.equals("powerdeck", true) || !uri.host.equals("pair", true)) return null
        val q = (uri.rawQuery ?: return null).split('&').mapNotNull {
            val i = it.indexOf('=')
            if (i <= 0) null else it.substring(0, i) to URLDecoder.decode(it.substring(i + 1), "UTF-8")
        }.toMap()
        if (q["v"] != "1") return null
        val host = q["h"]?.takeIf { hostRe.matches(it) } ?: return null
        val port = q["p"]?.toIntOrNull()?.takeIf { it in 1..65535 } ?: return null
        val fp = q["f"]?.takeIf { runCatching { Base64.getUrlDecoder().decode(it).size == 32 }.getOrDefault(false) } ?: return null
        val code = q["c"]?.lowercase()?.takeIf { codeRe.matches(it) } ?: return null
        return PairingInfo(
            name = q["n"]?.take(64)?.ifBlank { null } ?: "PC",
            host = host,
            port = port,
            fingerprint = fp,
            code = code,
            mac = q["m"]?.lowercase()?.takeIf { macRe.matches(it) },
            nanoKvm = q["k"]?.takeIf { hostRe.matches(it) },
            proxmox = q["x"]?.takeIf { hostRe.matches(it) },
        )
    }
}
