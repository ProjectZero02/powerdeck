package net.odysseylabs.powerdeck.ui

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.viewModels
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LifecycleEventEffect
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.flow.MutableStateFlow
import net.odysseylabs.powerdeck.data.Tailscale

class MainActivity : ComponentActivity() {
    private val vm: MainViewModel by viewModels()
    /** Screen requested from outside (a shortcut, a notification, an add link). */
    val route = MutableStateFlow<String?>(null)

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        if (savedInstanceState == null) handle(intent)
        setContent { PowerDeckTheme { App(vm, this) } }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handle(intent)
    }

    private fun handle(intent: Intent?) {
        intent?.getStringExtra(EXTRA_DEVICE)?.let { route.value = "device:$it" }
        val data = intent?.data ?: return
        if (data.scheme == "powerdeck") {
            route.value = "add"
            vm.openLink(data.toString())
        }
    }

    fun openTailscale() {
        if (Tailscale.isInstalled(this)) {
            Tailscale.requestConnect(this)
            Tailscale.launchIntent(this)?.let(::startActivity)
        } else {
            startActivity(Intent(Intent.ACTION_VIEW, Uri.parse("https://play.google.com/store/apps/details?id=${Tailscale.PACKAGE}")))
        }
    }

    fun askNotifications() {
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
    }

    fun open(url: String) = startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)))

    companion object {
        const val EXTRA_DEVICE = "device"
    }
}

