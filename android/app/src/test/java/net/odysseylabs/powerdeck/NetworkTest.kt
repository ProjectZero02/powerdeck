package net.odysseylabs.powerdeck

import kotlinx.coroutines.runBlocking
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import net.odysseylabs.powerdeck.data.KvmException
import net.odysseylabs.powerdeck.data.NanoKvmClient
import net.odysseylabs.powerdeck.data.NanoKvmCrypto
import net.odysseylabs.powerdeck.data.AddLink
import net.odysseylabs.powerdeck.data.HelperClient
import net.odysseylabs.powerdeck.data.HelperException
import net.odysseylabs.powerdeck.data.Lan
import net.odysseylabs.powerdeck.data.Presence
import net.odysseylabs.powerdeck.data.Updater
import okhttp3.OkHttpClient
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import java.net.URLDecoder
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Base64

class NanoKvmCryptoTest {
    private class FixedSalt(private val salt: ByteArray) : SecureRandom() {
        override fun nextBytes(bytes: ByteArray) {
            salt.copyInto(bytes)
        }
    }

    private fun hex(s: String) = s.chunked(2).map { it.toInt(16).toByte() }.toByteArray()

    // Expected ciphertexts produced by: openssl enc -aes-256-cbc -md md5 -S <salt> -pass pass:nanokvm-sipeed-2024 -a
    @Test fun matchesOpenSsl() {
        for ((plain, salt, ct) in listOf(
            Triple("Tr0ub4dor&3 +/=?% pässwörd", "0102030405060708", "CdZkOzG0IEdxjqegpohcXAKLO9Uk5MAGDzuqnDy8mLQ="),
            Triple("admin", "a1b2c3d4e5f60718", "0dKkPa6381Bd25DXo2I8vQ=="),
        )) {
            val out = Base64.getDecoder().decode(URLDecoder.decode(NanoKvmCrypto.encryptPassword(plain, FixedSalt(hex(salt))), "UTF-8"))
            assertEquals("Salted__", String(out.copyOfRange(0, 8), Charsets.US_ASCII))
            assertEquals(salt, out.copyOfRange(8, 16).joinToString("") { "%02x".format(it) })
            assertEquals(ct, Base64.getEncoder().encodeToString(out.copyOfRange(16, out.size)))
        }
    }

    @Test fun isUriEncoded() {
        repeat(50) { assertFalse(NanoKvmCrypto.encryptPassword("password-$it").contains(Regex("[+/=]"))) }
    }
}

class AddLinkTest {
    private val fp = Base64.getUrlEncoder().withoutPadding().encodeToString(ByteArray(32) { it.toByte() })
    private val good = "powerdeck://add?c=7K3Q-M9XD&f=$fp&h=100.101.1.10&n=Gaming-PC&p=47321&v=2"

    @Test fun parsesEverything() {
        val p = AddLink.parse(good)!!
        assertEquals("Gaming-PC", p.name)
        assertEquals("100.101.1.10", p.host)
        assertEquals(47321, p.port)
        assertEquals(fp, p.pin)
        assertEquals("7K3Q-M9XD", p.code)
        assertNull(AddLink.parse(good.replace("c=7K3Q-M9XD&", ""))!!.code)
    }

    @Test fun rejectsBadInput() {
        for (bad in listOf(
            "https://add?v=2", good.replace("v=2", "v=1"), good.replace("f=$fp", "f=AAAA"), good.replace("p=47321", "p=99999"),
            good.replace("powerdeck://add", "powerdeck://pair"), good.replace("h=100.101.1.10", "h=a%20b"), "not a link", "",
        )) assertNull(bad, AddLink.parse(bad))
    }

    @Test fun normalizesCodesLikeTheHelper() {
        assertEquals("7K3Q-M9XD", AddLink.normalizeCode("7k3q m9xd"))
        assertEquals("0111-ABCD", AddLink.normalizeCode("oIL1abcd"))
        assertNull(AddLink.normalizeCode("7K3Q-M9X"))
        assertNull(AddLink.normalizeCode("UUUU-UUUU")) // U isn't Crockford
    }

    @Test fun cleansTypedNames() {
        assertEquals("workstation", AddLink.cleanHost(" workstation "))
        assertEquals("pve.tail1234.ts.net", AddLink.cleanHost("https://pve.tail1234.ts.net:47321/"))
        assertNull(AddLink.cleanHost("a b"))
    }
}

