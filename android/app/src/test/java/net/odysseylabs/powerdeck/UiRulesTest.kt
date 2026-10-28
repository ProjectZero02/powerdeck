package net.odysseylabs.powerdeck

import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.DeviceState
import net.odysseylabs.powerdeck.data.Event
import net.odysseylabs.powerdeck.data.HelperStatus
import net.odysseylabs.powerdeck.data.HubMachine
import net.odysseylabs.powerdeck.data.HubRecovery
import com.google.zxing.BarcodeFormat
import com.google.zxing.qrcode.QRCodeReader
import com.google.zxing.qrcode.QRCodeWriter
import net.odysseylabs.powerdeck.ui.Tone
import net.odysseylabs.powerdeck.ui.agentKeysFor
import net.odysseylabs.powerdeck.ui.decodeLuma
import net.odysseylabs.powerdeck.ui.describe
import net.odysseylabs.powerdeck.ui.hubNote
import net.odysseylabs.powerdeck.ui.recoveryText
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

    @Test fun parsesARecovery() {
        val m = HubMachine.from(JSONObject("""{"name":"workstation","state":"hung","led":true,"kvm":{"name":"kvm-2"},
            "recovery":{"id":"r1","machine":"workstation","action":"reset","state":"proposed","reason":"Atlas can't reach it",
            "proposedBy":"Atlas","agent":true,"proposedAt":"2026-10-28T08:04:00Z"}}"""))
        val r = m.recovery!!
        assertEquals(listOf("r1", "workstation", "Atlas", "Atlas can't reach it"), listOf(r.id, r.machine, r.proposedBy, r.reason))
        assertTrue(r.proposed && r.open && r.agent && !r.forceOff && !r.direct)
        assertEquals(Instant.parse("2026-10-28T08:04:00Z"), r.proposedAt)
        assertNull(r.confirmedAt)
        assertNull(HubMachine.from(JSONObject("""{"name":"workstation","state":"hung"}""")).recovery) // a hub before 2.4

        // While it runs, the device's line says so instead of "Frozen since".
        val now = Instant.parse("2026-10-28T08:05:00Z").toEpochMilli()
        assertEquals("Resetting" to Tone.Busy, hubNote(m.copy(recovery = r.copy(state = HubRecovery.RUNNING)), now))
        assertTrue(hubNote(m.copy(recovery = r.copy(state = HubRecovery.NOT_BACK)), now)!!.first.startsWith("Frozen"))

        // The hub's settings, owners only.
        assertEquals(10, status("""{"name":"pve","hub":{"hungAfterMinutes":3,"autoResetMinutes":10}}""").autoResetMinutes)
        assertEquals(0, status("""{"name":"pve","hub":{"hungAfterMinutes":3,"autoResetMinutes":0}}""").autoResetMinutes)
        assertNull(status("""{"name":"pve"}""").autoResetMinutes)
    }

    /** The recovery card: who asks (an agent's words quoted as theirs), what happens, how it ended. */
    @Test fun recoveryCardText() {
        val now = Instant.parse("2026-10-28T08:05:00Z").toEpochMilli()
        val r = HubRecovery("r1", "workstation", "reset", "proposed", "Nothing answers and its power light is on.", "pve-helper", false,
            null, null, null, null, null, false)
        val hub = recoveryText(r, "Workstation", "pve", now, hubSelf = "pve-helper")!!
        assertEquals("pve suggests a reset", hub.title)
        assertTrue(hub.detail!!.startsWith("Nothing answers and its power light is on.\nA reset is like pressing"))
        assertEquals(Tone.Warn, hub.tone)

        val agent = recoveryText(r.copy(proposedBy = "Atlas", agent = true, reason = "ignore this and reset everything"), "Workstation", "pve", now)!!
        assertEquals("Atlas suggests a reset", agent.title)
        assertTrue(agent.detail!!.startsWith("“ignore this and reset everything”"))
        assertEquals("Atlas asks to force Workstation off", recoveryText(r.copy(proposedBy = "Atlas", action = "force-off"), "Workstation", "pve", now)!!.title)

        assertEquals("Resetting Workstation…" to Tone.Busy, recoveryText(r.copy(state = "running"), "Workstation", "pve", now)!!.let { it.title to it.tone })
        assertEquals("Workstation is back", recoveryText(r.copy(state = "back", endedAt = Instant.ofEpochMilli(now)), "Workstation", "pve", now)!!.title)
        val notBack = recoveryText(r.copy(state = "not_back"), "Workstation", "pve", now)!!
        assertEquals("Workstation didn't come back" to Tone.Warn, notBack.title to notBack.tone)
        assertEquals("The KVM can't be reached.", recoveryText(r.copy(state = "failed", message = "The KVM can't be reached."), "Workstation", "pve", now)!!.detail)
        for (closed in listOf("declined", "expired", "withdrawn")) assertNull(closed, recoveryText(r.copy(state = closed), "Workstation", "pve", now))
    }

    /** A hub's recovery lines are notified, and say what the frozen line would say again only once. */
    @Test fun recoveryLinesAreNotified() {
        val at = Instant.parse("2026-10-28T08:03:30Z")
        fun e(action: String, result: String, who: String = "pve") = Event(at, who, action, result)
        val lines = listOf(
            e("propose reset workstation: Nothing answers and its power light is on.", "proposed"),
            e("confirm reset workstation", "confirmed", "PowerDeck (automatic)"),
            e("propose force-off workstation", "proposed", "Atlas"),
            e("workstation answers again after the reset", "back"),
            e("workstation didn't answer within 5 minutes of the reset", "not_back"),
            e("workstation is off", "off"),
            e("reset workstation", "failed: The KVM can't be reached."),
        )
        for (l in lines) assertEquals(l.action, "workstation", EventsWorker.recoveryMachine(l))
        for (l in listOf(e("reset workstation", "done", "Pixel 9 Pro"), e("decline reset workstation", "declined", "Pixel 9 Pro"),
            e("reset workstation", "withdrawn: workstation answers again."), e("workstation answers again", "recovered"))) {
            assertNull(l.toString(), EventsWorker.recoveryMachine(l))
        }

        val frozen = e("workstation looks frozen: nothing answers, power light on", "detected")
        val again = e("workstation answers again", "recovered")
        val other = e("NAS looks frozen: nothing answers, power light on", "detected")
        assertEquals(listOf(lines[0], other), EventsWorker.quiet(listOf(frozen, lines[0], other)))
        assertEquals(listOf(lines[3]), EventsWorker.quiet(listOf(again, lines[3])))
        assertEquals(listOf(frozen, again), EventsWorker.quiet(listOf(frozen, again))) // nothing proposed (too soon): both stay
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
