package net.odysseylabs.powerdeck.ui

import android.content.Intent
import android.net.Uri
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.viewModels
import androidx.compose.material3.SnackbarHostState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LifecycleEventEffect
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.codescanner.GmsBarcodeScannerOptions
import com.google.mlkit.vision.codescanner.GmsBarcodeScanning
import net.odysseylabs.powerdeck.data.Tailscale

class MainActivity : ComponentActivity() {
    private val vm: MainViewModel by viewModels()

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        handleLink(intent)
        setContent { PowerDeckTheme { App(vm) } }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleLink(intent)
    }

    /** powerdeck://pair?… links (e.g. sent from the PC) pair directly. */
    private fun handleLink(intent: Intent?) {
        val data = intent?.data ?: return
        if (data.scheme == "powerdeck") vm.pair(data.toString())
    }

    fun scan() {
        val options = GmsBarcodeScannerOptions.Builder().setBarcodeFormats(Barcode.FORMAT_QR_CODE).enableAutoZoom().build()
        GmsBarcodeScanning.getClient(this, options).startScan()
            .addOnSuccessListener { code -> code.rawValue?.let(vm::pair) }
            .addOnFailureListener { vm.scanFailed(it.message) }
    }

    fun openTailscale() {
        if (Tailscale.isInstalled(this)) {
            Tailscale.requestConnect(this)
            Tailscale.launchIntent(this)?.let(::startActivity)
        } else {
            startActivity(Intent(Intent.ACTION_VIEW, Uri.parse("https://play.google.com/store/apps/details?id=${Tailscale.PACKAGE}")))
        }
    }
}

private enum class Screen { Home, Settings, Kvm }

@Composable
private fun App(vm: MainViewModel) {
    val state by vm.state.collectAsStateWithLifecycle()
    val activity = androidx.compose.ui.platform.LocalContext.current as MainActivity
    val snackbar = remember { SnackbarHostState() }
    var screen by rememberSaveable { mutableStateOf(Screen.Home) }
    var kvmSkipped by rememberSaveable { mutableStateOf(false) }
    var widgetTip by remember { mutableStateOf(false) }

    LaunchedEffect(Unit) { vm.messages.collect { snackbar.showSnackbar(it) } }
    LifecycleEventEffect(Lifecycle.Event.ON_RESUME) {
        widgetTip = !Shortcuts.hasWidget(activity)
        vm.startPolling()
    }
    LifecycleEventEffect(Lifecycle.Event.ON_PAUSE) { vm.stopPolling() }

    val config = state.config ?: return
    if (config.pc == null) {
        WelcomeScreen(state.pairing, onScan = activity::scan, onPaste = vm::pair, onDismissError = vm::clearPairingError)
        return
    }
    val needsKvm = (config.nanoKvm != null && !config.hasKvmPassword && !kvmSkipped) || screen == Screen.Kvm
    if (needsKvm) {
        LaunchedEffect(state.kvm) {
            if (state.kvm == KvmStep.Done) {
                vm.resetKvmStep()
                screen = Screen.Home
            }
        }
        BackHandler(enabled = screen == Screen.Kvm) { screen = Screen.Settings }
        KvmSetupScreen(
            address = config.nanoKvm.orEmpty(),
            user = config.nanoKvmUser,
            step = state.kvm,
            onConnect = vm::connectKvm,
            onSkip = { kvmSkipped = true; vm.resetKvmStep(); screen = Screen.Home },
        )
        return
    }
    when (screen) {
        Screen.Settings -> {
            BackHandler { screen = Screen.Home }
            SettingsScreen(
                config,
                SettingsActions(
                    back = { screen = Screen.Home },
                    unpair = { vm.unpair(); screen = Screen.Home },
                    changeKvm = { vm.resetKvmStep(); screen = Screen.Kvm },
                    forgetKvm = { vm.forgetKvm() },
                    setConfirm = { vm.setConfirmShutdown(it) },
                    setAutoTailscale = { vm.setAutoTailscale(it) },
                    addWidget = { Shortcuts.pinWidget(activity) },
                    addTiles = { Shortcuts.addTiles(activity) },
                    checkUpdate = vm::checkUpdate,
                    openProject = { activity.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse("https://github.com/ProjectZero02/powerdeck"))) },
                ),
            )
        }
        else -> HomeScreen(
            state,
            HomeActions(
                pcOn = vm::pcOn,
                pcShutdown = vm::pcShutdown,
                pcRestart = vm::pcRestart,
                pcCancel = vm::pcCancel,
                serverOn = vm::serverOn,
                serverOff = vm::serverOff,
                setUpServer = { vm.resetKvmStep(); screen = Screen.Kvm },
                retry = vm::refresh,
                openSettings = { screen = Screen.Settings },
                connectTailscale = activity::openTailscale,
                update = vm::installUpdate,
                addWidget = { Shortcuts.pinWidget(activity) },
            ),
            snackbar,
            showWidgetTip = widgetTip,
        )
    }
}
