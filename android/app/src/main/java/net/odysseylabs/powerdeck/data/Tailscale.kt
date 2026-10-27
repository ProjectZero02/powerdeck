package net.odysseylabs.powerdeck.data

import android.content.Context
import android.content.Intent
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import kotlinx.coroutines.delay
import kotlinx.coroutines.withTimeoutOrNull

/**
 * PowerDeck reaches the PC and NanoKVM over Tailscale. When the VPN is off it asks the Tailscale app
 * to connect (Tailscale's exported IPNReceiver accepts CONNECT_VPN), so buttons work without opening it.
 */
object Tailscale {
    const val PACKAGE = "com.tailscale.ipn"

    fun isInstalled(ctx: Context): Boolean =
        runCatching { ctx.packageManager.getPackageInfo(PACKAGE, 0) }.isSuccess

    @Suppress("DEPRECATION") // allNetworks is the simplest way to see a VPN that isn't the default network
    fun vpnActive(ctx: Context): Boolean {
        val cm = ctx.getSystemService(ConnectivityManager::class.java) ?: return false
        return cm.allNetworks.any { cm.getNetworkCapabilities(it)?.hasTransport(NetworkCapabilities.TRANSPORT_VPN) == true }
    }

    fun requestConnect(ctx: Context) {
        ctx.sendBroadcast(Intent("$PACKAGE.CONNECT_VPN").setClassName(PACKAGE, "$PACKAGE.IPNReceiver"))
    }

    fun launchIntent(ctx: Context): Intent? = ctx.packageManager.getLaunchIntentForPackage(PACKAGE)

    /** Makes sure the VPN is up, asking Tailscale to connect if allowed. Returns whether it is up. */
    suspend fun ensureConnected(ctx: Context, autoConnect: Boolean): Boolean {
        if (vpnActive(ctx)) return true
        if (!autoConnect || !isInstalled(ctx)) return false
        requestConnect(ctx)
        return withTimeoutOrNull(10_000) {
            while (!vpnActive(ctx)) delay(250)
            delay(600) // give routes a moment to settle
            true
        } ?: false
    }
}
