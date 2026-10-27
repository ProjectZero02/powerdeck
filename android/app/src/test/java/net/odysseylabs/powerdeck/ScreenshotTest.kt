package net.odysseylabs.powerdeck

import androidx.compose.material3.SnackbarHostState
import androidx.compose.runtime.Composable
import androidx.compose.ui.test.junit4.v2.createComposeRule
import androidx.compose.ui.test.onRoot
import com.github.takahirom.roborazzi.RobolectricDeviceQualifiers
import com.github.takahirom.roborazzi.captureRoboImage
import net.odysseylabs.powerdeck.data.Check
import net.odysseylabs.powerdeck.data.Device
import net.odysseylabs.powerdeck.data.DeviceState
import net.odysseylabs.powerdeck.data.Event
import net.odysseylabs.powerdeck.data.HelperStatus
import net.odysseylabs.powerdeck.data.KvmLink
import net.odysseylabs.powerdeck.data.Peer
import net.odysseylabs.powerdeck.data.Pending
import net.odysseylabs.powerdeck.data.Presence
import net.odysseylabs.powerdeck.data.Release
import net.odysseylabs.powerdeck.data.Schedule
import net.odysseylabs.powerdeck.data.Settings
import net.odysseylabs.powerdeck.ui.AddActions
import net.odysseylabs.powerdeck.ui.AddScreen
import net.odysseylabs.powerdeck.ui.AddStep
import net.odysseylabs.powerdeck.ui.Detail
import net.odysseylabs.powerdeck.ui.DeviceActions
import net.odysseylabs.powerdeck.ui.DeviceScreen
import net.odysseylabs.powerdeck.ui.HomeActions
import net.odysseylabs.powerdeck.ui.HomeScreen
import net.odysseylabs.powerdeck.ui.PowerDeckTheme
import net.odysseylabs.powerdeck.ui.SettingsActions
import net.odysseylabs.powerdeck.ui.SettingsScreen
import net.odysseylabs.powerdeck.ui.UiState
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.time.Instant

/**
 * Renders each screen to build/outputs/roborazzi/ (record with -Proborazzi.test.record=true).
 * CI uploads the images so every release's UI can be reviewed without a device.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [36], qualifiers = RobolectricDeviceQualifiers.Pixel7)
class ScreenshotTest {
    @get:Rule val compose = createComposeRule()

    private val pc = Device("pc", "Gaming-PC", "100.101.1.10", pin = "x", os = "windows", mac = "d8:5e:d3:12:34:56", lan = listOf("192.168.1.0/24"), me = "Pixel")
    private val pve = Device("pve", "pve", "100.101.1.30", pin = "y", os = "linux", guests = true, lan = listOf("192.168.1.0/24"),
        kvm = KvmLink("nanokvm", "100.101.1.20", "NanoKVM", "admin"))
    private val nas = Device("nas", "NAS", null, pin = null, kvm = KvmLink("nanokvm", "100.101.1.40", "nas-kvm", "admin"))
    private fun status(name: String, os: String, pending: Pending? = null, presence: Presence? = null, guests: Int? = null) = HelperStatus(
        name, os, "2.0.0", 3 * 3600 + 720, pending, presence, null, emptyList(), true, guests, if (guests != null) listOf("public", "private", "agent") else emptyList(),
        updatesPending = os == "windows", checks = listOf(
            Check("tailscale", "ok", "Tailscale connected", null, false),
            Check("fast_startup", "warn", "Fast Startup is on", "Turn it off so a shutdown is a real power-off the network card can wake from.", true),
            Check("bios", "info", "BIOS: Wake-on-LAN on, ErP off", "Confirmed by the first successful test wake.", false),
        ), youName = "Pixel",
    )
    private val all = UiState(
        loaded = true, devices = listOf(pc, pve, nas), tiles = listOf("pc", "pve", null, null),
        states = mapOf(
            "pc" to DeviceState.On(status("Gaming-PC", "windows", presence = Presence(true, "Alex", 20, true, false, 1))),
            "pve" to DeviceState.On(status("pve", "linux", guests = 3)),
            "nas" to DeviceState.Off,
        ),
    )

    private fun shot(name: String, content: @Composable () -> Unit) {
        compose.setContent { PowerDeckTheme(dynamic = false) { content() } }
        compose.onRoot().captureRoboImage("build/outputs/roborazzi/$name.png")
    }

    @Test fun home() = shot("home") { HomeScreen(all, HomeActions(), SnackbarHostState(), showWidgetTip = true) }

    @Test fun homeBusy() = shot("home-countdown") {
        val pending = Pending("shutdown", Instant.now().plusSeconds(44), "Pixel")
        HomeScreen(
            all.copy(
                states = all.states + ("pc" to DeviceState.On(status("Gaming-PC", "windows", pending = pending))) + ("pve" to DeviceState.Off),
                busy = setOf("pve"), update = Release("2.1.0", "", null, ""),
                found = listOf(Peer("helper", "laptop", null, "100.101.1.50", 47321, "windows", "z")),
            ),
            HomeActions(), SnackbarHostState(), false,
        )
    }

    @Test fun homeTailscaleOff() = shot("home-tailscale-off") {
        HomeScreen(all.copy(tailnet = false, states = all.devices.associate { it.id to DeviceState.Problem("Tailscale is off") }), HomeActions(), SnackbarHostState(), false)
    }

    @Config(qualifiers = "+night")
    @Test fun homeDark() = shot("home-dark") { HomeScreen(all, HomeActions(), SnackbarHostState(), false) }

    @Test fun welcome() = shot("welcome") { AddScreen(UiState(loaded = true), AddActions(), SnackbarHostState()) }

    @Test fun addFound() = shot("add-found") {
        AddScreen(
            UiState(
                loaded = true, devices = listOf(pc), add = AddStep.Working("pve"),
                found = listOf(
                    Peer("helper", "pve", null, "100.101.1.30", 47321, "linux", "y"),
                    Peer("nanokvm", "server-nanokvm", null, "100.101.1.20", 80, null, null),
                ),
            ),
            AddActions(back = {}), SnackbarHostState(),
        )
    }

    @Test fun device() = shot("device") {
        DeviceScreen(
            pc, all,
            Detail(
                events = listOf(
                    Event(Instant.parse("2026-10-27T07:00:00Z"), "Lyra via pve", "wake Gaming-PC", "sent"),
                    Event(Instant.parse("2026-10-26T23:00:00Z"), "Schedule", "shutdown", "scheduled"),
                    Event(Instant.parse("2026-10-26T18:00:00Z"), "Pixel", "restart", "scheduled"),
                ),
                schedules = listOf(pc to Schedule("a", "shutdown", null, "01:00", listOf("mon", "tue", "wed", "thu", "fri", "sat", "sun"), true, true),
                    pve to Schedule("b", "wake", "Gaming-PC", "07:30", listOf("mon", "tue", "wed", "thu", "fri"), false, true)),
            ),
            DeviceActions(), SnackbarHostState(),
        )
    }

    @Test fun settings() = shot("settings") { SettingsScreen(Settings(), SettingsActions()) }
}
