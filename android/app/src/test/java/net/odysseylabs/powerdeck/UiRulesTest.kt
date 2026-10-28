package net.odysseylabs.powerdeck

import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.DeviceState
import net.odysseylabs.powerdeck.data.Event
import net.odysseylabs.powerdeck.data.HelperStatus
import net.odysseylabs.powerdeck.data.HubMachine
import com.google.zxing.BarcodeFormat
import com.google.zxing.qrcode.QRCodeReader
import com.google.zxing.qrcode.QRCodeWriter
import net.odysseylabs.powerdeck.ui.Tone
import net.odysseylabs.powerdeck.ui.agentKeysFor
import net.odysseylabs.powerdeck.ui.decodeLuma
import net.odysseylabs.powerdeck.ui.describe
import net.odysseylabs.powerdeck.ui.hubNote
import net.odysseylabs.powerdeck.ui.uninstallBlocker
import net.odysseylabs.powerdeck.work.EventsWorker
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant

class UiRulesTest {
    private fun status(json: String) = HelperStatus.from(JSONObject(json))
    private val pc = Device("pc", "My PC", "100.64.0.1", pin = "x", os = "windows")

    @Test fun parsesTheHubsVerdict() {
        val m = HubMachine.from(JSONObject("""{"name":"workstation","state":"hung","led":true,"since":"2026-10-28T08:00:30Z",
            "evidence":{"at":"2026-10-28T08:03:30Z","helper":"silent","port":"accepts","ping":"no_reply","led":"on","ledTrusted":true,"tailnetOnline":true},
            "kvm":{"name":"kvm-2","wiring":{"led":"wired","power":"wired","reset":"unknown"}},"actions":["reset","force-off"]}"""))
        assertTrue(m.hung)
        assertEquals(Instant.parse("2026-10-28T08:00:30Z"), m.since)
        val e = m.evidence!!
        assertEquals(listOf("silent", "accepts", "no_reply", "on"), listOf(e.helper, e.port, e.ping, e.led))
        assertTrue(e.ledTrusted)
        assertEquals(Instant.parse("2026-10-28T08:03:30Z"), e.at)

        val now = Instant.parse("2026-10-28T08:05:00Z").toEpochMilli()
        val (text, tone) = describe(DeviceState.On(null, m), now)
        assertTrue(text, text.startsWith("Frozen since "))
        assertEquals(Tone.Warn, tone)
        assertEquals("On" to Tone.On, describe(DeviceState.On(null), now))

        // A hub before 2.3 has no watch: nothing to add.
        val old = HubMachine.from(JSONObject("""{"name":"workstation","state":"unknown","led":true}"""))
        assertNull(old.reason)
        assertNull(old.since)
        assertNull(old.evidence)
        assertNull(hubNote(old, now))
        assertEquals("Installing updates" to Tone.Busy, hubNote(old.copy(reason = "updating"), now))
        assertEquals("May be asleep" to Tone.Unknown, hubNote(old.copy(reason = "may_be_asleep"), now))
        assertNull(hubNote(old.copy(reason = "led_not_trusted"), now))
    }

    /** The hub's log lines about a frozen machine are notified, whoever wrote them. */
    @Test fun frozenMachinesAreNotified() {
        val at = Instant.parse("2026-10-28T08:03:30Z")
        assertTrue(EventsWorker.hubWatch(Event(at, "pve", "Workstation looks frozen: nothing answers, power light on", "detected")))
        assertTrue(EventsWorker.hubWatch(Event(at, "pve", "Workstation answers again", "recovered")))
        assertFalse(EventsWorker.hubWatch(Event(at, "Pixel 9 Pro", "shutdown Workstation", "ok")))
        assertFalse(EventsWorker.hubWatch(Event(at, "Atlas", "Workstation looks frozen", "ok")))
    }

    @Test fun parsesAgentsAndCaps() {
        val s = status("""{"name":"pc","caps":["shutdown","restart","uninstall"],"agents":{"allowed":false,"keys":[{"name":"Atlas","devices":["My PC"]}]}}""")
        assertEquals(listOf("shutdown", "restart", "uninstall"), s.caps)
        assertEquals(false, s.agents?.allowed)
        assertEquals("Atlas", s.agents?.keys?.single()?.name)
        assertNull(status("""{"name":"pc"}""").agents) // not an owner, or an older helper
    }

    @Test fun agentKeysCoverListedOrAllDevices() {
        val hub = DeviceState.On(status("""{"name":"hub","agents":{"allowed":true,"keys":[{"name":"Atlas","devices":["My PC"]},{"name":"Any","devices":[]},{"name":"Other","devices":["nas"]}]}}"""))
        assertEquals(listOf("Atlas", "Any"), agentKeysFor(pc, mapOf("hub" to hub, "off" to DeviceState.Off)))
        assertTrue(agentKeysFor(pc, mapOf("off" to DeviceState.Off)).isEmpty())
    }

    @Test fun uninstallNeedsARunningNewHelper() {
        assertNotNull(uninstallBlocker(pc, DeviceState.Off))
        assertNotNull(uninstallBlocker(pc, DeviceState.On(status("""{"name":"pc","caps":["shutdown"]}"""))))
        assertNull(uninstallBlocker(pc, DeviceState.On(status("""{"name":"pc","caps":["shutdown","uninstall"]}"""))))
    }

    @Test fun scannerReadsAQrCode() {
        val link = "powerdeck://add?v=2&host=my-pc.tail1234.ts.net&port=47321&name=My%20PC&pin=abc"
        val size = 400
        val m = QRCodeWriter().encode(link, BarcodeFormat.QR_CODE, size, size)
        val luma = ByteArray(size * size) { i -> if (m.get(i % size, i / size)) 0x20 else 0xE0.toByte() }
        assertEquals(link, decodeLuma(QRCodeReader(), luma, size, size))
        assertNull(decodeLuma(QRCodeReader(), ByteArray(size * size) { 0x80.toByte() }, size, size))
    }
}
