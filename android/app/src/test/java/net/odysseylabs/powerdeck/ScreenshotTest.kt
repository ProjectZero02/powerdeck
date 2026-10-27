package net.odysseylabs.powerdeck

import androidx.compose.material3.SnackbarHostState
import androidx.compose.runtime.Composable
import androidx.compose.ui.test.junit4.v2.createComposeRule
import androidx.compose.ui.test.onRoot
import com.github.takahirom.roborazzi.RobolectricDeviceQualifiers
import com.github.takahirom.roborazzi.captureRoboImage
import net.odysseylabs.powerdeck.data.Config
import net.odysseylabs.powerdeck.data.PcConfig
import net.odysseylabs.powerdeck.data.PcPending
import net.odysseylabs.powerdeck.data.PcState
import net.odysseylabs.powerdeck.data.Release
import net.odysseylabs.powerdeck.data.ServerState
import net.odysseylabs.powerdeck.ui.HomeActions
import net.odysseylabs.powerdeck.ui.HomeScreen
import net.odysseylabs.powerdeck.ui.KvmSetupScreen
import net.odysseylabs.powerdeck.ui.KvmStep
import net.odysseylabs.powerdeck.ui.PairingStep
import net.odysseylabs.powerdeck.ui.PowerDeckTheme
import net.odysseylabs.powerdeck.ui.SettingsActions
import net.odysseylabs.powerdeck.ui.SettingsScreen
import net.odysseylabs.powerdeck.ui.UiState
import net.odysseylabs.powerdeck.ui.WelcomeScreen
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config as RConfig
import org.robolectric.annotation.GraphicsMode
import java.time.Instant

/**
 * Renders each screen to build/outputs/roborazzi/ (record with -Proborazzi.test.record=true).
 * CI uploads the images so every release's UI can be reviewed without a device.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@RConfig(sdk = [36], qualifiers = RobolectricDeviceQualifiers.Pixel7)
class ScreenshotTest {
    @get:Rule val compose = createComposeRule()

    private val config = Config(
        pc = PcConfig("Gaming-PC", "100.101.1.10", 47321, "x", "d8:5e:d3:12:34:56"),
        nanoKvm = "100.101.1.20", nanoKvmUser = "admin", hasKvmPassword = true, proxmox = "100.101.1.30:8006",
        confirmShutdown = true, autoTailscale = true,
    )

    private fun shot(name: String, content: @Composable () -> Unit) {
        compose.setContent { PowerDeckTheme(dynamic = false) { content() } }
        compose.onRoot().captureRoboImage("build/outputs/roborazzi/$name.png")
    }

    @Test fun homeBothOn() = shot("home-both-on") {
        HomeScreen(UiState(config, PcState.On(3 * 3600 + 720, null), ServerState.On(true)), HomeActions(), SnackbarHostState(), showWidgetTip = true)
    }

    @Test fun homeStarting() = shot("home-pc-off-server-starting") {
        HomeScreen(UiState(config, PcState.Off, ServerState.On(false), busy = setOf(net.odysseylabs.powerdeck.ui.Target.Pc)), HomeActions(), SnackbarHostState(), false)
    }

    @Test fun homeShuttingDown() = shot("home-pc-shutting-down") {
        val pending = PcPending("shutdown", Instant.now().plusSeconds(24))
        HomeScreen(
            UiState(config, PcState.On(600, pending), ServerState.Off, update = Release("1.1.0", "", null, "")),
            HomeActions(), SnackbarHostState(), false,
        )
    }

    @Test fun homeTailscaleOff() = shot("home-tailscale-off") {
        HomeScreen(
            UiState(config.copy(hasKvmPassword = false), PcState.Problem("Tailscale is off"), ServerState.Problem("Tailscale is off"), tailnet = false),
            HomeActions(), SnackbarHostState(), false,
        )
    }

    @RConfig(qualifiers = "+night")
    @Test fun homeDark() = shot("home-dark") {
        HomeScreen(UiState(config, PcState.On(7200, null), ServerState.On(true)), HomeActions(), SnackbarHostState(), false)
    }

    @Test fun welcome() = shot("welcome") { WelcomeScreen(PairingStep.Idle, {}, {}, {}) }

    @Test fun welcomePairing() = shot("welcome-pairing") { WelcomeScreen(PairingStep.Working("Gaming-PC"), {}, {}, {}) }

    @Test fun kvmSetup() = shot("kvm-setup") { KvmSetupScreen("100.101.1.20", "admin", KvmStep.Failed("The NanoKVM rejected the password."), { _, _, _ -> }, {}) }

    @Test fun settings() = shot("settings") { SettingsScreen(config, SettingsActions()) }
}
