package net.odysseylabs.powerdeck.data

import java.net.URI
import java.net.URLDecoder
import java.util.Base64

/** What a helper's QR code carries: `powerdeck://add?v=2&n=&h=&p=&f=[&c=]` (see docs/protocol.md). */
data class AddInfo(val name: String, val host: String, val port: Int, val pin: String, val code: String?)

object AddLink {
    private val hostRe = Regex("^[A-Za-z0-9.:\\-]{1,253}$")
    private val codeRe = Regex("^[0-9A-HJKMNP-TV-Z]{8}$")

    /** Parses an add link; null for anything that isn't a valid v2 link. */
    fun parse(text: String): AddInfo? {
        val uri = runCatching { URI(text.trim()) }.getOrNull() ?: return null
        if (!uri.scheme.equals("powerdeck", true) || !uri.host.equals("add", true)) return null
        val q = (uri.rawQuery ?: return null).split('&').mapNotNull {
            val i = it.indexOf('=')
            if (i <= 0) null else it.substring(0, i) to runCatching { URLDecoder.decode(it.substring(i + 1), "UTF-8") }.getOrDefault("")
        }.toMap()
        if (q["v"] != "2") return null
        val host = q["h"]?.takeIf { hostRe.matches(it) } ?: return null
        val port = q["p"]?.toIntOrNull()?.takeIf { it in 1..65535 } ?: return null
        val pin = q["f"]?.takeIf(::validPin) ?: return null
        return AddInfo(
            name = q["n"]?.trim()?.take(64)?.ifBlank { null } ?: host,
            host = host,
            port = port,
            pin = pin,
            code = q["c"]?.let(::normalizeCode),
        )
    }

    fun validPin(pin: String) = runCatching { Base64.getUrlDecoder().decode(pin).size == 32 }.getOrDefault(false)

    /** Same rules as the helper: case, spaces and dashes don't matter; O reads as 0, I and L as 1. */
    fun normalizeCode(s: String): String? {
        val v = s.uppercase().filter { it != ' ' && it != '-' }.map {
            when (it) {
                'O' -> '0'
                'I', 'L' -> '1'
                else -> it
            }
        }.joinToString("")
        return if (codeRe.matches(v)) v.substring(0, 4) + "-" + v.substring(4) else null
    }

    /** A typed Tailscale name or address, e.g. `za-worldo` or `100.101.1.10`. */
    fun cleanHost(s: String): String? = s.trim().removePrefix("https://").removePrefix("http://").substringBefore('/')
        .substringBefore(":47321").trimEnd('.').takeIf { it.isNotEmpty() && hostRe.matches(it) }
}
