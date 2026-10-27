package net.odysseylabs.powerdeck

import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.DeviceState
import net.odysseylabs.powerdeck.data.HelperStatus
import com.google.zxing.BarcodeFormat
import com.google.zxing.qrcode.QRCodeReader
import com.google.zxing.qrcode.QRCodeWriter
import net.odysseylabs.powerdeck.ui.agentKeysFor
import net.odysseylabs.powerdeck.ui.decodeLuma
import net.odysseylabs.powerdeck.ui.uninstallBlocker
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class UiRulesTest {
    private fun status(json: String) = HelperStatus.from(JSONObject(json))
    private val pc = Device("pc", "My PC", "100.64.0.1", pin = "x", os = "windows")

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