class LanTest {
    @Test fun overlap() {
        assertTrue(Lan.overlaps(listOf("192.168.1.0/24"), listOf("192.168.1.0/24")))
        assertTrue(Lan.overlaps(listOf("10.0.0.0/8"), listOf("10.1.2.0/24")))
        assertFalse(Lan.overlaps(listOf("192.168.1.0/24"), listOf("192.168.2.0/24")))
        assertFalse(Lan.overlaps(listOf("pve.local/24"), listOf("192.168.1.0/24")))
        assertEquals("192.168.1.255", Lan.parse("192.168.1.0/24")!!.broadcast()!!.hostAddress)
    }

    @Test fun magicPacket() {
        val p = Lan.magicPacket("d8:5e:d3:12:34:56")!!
        assertEquals(102, p.size)
        assertEquals(0xd8.toByte(), p[6])
        assertNull(Lan.magicPacket("nope"))
    }
}

class HelperClientTest {
    private val server = MockWebServer()
    private val cert = HeldCertificate.Builder().commonName("PowerDeck test").addSubjectAlternativeName("localhost").build()
    private val pin = Base64.getUrlEncoder().withoutPadding().encodeToString(MessageDigest.getInstance("SHA-256").digest(cert.certificate.encoded))

    init {
        server.useHttps(HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory())
        server.start()
    }

    @After fun tearDown() = server.close()

    private fun client(fp: String? = pin, key: String? = null, seen: (String) -> Unit = {}) = HelperClient(OkHttpClient(), server.hostName, server.port, fp, key, seen)

    private fun json(code: Int, body: String) = MockResponse.Builder().code(code).addHeader("Content-Type", "application/json").body(body).build()

    private val status = """{"name":"Gaming-PC","os":"windows","version":"2.0.0","apiVersion":2,"uptimeSeconds":42,
        "presence":{"active":true,"user":"Alex","idleSeconds":30,"fullscreen":true},
        "network":{"mac":"d8:5e:d3:12:34:56","lan":["192.168.1.0/24"],"wired":true},
        "checks":[{"id":"fast_startup","level":"warn","title":"Fast Startup is on","fixable":true}],
        "you":{"kind":"owner","name":"Pixel","scopes":["status"]}}"""

    @Test fun readsStatus() = runBlocking {
        server.enqueue(json(200, status))
        val s = client().status()
        assertEquals("Gaming-PC", s.name)
        assertEquals("d8:5e:d3:12:34:56", s.mac)
        assertEquals(listOf("192.168.1.0/24"), s.lan)
        assertTrue(s.presence!!.active)
        assertTrue(s.checks.single().problem)
        assertEquals("Pixel", s.youName)
        assertNull(server.takeRequest().headers["Authorization"])
    }

    @Test fun trustsOnFirstUseAndReportsThePin() = runBlocking {
        server.enqueue(json(200, status))
        var seen: String? = null
        client(fp = null) { seen = it }.status()
        assertEquals(pin, seen)
    }

    @Test fun rejectsAnotherCertificate() {
        val otherPin = Base64.getUrlEncoder().withoutPadding().encodeToString(ByteArray(32))
        assertThrows(HelperException.WrongDevice::class.java) { runBlocking { client(fp = otherPin).status() } }
    }

    @Test fun notOwnerThenPair() = runBlocking {
        server.enqueue(json(401, """{"error":"not_owner","message":"This device belongs to another Tailscale account."}"""))
        assertThrows(HelperException.NotOwner::class.java) { runBlocking { client().status() } }
        server.enqueue(json(200, """{"key":"pdk_x","status":$status}"""))
        val (key, s) = client().pair("7K3Q-M9XD", "Phone")
        assertEquals("pdk_x", key)
        assertEquals("Gaming-PC", s.name)
        server.takeRequest()
        val req = server.takeRequest()
        assertEquals("/v2/pair", req.url.encodedPath)
        assertEquals("7K3Q-M9XD", JSONObject(req.body!!.utf8()).getString("code"))
    }

    @Test fun sendsKeyAndParsesPending() = runBlocking {
        server.enqueue(json(202, """{"pending":{"action":"shutdown","at":"2026-10-06T12:01:00Z","by":"Phone"}}"""))
        val p = client(key = "pdk_k").power("shutdown")
        assertEquals("shutdown", p!!.action)
        val req = server.takeRequest()
        assertEquals("Bearer pdk_k", req.headers["Authorization"])
        val body = JSONObject(req.body!!.utf8())
        assertEquals(60, body.getInt("delaySeconds"))
        assertFalse(body.getBoolean("force"))
    }

