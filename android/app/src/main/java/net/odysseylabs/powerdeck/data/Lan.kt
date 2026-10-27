package net.odysseylabs.powerdeck.data

import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.math.BigInteger
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.Inet4Address
import java.net.InetAddress

/** Small LAN helpers: subnet overlap (to pick a waker) and the phone's own Wake-on-LAN. */
object Lan {
    data class Cidr(val base: BigInteger, val bits: Int, val v6: Boolean) {
        private val width get() = if (v6) 128 else 32
        fun network(prefix: Int): BigInteger = base.shiftRight(width - prefix).shiftLeft(width - prefix)
        fun contains(addr: InetAddress): Boolean {
            if ((addr is Inet4Address) == v6) return false
            val a = BigInteger(1, addr.address)
            return a.shiftRight(width - bits) == base.shiftRight(width - bits)
        }
        fun broadcast(): InetAddress? = if (v6) null else v4(base.or(BigInteger.ONE.shiftLeft(32 - bits).subtract(BigInteger.ONE)))
    }

    private fun v4(n: BigInteger): InetAddress = InetAddress.getByAddress(ByteArray(4) { i -> n.shiftRight(8 * (3 - i)).toInt().toByte() })

    fun parse(s: String): Cidr? = runCatching {
        val (ip, len) = s.split('/', limit = 2).let { it[0] to it.getOrNull(1) }
        if (!ip.contains(':') && !ip.matches(Regex("^[0-9.]+$"))) return null // never resolve names
        val a = InetAddress.getByName(ip)
        val v6 = a !is Inet4Address
        val bits = len?.toInt() ?: if (v6) 128 else 32
        require(bits in 0..(if (v6) 128 else 32))
        Cidr(BigInteger(1, a.address), bits, v6)
    }.getOrNull()

    /** Do two subnet lists share a subnet (either contains the other)? */
    fun overlaps(a: List<String>, b: List<String>): Boolean = a.mapNotNull(::parse).any { x ->
        b.mapNotNull(::parse).any { y ->
            x.v6 == y.v6 && minOf(x.bits, y.bits).let { p -> x.network(p) == y.network(p) }
        }
    }

    /** The phone's own subnets on Wi-Fi/Ethernet (not mobile data, not the VPN). */
    @Suppress("DEPRECATION")
    fun phoneSubnets(ctx: Context): List<String> {
        val cm = ctx.getSystemService(ConnectivityManager::class.java) ?: return emptyList()
        return cm.allNetworks.filter {
            val c = cm.getNetworkCapabilities(it) ?: return@filter false
            !c.hasTransport(NetworkCapabilities.TRANSPORT_VPN) &&
                (c.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) || c.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET))
        }.flatMap { n ->
            cm.getLinkProperties(n)?.linkAddresses.orEmpty().filter { it.address is Inet4Address }.map { la ->
                val c = Cidr(BigInteger(1, la.address.address), la.prefixLength, false)
                "${v4(c.network(la.prefixLength)).hostAddress}/${la.prefixLength}"
            }
        }
    }

    fun magicPacket(mac: String): ByteArray? {
        val m = mac.split(':', '-').takeIf { it.size == 6 }?.map { it.toIntOrNull(16)?.toByte() ?: return null } ?: return null
        return ByteArray(6) { 0xff.toByte() } + (1..16).flatMap { m }.toByteArray()
    }

    /** Sends the magic packet from the phone to each subnet broadcast and 255.255.255.255, 3 times. */
    suspend fun wake(mac: String, subnets: List<String>): Boolean = withContext(Dispatchers.IO) {
        val packet = magicPacket(mac) ?: return@withContext false
        val targets = subnets.mapNotNull { parse(it)?.broadcast() } + InetAddress.getByName("255.255.255.255")
        runCatching {
            DatagramSocket().use { s ->
                s.broadcast = true
                repeat(3) { for (t in targets) s.send(DatagramPacket(packet, packet.size, t, 9)) }
            }
            true
        }.getOrDefault(false)
    }
}
