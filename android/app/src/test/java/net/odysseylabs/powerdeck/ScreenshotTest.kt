package net.odysseylabs.powerdeck

import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.requiredSize
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.wrapContentSize
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.TransformOrigin
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.material3.SnackbarHostState
import androidx.compose.runtime.Composable
import androidx.compose.ui.test.junit4.v2.createComposeRule
import androidx.compose.ui.test.onRoot
import com.github.takahirom.roborazzi.RobolectricDeviceQualifiers
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.BatteryFull
import androidx.compose.material.icons.rounded.Wifi
import androidx.compose.material3.Icon
import com.github.takahirom.roborazzi.ExperimentalRoborazziApi
import com.github.takahirom.roborazzi.captureRoboImage
import com.github.takahirom.roborazzi.captureScreenRoboImage
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
import android.Manifest
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.platform.LocalInspectionMode
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.onNodeWithText
import net.odysseylabs.powerdeck.data.AgentKey
import net.odysseylabs.powerdeck.data.AddInfo
import net.odysseylabs.powerdeck.data.Agents
import net.odysseylabs.powerdeck.data.KeyInfo
import net.odysseylabs.powerdeck.ui.AddScreen
import net.odysseylabs.powerdeck.ui.AgentsActions
import net.odysseylabs.powerdeck.ui.AgentsScreen
import net.odysseylabs.powerdeck.ui.DeleteDialog
import net.odysseylabs.powerdeck.ui.ScanScreen
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows
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
        kvm = KvmLink("nanokvm", "100.101.1.20", "NanoKVM", "admin", ledSeen = true))
    private val nas = Device("nas", "NAS", null, pin = null, kvm = KvmLink("nanokvm", "100.101.1.40", "nas-kvm", "admin", ledSeen = true))
    private fun status(name: String, os: String, pending: Pending? = null, presence: Presence? = null, guests: Int? = null) = HelperStatus(
        name, os, "2.0.0", 3 * 3600 + 720, pending, presence, null, emptyList(), true, guests, if (guests != null) listOf("public", "private", "agent") else emptyList(),
        updatesPending = os == "windows", checks = listOf(
            Check("tailscale", "ok", "Tailscale connected", null, false),
            Check("fast_startup", "warn", "Fast Startup is on", "Shutting down from the Start menu then leaves the PC unable to wake.", true),
            Check("bios", "info", "Check the BIOS: Wake-on-LAN on, ErP off", "A first wake from the app confirms them.", false),
        ), youName = "Pixel",
        agents = Agents(allowed = true, keys = listOf(AgentKey("Atlas", listOf("Gaming-PC", "pve")))),
        caps = listOf("shutdown", "restart", "uninstall"),
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
        compose.setContent { PowerDeckTheme(dynamic = false) { WithStatusBar(content) } }
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
                    Peer("nanokvm", "nanokvm", null, "100.101.1.20", 80, null, null),
                ),
            ),
            AddActions(back = {}), SnackbarHostState(),
        )
    }

    @OptIn(ExperimentalRoborazziApi::class)
    @Test fun addSheet() {
        compose.setContent {
            PowerDeckTheme(dynamic = false) {
                WithStatusBar {
                    AddScreen(UiState(loaded = true, add = AddStep.Review(AddInfo("Gaming-PC", "gaming-pc.tail1234.ts.net", 47321, "x", null), external = false)),
                        AddActions(), SnackbarHostState())
                }
            }
        }
        compose.onNodeWithText("Countdown to action").assertExists()
        compose.onNodeWithText("Add Gaming-PC").assertExists()
        captureScreenRoboImage("build/outputs/roborazzi/add-sheet.png") // the sheet is its own window
    }

    @Test fun addSearching() = shot("add-searching") {
        AddScreen(UiState(loaded = true, devices = listOf(pc), finding = true), AddActions(back = {}), SnackbarHostState())
    }

    @Test fun addNothing() = shot("add-nothing") {
        AddScreen(UiState(loaded = true, devices = listOf(pc), found = emptyList()), AddActions(back = {}), SnackbarHostState())
    }

    @Test fun scan() {
        Shadows.shadowOf(RuntimeEnvironment.getApplication()).grantPermissions(Manifest.permission.CAMERA)
        shot("scan") { CompositionLocalProvider(LocalInspectionMode provides true) { ScanScreen(onCode = {}, onBack = {}) } }
    }

    @OptIn(ExperimentalRoborazziApi::class)
    @Test fun homeMenu() {
        compose.setContent { PowerDeckTheme(dynamic = false) { WithStatusBar { HomeScreen(all, HomeActions(), SnackbarHostState(), false) } } }
        compose.onNodeWithContentDescription("More for Gaming-PC").performClick()
        compose.onNodeWithText("Delete").assertExists()
        compose.onNodeWithText("Settings").assertExists()
        captureScreenRoboImage("build/outputs/roborazzi/home-menu.png") // the menu is its own window
    }

    @Test fun delete() = shot("delete") { DeleteDialog(pc, all.states["pc"], {}, {}, {}) }

    @Test fun agents() = shot("agents") {
        AgentsScreen(pve, all, Detail(keys = listOf(KeyInfo("k", "Atlas", "automation", listOf("status", "wake"), listOf("Gaming-PC", "pve"), true, "2026-10-27T07:00:00Z"))),
            AgentsActions(), SnackbarHostState())
    }

    @Test fun device() = shot("device") {
        DeviceScreen(
            pc, all,
            Detail(
                events = listOf(
                    Event(Instant.parse("2026-10-27T07:00:00Z"), "Atlas via pve", "wake Gaming-PC", "sent"),
                    Event(Instant.parse("2026-10-26T23:00:00Z"), "Schedule", "shutdown", "scheduled"),
                    Event(Instant.parse("2026-10-26T18:00:00Z"), "Pixel", "restart", "scheduled"),
                ),
                schedules = listOf(pc to Schedule("a", "shutdown", null, "01:00", listOf("mon", "tue", "wed", "thu", "fri", "sat", "sun"), true, true),
                    pve to Schedule("b", "wake", "Gaming-PC", "07:30", listOf("mon", "tue", "wed", "thu", "fri"), false, true)),
            ),
            DeviceActions(), SnackbarHostState(),
        )
    }

    @Test fun deviceAgents() {
        shot("device-agents") { DeviceScreen(pc, all, Detail(), DeviceActions(), SnackbarHostState()) }
        compose.onNodeWithText("Allow AI agents to use this device").performScrollTo().assertIsDisplayed()
        compose.onNodeWithText("Atlas can use it. Shutdowns count down 60 s").assertExists()
        compose.onRoot().captureRoboImage("build/outputs/roborazzi/device-agents.png")
    }

    @Test fun settings() = shot("settings") { SettingsScreen(Settings(), SettingsActions()) }

    /** The README banner and GitHub social preview: the real screens in phone frames. */
    @Config(qualifiers = "w1280dp-h640dp-hdpi")
    @Test fun banner() {
        compose.setContent { Banner() }
        compose.onRoot().captureRoboImage("build/outputs/roborazzi/banner.png")
    }

    @Composable
    private fun Banner() {
        Box(
            Modifier.fillMaxSize().background(Brush.linearGradient(listOf(Color(0xFF15123A), Color(0xFF2B1F7A), Color(0xFF5B4CF0)), start = Offset.Zero, end = Offset.Infinite)),
        ) {
            Column(Modifier.align(Alignment.CenterStart).padding(start = 72.dp, bottom = 8.dp).width(560.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Image(painterResource(R.drawable.ic_logo), contentDescription = null, modifier = Modifier.size(76.dp))
                    Spacer(Modifier.width(20.dp))
                    Text("PowerDeck", color = Color.White, fontSize = 64.sp, fontWeight = FontWeight.Bold, letterSpacing = (-1).sp)
                }
                Spacer(Modifier.height(20.dp))
                Text("Turn your computers on and off from your phone, from anywhere.", color = Color.White.copy(alpha = 0.86f), fontSize = 25.sp, lineHeight = 34.sp)
                Spacer(Modifier.height(34.dp))
                listOf(listOf("Windows", "Linux", "Proxmox"), listOf("Wake-on-LAN", "NanoKVM", "Schedules"), listOf("Widget & tiles", "AI agents (MCP)")).forEach { row ->
                    Row(horizontalArrangement = Arrangement.spacedBy(10.dp), modifier = Modifier.padding(bottom = 10.dp)) {
                        row.forEach { label ->
                            Text(
                                label, color = Color.White, fontSize = 17.sp, fontWeight = FontWeight.Medium,
                                modifier = Modifier.clip(RoundedCornerShape(50)).background(Color.White.copy(alpha = 0.13f)).padding(horizontal = 16.dp, vertical = 8.dp),
                            )
                        }
                    }
                }
            }
            // The home screen, where people spend their time, is the one in front.
            Phone(x = 640.dp, y = 74.dp, dark = false) {
                DeviceScreen(pc, all, Detail(schedules = listOf(pc to Schedule("a", "shutdown", null, "01:00", listOf("mon", "tue", "wed", "thu", "fri", "sat", "sun"), true, true))),
                    DeviceActions(), SnackbarHostState())
            }
            Phone(x = 1000.dp, y = 74.dp, dark = true) { HomeScreen(all, HomeActions(), SnackbarHostState(), false) }
            Phone(x = 810.dp, y = 44.dp, dark = false) { HomeScreen(all, HomeActions(), SnackbarHostState(), false) }
        }
    }

    /** The screen under a phone's status bar, as it looks on a device (Robolectric has no system bars). */
    @Composable
    private fun WithStatusBar(screen: @Composable () -> Unit) {
        Surface(color = MaterialTheme.colorScheme.background) {
            Column {
                Row(Modifier.fillMaxWidth().height(32.dp).padding(horizontal = 24.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text("9:41", style = MaterialTheme.typography.labelLarge, modifier = Modifier.weight(1f))
                    Icon(Icons.Rounded.Wifi, null, Modifier.size(16.dp))
                    Spacer(Modifier.width(4.dp))
                    Icon(Icons.Rounded.BatteryFull, null, Modifier.size(16.dp))
                }
                Box(Modifier.weight(1f)) { screen() }
            }
        }
    }

    /** A 411×914 dp screen at 58 %, in a rounded bezel. */
    @Composable
    private fun Phone(x: Dp, y: Dp, dark: Boolean, screen: @Composable () -> Unit) {
        val scale = 0.58f
        Box(
            Modifier.offset(x, y).shadow(28.dp, RoundedCornerShape(40.dp)).clip(RoundedCornerShape(40.dp))
                .background(Color(0xFF0B0B12)).padding(7.dp).clip(RoundedCornerShape(34.dp))
                .size(411.dp * scale, 914.dp * scale),
        ) {
            Box(Modifier.wrapContentSize(Alignment.TopStart, unbounded = true).requiredSize(411.dp, 914.dp)
                .graphicsLayer { scaleX = scale; scaleY = scale; transformOrigin = TransformOrigin(0f, 0f) }) {
                PowerDeckTheme(dynamic = false, dark = dark) { WithStatusBar(screen) }
            }
        }
    }
}