@Composable
private fun App(vm: MainViewModel, activity: MainActivity) {
    val state by vm.state.collectAsStateWithLifecycle()
    val detail by vm.detail.collectAsStateWithLifecycle()
    val requested by activity.route.collectAsStateWithLifecycle()
    val ctx = LocalContext.current
    val snackbar = remember { SnackbarHostState() }
    var route by rememberSaveable { mutableStateOf("home") }
    val prefs = remember { ctx.getSharedPreferences("ui", 0) }
    var widgetTip by remember { mutableStateOf(false) }

    LaunchedEffect(requested) {
        requested?.let { route = it; activity.route.value = null }
    }
    LaunchedEffect(Unit) { vm.messages.collect { snackbar.showSnackbar(it) } }
    LifecycleEventEffect(Lifecycle.Event.ON_RESUME) {
        widgetTip = !prefs.getBoolean("widgetTipDismissed", false) && !Shortcuts.hasWidget(ctx)
        vm.startPolling()
        vm.find(refresh = false)
    }
    LifecycleEventEffect(Lifecycle.Event.ON_PAUSE) { vm.stopPolling() }
    // Once the first device is in, ask for notification permission (schedules and agents report through it).
    LaunchedEffect(state.devices.isNotEmpty()) { if (state.devices.isNotEmpty()) activity.askNotifications() }

    if (!state.loaded) return
    val hint: (String) -> Unit = { vm.say(it) }

    when {
        route == "scan" -> {
            val back = if (state.devices.isEmpty()) "home" else "add"
            BackHandler { route = back }
            ScanScreen(onCode = { vm.addLink(it); route = back }, onBack = { route = back })
        }
        state.devices.isEmpty() || route == "add" -> {
            if (route == "add") BackHandler { vm.resetAdd(); route = "home" }
            LaunchedEffect(Unit) { vm.find(refresh = true) }
            AddScreen(
                state,
                AddActions(
                    back = if (state.devices.isEmpty()) null else ({ vm.resetAdd(); route = "home" }),
                    scan = { route = "scan" },
                    byName = vm::addByName,
                    addPeer = vm::addPeer,
                    submitCode = vm::submitCode,
                    dismiss = vm::resetAdd,
                    confirmLink = vm::confirmLink,
                    setHold = { vm.setConfirmShutdown(it) },
                    setCountdown = { vm.setCountdown(it) },
                    find = { vm.find(refresh = true) },
                    install = { activity.open(INSTALL_GUIDE) },
                    linkKvm = { id, name, p, user, pw, done -> vm.linkKvm(id, name, p.kind, p.kvmHost, p.name, user, pw, done) },
                ),
                snackbar,
            )
        }
        route == "settings" -> {
            BackHandler { route = "home" }
            SettingsScreen(
                state.settings,
                SettingsActions(
                    back = { route = "home" },
                    setConfirm = { vm.setConfirmShutdown(it) },
                    setCountdown = { vm.setCountdown(it) },
                    setAutoTailscale = vm::setAutoTailscale,
                    setNotifications = { on -> vm.setNotifications(on); if (on) activity.askNotifications() },
                    addWidget = { Shortcuts.pinWidget(ctx) },
                    addTiles = { Shortcuts.addTiles(ctx, state.devices, state.tiles) { slot, d -> vm.setTile(slot, d) } },
                    checkUpdate = vm::checkUpdate,
                    installUpdate = vm::installUpdate,
                    updateDevices = vm::updateAllHelpers,
                    setDevicesAutoUpdate = vm::setHelperAutoUpdate,
                    openProject = { activity.open("https://github.com/ProjectZero02/powerdeck") },
                ),
                helpers = vm.helperUpdates(state),
                update = state.update,
                appCheck = state.appCheck,
                snackbar = snackbar,
            )
        }
        route.startsWith("device:") -> {
            val d = state.devices.firstOrNull { it.id == route.removePrefix("device:") }
            if (d == null) {
                LaunchedEffect(Unit) { route = "home" }
                return
            }
            BackHandler { route = "home" }
            LaunchedEffect(d.id) { vm.loadDetail(d) }
            DeviceScreen(
                d, state, detail,
                DeviceActions(
                    back = { route = "home" },
                    turnOn = { vm.turnOn(d) },
                    shutdown = { upd -> vm.powerOff(d, restart = false, installUpdates = upd) },
                    restart = { vm.powerOff(d, restart = true) },
                    cancel = { vm.cancel(d) },
                    forceOff = { vm.forceOff(d) },
                    fix = { vm.fix(d, it) },
                    addSchedule = { act, t, days, skip -> vm.addSchedule(d, act, t, days, skip) },
                    removeSchedule = { host, s -> vm.removeSchedule(d, host, s) },
                    loadKeys = { vm.loadKeys(d) },
                    addKey = { n, p, devs -> vm.addKey(d, n, p, devs) },
                    removeKey = { vm.removeKey(d, it) },
                    clearNewKey = vm::clearNewKey,
                    linkKvm = { id, name, p, user, pw, done -> vm.linkKvm(id, name, p.kind, p.kvmHost, p.name, user, pw, done) },
                    unlinkKvm = { vm.unlinkKvm(d) },
                    setTile = { slot ->
                        val cur = state.tiles.indexOf(d.id)
                        if (cur >= 0) vm.setTile(cur, null)
                        if (slot != null) {
                            vm.setTile(slot, d)
                            Shortcuts.requestTile(ctx, slot, d.name)
                        }
                    },
                    pinShortcut = { Shortcuts.pinDevice(ctx, d) },
                    rename = { vm.rename(d, it) },
                    remove = { vm.remove(d); route = "home" },
                    uninstall = { vm.uninstall(d); route = "home" },
                    setAgents = { vm.setAgents(d, it) },
                    agents = { route = "agents:${d.id}" },
                    hint = hint,
                    updateHelper = { vm.updateHelper(d) },
                    pauseUpdates = { vm.pauseUpdates(d, it) },
                    removeOwner = { vm.removeOwner(d, it) },
                ),
                snackbar,
            )
        }
        route.startsWith("agents:") -> {
            val d = state.devices.firstOrNull { it.id == route.removePrefix("agents:") }
            if (d == null) {
                LaunchedEffect(Unit) { route = "home" }
                return
            }
            BackHandler { route = "device:${d.id}" }
            AgentsScreen(
                d, state, detail,
                AgentsActions(
                    back = { route = "device:${d.id}" },
                    loadKeys = { vm.loadKeys(d) },
                    addKey = { n, p, devs -> vm.addKey(d, n, p, devs) },
                    removeKey = { vm.removeKey(d, it) },
                    clearNewKey = vm::clearNewKey,
                    guide = { activity.open(AGENTS_GUIDE) },
                ),
                snackbar,
            )
        }
        else -> HomeScreen(
            state,
            HomeActions(
                open = { route = "device:${it.id}" },
                turnOn = vm::turnOn,
                shutdown = { vm.powerOff(it, restart = false) },
                restart = { vm.powerOff(it, restart = true) },
                cancel = vm::cancel,
                retry = vm::refresh,
                add = { route = "add" },
                settings = { route = "settings" },
                connectTailscale = activity::openTailscale,
                update = vm::installUpdate,
                addWidget = { Shortcuts.pinWidget(ctx) },
                dismissWidgetTip = { prefs.edit().putBoolean("widgetTipDismissed", true).apply(); widgetTip = false },
                hint = hint,
                rename = { d, n -> vm.rename(d, n) },
                pinShortcut = { Shortcuts.pinDevice(ctx, it) },
                remove = vm::remove,
                uninstall = vm::uninstall,
            ),
            snackbar,
            showWidgetTip = widgetTip,
        )
    }

    state.force?.let { f ->
        AlertDialog(
            onDismissRequest = vm::dismissForce,
            title = { Text(f.ask.title) },
            text = { Text(f.ask.text) },
            confirmButton = {
                Button(onClick = { vm.forcePower(f) }, colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error)) {
                    Text(f.ask.confirm)
                }
            },
            dismissButton = { TextButton(onClick = vm::dismissForce) { Text("Leave it on") } },
        )
    }
}