    @Test fun inUseCarriesPresence() {
        server.enqueue(json(409, """{"error":"in_use","message":"Someone is using this device.","presence":{"active":true,"user":"Alex","idleSeconds":5}}"""))
        val e = assertThrows(HelperException.Api::class.java) { runBlocking { client().power("restart") } }
        assertEquals("in_use", e.code)
        assertEquals("Alex", Presence.from(e.body.optJSONObject("presence"))!!.user)
    }

    @Test fun peers() = runBlocking {
        server.enqueue(json(200, """{"checkedAt":"2026-10-06T12:00:00Z","peers":[{"kind":"nanokvm","name":"kvm","address":"100.101.1.20","online":true},
            {"kind":"helper","name":"pve","address":"100.101.1.30","port":47321,"os":"linux","pin":"$pin"}]}"""))
        val peers = client().peers(refresh = true)
        assertTrue(peers[0].isKvm)
        assertEquals(pin, peers[1].pin)
        val req = server.takeRequest()
        assertEquals("/v2/peers", req.url.encodedPath)
        assertEquals("1", req.url.queryParameter("refresh"))
    }

    @Test fun offDeviceIsUnreachable() {
        server.close()
        assertThrows(HelperException.Unreachable::class.java) { runBlocking { client().status() } }
    }
}

class NanoKvmClientTest {
    private val server = MockWebServer().apply { start() }

    @After fun tearDown() = server.close()

    private fun client(pw: String = "pw") = NanoKvmClient(OkHttpClient(), "${server.hostName}:${server.port}", "admin", pw)
    private fun ok(data: String = "null", cookie: String? = null) = MockResponse.Builder().body("""{"code":0,"msg":"success","data":$data}""")
        .apply { if (cookie != null) addHeader("Set-Cookie", cookie) }.build()

    @Test fun loginLedPressAndWake() = runBlocking {
        server.enqueue(ok("""{"token":"jwt1"}"""))
        server.enqueue(ok("""{"pwr":false,"hdd":false}"""))
        server.enqueue(ok())
        server.enqueue(ok())
        val kvm = client()
        val cookie = kvm.login()
        assertEquals("nano-kvm-token=jwt1", cookie)
        assertFalse(kvm.powerLedOn(cookie))
        kvm.pressPower(cookie)
        kvm.wake(cookie, "d8:5e:d3:12:34:56")
        val login = server.takeRequest()
        val sent = JSONObject(login.body!!.utf8())
        assertEquals("admin", sent.getString("username"))
        assertTrue(URLDecoder.decode(sent.getString("password"), "UTF-8").startsWith("U2FsdGVkX1")) // base64 of "Salted__"
        assertEquals("nano-kvm-token=jwt1", server.takeRequest().headers["Cookie"])
        val press = JSONObject(server.takeRequest().body!!.utf8())
        assertEquals("power", press.getString("type"))
        assertEquals(800, press.getInt("duration"))
        assertEquals("d8:5e:d3:12:34:56", JSONObject(server.takeRequest().body!!.utf8()).getString("mac"))
    }

    @Test fun missingLedReadingIsNotOff() {
        // "Off" would allow a press, and a press shuts a running computer down: no reading must throw.
        server.enqueue(ok("""{"hdd":false}"""))
        assertThrows(KvmException::class.java) { runBlocking { client().powerLedOn("c") } }
        server.enqueue(ok())
        assertThrows(KvmException::class.java) { runBlocking { client().powerLedOn("c") } }
    }

    @Test fun newerFirmwareCookieOnly() = runBlocking {
        server.enqueue(ok(cookie = "nano-kvm-token=jwt2; Path=/; HttpOnly; SameSite=Strict"))
        assertEquals("nano-kvm-token=jwt2", client().login())
    }

    @Test fun wrongPasswordAndLockout() {
        server.enqueue(MockResponse.Builder().body("""{"code":-2,"msg":"invalid username or password"}""").build())
        assertThrows(KvmException.BadPassword::class.java) { runBlocking { client().login() } }
        server.enqueue(MockResponse.Builder().body("""{"code":-5,"msg":"locked"}""").build())
        assertThrows(KvmException.Locked::class.java) { runBlocking { client().login() } }
    }

    @Test fun unreachable() {
        server.close()
        assertThrows(KvmException.Unreachable::class.java) { runBlocking { client().login() } }
    }
}

class UpdaterTest {
    @Test fun versionOrder() {
        assertTrue(Updater.newer("1.0.1", "1.0.0"))
        assertTrue(Updater.newer("1.10.0", "1.9.9"))
        assertTrue(Updater.newer("v2.0.0", "1.99.0"))
        assertFalse(Updater.newer("1.0.0", "1.0.0"))
        assertFalse(Updater.newer("1.0.0", "1.0.1"))
        assertNotNull(Updater.APK)
    }
}
