package net.odysseylabs.powerdeck

import kotlinx.coroutines.runBlocking
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import net.odysseylabs.powerdeck.data.KvmException
import net.odysseylabs.powerdeck.data.NanoKvmClient
import net.odysseylabs.powerdeck.data.NanoKvmCrypto
import net.odysseylabs.powerdeck.data.PairingLink
import net.odysseylabs.powerdeck.data.PcClient
import net.odysseylabs.powerdeck.data.PcException
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

class PairingLinkTest {
    private val fp = Base64.getUrlEncoder().withoutPadding().encodeToString(ByteArray(32) { it.toByte() })
    private val good = "powerdeck://pair?c=abcdefghijklmnopqrstuvwxyz&f=$fp&h=100.101.1.10&k=100.101.1.20" +
        "&m=d8%3A5e%3Ad3%3A12%3A34%3A56&n=Gaming-PC&p=47321&v=1&x=100.101.1.30%3A8006"

    @Test fun parsesEverything() {
        val p = PairingLink.parse(good)!!
        assertEquals("Gaming-PC", p.name)
        assertEquals("100.101.1.10", p.host)
        assertEquals(47321, p.port)
        assertEquals(fp, p.fingerprint)
        assertEquals("abcdefghijklmnopqrstuvwxyz", p.code)
        assertEquals("d8:5e:d3:12:34:56", p.mac)
        assertEquals("100.101.1.20", p.nanoKvm)
        assertEquals("100.101.1.30:8006", p.proxmox)
    }

    @Test fun rejectsBadInput() {
        for (bad in listOf(
            "https://pair?v=1", good.replace("v=1", "v=2"), good.replace("c=abcdefghijklmnopqrstuvwxyz", "c=short"),
            good.replace("f=$fp", "f=AAAA"), good.replace("p=47321", "p=99999"), good.replace("powerdeck://pair", "powerdeck://other"),
            "not a link", "",
        )) assertNull(bad, PairingLink.parse(bad))
    }

    @Test fun dropsInvalidOptionalFields() {
        val p = PairingLink.parse(good.replace("m=d8%3A5e%3Ad3%3A12%3A34%3A56", "m=zz"))!!
        assertNull(p.mac)
    }
}

class PcClientTest {
    private val server = MockWebServer()
    private val cert = HeldCertificate.Builder().commonName("PowerDeck test").addSubjectAlternativeName("localhost").build()
    private val pin = Base64.getUrlEncoder().withoutPadding().encodeToString(MessageDigest.getInstance("SHA-256").digest(cert.certificate.encoded))

    init {
        server.useHttps(HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory())
        server.start()
    }

    @After fun tearDown() = server.close()

    private fun client(fp: String = pin, token: String? = "tok") = PcClient(OkHttpClient(), server.hostName, server.port, fp, token)

    private fun json(code: Int, body: String) = MockResponse.Builder().code(code).addHeader("Content-Type", "application/json").body(body).build()

    @Test fun pairsAndReadsContext() = runBlocking {
        server.enqueue(json(200, """{"deviceId":"d1","token":"secret","pc":{"name":"Gaming-PC","version":"1.0.0","uptimeSeconds":42,
            "pending":null,"mac":"d8:5e:d3:12:34:56","nanokvm":{"address":"100.101.1.20"},"proxmox":{"address":"100.101.1.30:8006"}}}"""))
        val r = client(token = null).pair("abc", "Phone", "Model")
        assertEquals("secret", r.token)
        assertEquals("100.101.1.20", r.pc.nanoKvm)
        val req = server.takeRequest()
        assertEquals("/v1/pair", req.url.encodedPath)
        assertNull(req.headers["Authorization"])
        assertEquals("abc", JSONObject(req.body!!.utf8()).getString("code"))
    }

    @Test fun rejectsAnotherCertificate() {
        val otherPin = Base64.getUrlEncoder().withoutPadding().encodeToString(ByteArray(32))
        assertThrows(PcException.WrongPc::class.java) { runBlocking { client(fp = otherPin).status() } }
    }

    @Test fun sendsTokenAndParsesPending() = runBlocking {
        server.enqueue(json(202, """{"pending":{"action":"shutdown","at":"2026-10-06T12:00:30Z"}}"""))
        val p = client().power("shutdown")
        assertEquals("shutdown", p!!.action)
        assertEquals("Bearer tok", server.takeRequest().headers["Authorization"])
    }

    @Test fun unauthorizedMeansUnpaired() {
        server.enqueue(json(401, """{"error":"unauthorized"}"""))
        assertThrows(PcException.Unpaired::class.java) { runBlocking { client().status() } }
    }

    @Test fun conflictIsReported() {
        server.enqueue(json(409, """{"error":"pending","message":"A shutdown or restart is already scheduled."}"""))
        val e = assertThrows(PcException.Rejected::class.java) { runBlocking { client().power("restart") } }
        assertEquals("A shutdown or restart is already scheduled.", e.message)
    }

    @Test fun offPcIsUnreachable() {
        server.close()
        assertThrows(PcException.Unreachable::class.java) { runBlocking { client().status() } }
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
