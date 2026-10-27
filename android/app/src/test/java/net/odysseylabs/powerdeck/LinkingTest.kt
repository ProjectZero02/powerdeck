package net.odysseylabs.powerdeck

import androidx.test.core.app.ApplicationProvider
import kotlinx.coroutines.runBlocking
import mockwebserver3.Dispatcher
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import mockwebserver3.RecordedRequest
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.DeviceState
import net.odysseylabs.powerdeck.data.DeviceStore
import net.odysseylabs.powerdeck.data.KvmLink
import net.odysseylabs.powerdeck.data.PowerDeck
import net.odysseylabs.powerdeck.data.SecretBox
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import java.security.MessageDigest
import java.util.Base64
import java.util.concurrent.CopyOnWriteArrayList

/** The app claims upgraded helpers, gives the owner's helpers peer keys for each other, and renames KVMs to Tailscale names. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [36])
class LinkingTest {
    private class FakeHelper(val name: String, status: String, peers: String, keyFor: String = "pdk_from_$name") {
        val cert: HeldCertificate = HeldCertificate.Builder().commonName(name).addSubjectAlternativeName("localhost").build()
        val pin: String = Base64.getUrlEncoder().withoutPadding().encodeToString(MessageDigest.getInstance("SHA-256").digest(cert.certificate.encoded))
        val calls = CopyOnWriteArrayList<String>()
        val server = MockWebServer().apply {
            useHttps(HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory())
            dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse {
                    val body = request.body?.utf8().orEmpty()
                    calls += "${request.method} ${request.url.encodedPath} $body".trim()
                    val out = when ("${request.method} ${request.url.encodedPath}") {
                        "GET /v2/status" -> status
                        "GET /v2/peers" -> """{"peers":[$peers]}"""
                        "POST /v2/keys" -> """{"id":"k1","key":"$keyFor"}"""
                        else -> null
                    }
                    return if (out == null) MockResponse.Builder().code(204).build()
                    else MockResponse.Builder().addHeader("Content-Type", "application/json").body(out).build()
                }
            }
            start()
        }
    }

    private val ctx = ApplicationProvider.getApplicationContext<android.content.Context>()
    private val store = DeviceStore(ctx, object : SecretBox {
        override fun seal(plain: String) = plain
        override fun open(sealed: String) = sealed
    })

    private val gaming = FakeHelper("Gaming-PC", """{"name":"Gaming-PC","owners":{"legacy":false,"you":true},"peerKeys":["pve"]}""", "")
    private val pve = FakeHelper("pve", """{"name":"pve","owners":{"legacy":true,"you":false},"peerKeys":[]}""",
        """{"kind":"helper","name":"gaming-pc","address":"100.101.1.10","port":47321,"pin":"${gaming.pin}"},
           {"kind":"nanokvm","name":"kvm","dnsName":"kvm.example.ts.net.","address":"100.101.1.20"}""")

    @After fun tearDown() {
        pve.server.close()
        gaming.server.close()
    }

    @Test fun claimsLinksAndRenames() = runBlocking {
        for (id in store.all().map { it.id }) store.remove(id)
        val deck = PowerDeck(ctx, store)
        val a = store.get(store.upsert(Device(id = "a", name = "pve", host = pve.server.hostName, port = pve.server.port, pin = pve.pin)))!!
        val b = store.get(store.upsert(Device(id = "b", name = "Gaming-PC", host = gaming.server.hostName, port = gaming.server.port, pin = gaming.pin)))!!
        val c = store.upsert(Device(id = "c", name = "NAS", host = null, pin = null, kvm = KvmLink("nanokvm", "100.101.1.20", "kvm", "admin", ledSeen = true)))

        val states = mapOf(a.id to deck.state(a), b.id to deck.state(b))
        assertTrue(states.values.all { it is DeviceState.On })
        assertTrue("claims the upgraded helper", pve.calls.any { it.startsWith("POST /v2/owners/claim") })
        assertTrue("doesn't claim one that has owners", gaming.calls.none { it.contains("/v2/owners") })

        deck.linkHelpers(states)
        // pve needs a key for Gaming-PC: Gaming-PC issues it, pve stores it under its own name for it.
        val add = gaming.calls.single { it.startsWith("POST /v2/keys") }
        val req = JSONObject(add.substringAfter("/v2/keys "))
        assertEquals("peer", req.getString("kind"))
        assertEquals("pve", req.getString("name"))
        val put = pve.calls.single { it.startsWith("PUT /v2/peers/") }
        assertTrue(put, put.startsWith("PUT /v2/peers/gaming-pc/key "))
        assertEquals("pdk_from_Gaming-PC", JSONObject(put.substringAfter("/key ")).getString("key"))
        // Gaming-PC already holds a key for pve.
        assertTrue(pve.calls.none { it.startsWith("POST /v2/keys") })
        assertEquals("kvm.example.ts.net", store.get(c)!!.kvm!!.address)

        deck.linkHelpers(states) // tried once per run
        assertEquals(1, gaming.calls.count { it.startsWith("POST /v2/keys") })
    }
}
