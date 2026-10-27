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
import net.odysseylabs.powerdeck.data.HUB_PHONE
import net.odysseylabs.powerdeck.data.HelperStatus
import net.odysseylabs.powerdeck.data.KvmLink
import net.odysseylabs.powerdeck.data.PowerDeck
import net.odysseylabs.powerdeck.data.SecretBox
import okhttp3.Dns
import okhttp3.OkHttpClient
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import java.net.InetAddress
import java.security.MessageDigest
import java.util.Base64
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.TimeUnit

/** KVMs move to a hub helper (pve here), actions go through it, and the phone acts itself when it's down. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [36])
class KvmHubTest {
    /** A helper: [routes] maps "METHOD /path" to (HTTP code, JSON body); the body is passed in. */
    private class FakeHelper(name: String, val routes: (String, String) -> Pair<Int, String>?) {
        val cert: HeldCertificate = HeldCertificate.Builder().commonName(name).addSubjectAlternativeName("localhost").build()
        val pin: String = Base64.getUrlEncoder().withoutPadding().encodeToString(MessageDigest.getInstance("SHA-256").digest(cert.certificate.encoded))
        val calls = CopyOnWriteArrayList<String>()
        val server = MockWebServer().apply {
            useHttps(HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory())
            dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse {
                    val body = request.body?.utf8().orEmpty()
                    val route = "${request.method} ${request.url.encodedPath}"
                    calls += "$route $body".trim()
                    val (code, out) = routes(route, body) ?: when (route) {
                        "GET /v2/hello" -> 200 to """{"app":"PowerDeck","name":"$name","apiVersion":2}"""
                        "GET /v2/status" -> 200 to """{"name":"$name","peerKeys":[]}"""
                        "GET /v2/peers" -> 200 to """{"peers":[]}"""
                        else -> 404 to """{"error":"not_found","message":"Not found."}"""
                    }
                    return MockResponse.Builder().code(code).addHeader("Content-Type", "application/json").body(out).build()
                }
            }
            start()
        }
    }

    /** A NanoKVM on plain HTTP whose power light comes on after a press. */
    private class FakeKvm {
        @Volatile var led = false
        val presses = CopyOnWriteArrayList<String>()
        val server = MockWebServer().apply {
            dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse {
                    val out = when ("${request.method} ${request.url.encodedPath}") {
                        "POST /api/auth/login" -> """{"code":0,"msg":"success","data":{"token":"t1"}}"""
                        "GET /api/vm/gpio" -> """{"code":0,"msg":"success","data":{"pwr":$led,"hdd":false}}"""
                        "POST /api/vm/gpio" -> { presses += request.body?.utf8().orEmpty(); led = true; """{"code":0,"msg":"success"}""" }
                        else -> """{"code":-1,"msg":"unknown"}"""
                    }
                    return MockResponse.Builder().addHeader("Content-Type", "application/json").body(out).build()
                }
            }
            start()
        }
        val address get() = "kvm.example.ts.net:${server.port}"
    }

    private val ctx = ApplicationProvider.getApplicationContext<android.content.Context>()
    private val store = DeviceStore(ctx, object : SecretBox {
        override fun seal(plain: String) = plain
        override fun open(sealed: String) = sealed
    })
    // Tailscale names reach the local fakes.
    private val http = OkHttpClient.Builder().connectTimeout(2, TimeUnit.SECONDS).readTimeout(10, TimeUnit.SECONDS)
        .dns { host -> if (host.endsWith(".ts.net")) listOf(InetAddress.getByName("127.0.0.1")) else Dns.SYSTEM.lookup(host) }.build()
    private val deck = PowerDeck(ctx, store, http) { true }

    private val hubReplies = mutableMapOf<String, Pair<Int, String>>()
    private val hub = FakeHelper("pve") { route, body ->
        hubReplies["$route ${JSONObject(body.ifEmpty { "{}" }).optBoolean("force")}"] ?: hubReplies[route]
    }
    private val gaming = FakeHelper("Gaming-PC") { _, _ -> null }
    private val kvm = FakeKvm()
    private lateinit var pve: Device
    private lateinit var pc: Device

    @Before fun setUp() = runBlocking {
        for (id in store.all().map { it.id }) store.remove(id)
        store.setKvmHub("")
        pve = store.get(store.upsert(Device(id = "pve", name = "pve", host = hub.server.hostName, port = hub.server.port, pin = hub.pin, os = "linux")))!!
        pc = store.get(store.upsert(Device(id = "pc", name = "Gaming-PC", host = gaming.server.hostName, port = gaming.server.port, pin = gaming.pin,
            os = "windows", mac = "02:00:5e:00:00:01", helperName = "Gaming-PC")))!!
        hubReplies["POST /v2/kvms"] = 201 to """{"kvm":{"name":"kvm","address":"http://${kvm.address}","user":"admin","machine":"gaming-pc",
            "wiring":{"led":"wired","power":"unknown","reset":"unknown"}},"led":true}"""
        hubReplies["DELETE /v2/kvms/kvm"] = 204 to ""
    }

    @After fun tearDown() {
        hub.server.close()
        gaming.server.close()
        kvm.server.close()
    }

    @Test fun linkingHandsTheKvmToTheHub() = runBlocking {
        kvm.led = true // Gaming-PC is on: its light agrees with its helper
        val o = deck.linkKvm(pc.id, "nanokvm", kvm.address, "kvm", "admin", "s3cret")
        assertTrue(o.message, o.ok && o.message.contains("pve drives it"))
        val add = JSONObject(hub.calls.single { it.startsWith("POST /v2/kvms") }.substringAfter("/v2/kvms "))
        assertEquals("Gaming-PC", add.getString("machine"))
        assertEquals("s3cret", add.getString("password"))
        assertTrue("tells the hub there's a helper to check", add.getBoolean("helper"))
        val k = store.get(pc.id)!!.kvm!!
        assertEquals("pve", k.hub)
        assertEquals("gaming-pc", k.machine)
        assertTrue(k.wiring!!.ledWired && k.ledSeen)
    }

    @Test fun anOldHubLeavesTheKvmOnThePhone() = runBlocking {
        kvm.led = true
        hubReplies.remove("POST /v2/kvms") // 404: a helper before 2.2
        val o = deck.linkKvm(pc.id, "nanokvm", kvm.address, "kvm", "admin", "s3cret")
        assertTrue(o.message, o.ok && o.message.contains("needs PowerDeck 2.2"))
        val k = store.get(pc.id)!!.kvm!!
        assertNull(k.hub)
        assertTrue(k.hubError!!.contains("2.2"))
    }

    private suspend fun onHub() {
        store.linkKvm(pc.id, KvmLink("nanokvm", kvm.address, "kvm", "admin", ledSeen = true, hub = "pve", machine = "gaming-pc"), "s3cret")
        pc = store.get(pc.id)!!
    }

    @Test fun resetAsksBeforeResettingAMachineThatAnswers() = runBlocking {
        onHub()
        hubReplies["POST /v2/machines/gaming-pc/reset false"] = 409 to
            """{"error":"helper_answers","message":"gaming-pc's helper still answers.","steps":[{"do":"helper answers"}]}"""
        hubReplies["POST /v2/machines/gaming-pc/reset true"] = 200 to
            """{"ok":true,"message":"Pressed reset on gaming-pc.","steps":[{"do":"read power light before","led":true},{"do":"press reset 800ms"}]}"""
        val first = deck.reset(pc)
        assertFalse(first.ok)
        assertEquals("reset", first.ask!!.hubAction)
        val forced = deck.reset(pc, force = true)
        assertTrue(forced.message, forced.ok && forced.message.startsWith("Pressed reset"))
        assertTrue("the phone never pressed", kvm.presses.isEmpty())
    }

    @Test fun turnOnGoesThroughTheHub() = runBlocking {
        onHub()
        gaming.server.close() // off
        hubReplies["POST /v2/machines/gaming-pc/wake"] = 200 to """{"ok":true,"message":"gaming-pc is starting.","steps":[]}"""
        val o = deck.turnOn(pc)
        assertTrue(o.message, o.ok && o.message == "gaming-pc is starting.")
        assertTrue(kvm.presses.isEmpty())
    }

    @Test fun thePhonePressesItselfWhenTheHubIsDown() = runBlocking {
        onHub()
        gaming.server.close()
        hub.server.close()
        val o = deck.turnOn(pc) // the light reads off (seen on before): press, then it's on
        assertTrue(o.message, o.ok && o.message.contains("starting"))
        assertEquals(1, kvm.presses.size)
        assertEquals(800, JSONObject(kvm.presses.single()).getInt("duration"))
    }

    @Test fun forceOffWhileAnsweringAsksFirst() = runBlocking {
        onHub()
        hubReplies["POST /v2/machines/gaming-pc/force-off false"] = 409 to """{"error":"helper_answers","message":"It answers."}"""
        val o = deck.forceOff(pc)
        assertEquals("force-off", o.ask!!.hubAction)
        val body = JSONObject(hub.calls.single { it.startsWith("POST /v2/machines/gaming-pc/force-off") }.substringAfter("force-off "))
        assertTrue("stays off", body.getBoolean("stayOff"))
    }

    @Test fun existingKvmsMoveToTheHubAndBack() = runBlocking {
        store.linkKvm(pc.id, KvmLink("nanokvm", kvm.address, "kvm", "admin", ledSeen = true), "s3cret") // linked before 2.2
        val states = mapOf(pve.id to DeviceState.On(HelperStatus.from(JSONObject("""{"name":"pve","os":"linux","peerKeys":[]}"""))))
        deck.linkHelpers(states)
        assertEquals("s3cret", JSONObject(hub.calls.single { it.startsWith("POST /v2/kvms") }.substringAfter("/v2/kvms ")).getString("password"))
        assertEquals("pve", store.get(pc.id)!!.kvm!!.hub)
        deck.linkHelpers(states)
        assertEquals("moved once", 1, hub.calls.count { it.startsWith("POST /v2/kvms") })

        store.setKvmHub(HUB_PHONE)
        deck.linkHelpers(states)
        assertTrue(hub.calls.any { it.startsWith("DELETE /v2/kvms/kvm") })
        assertNull(store.get(pc.id)!!.kvm!!.hub)
    }
}
