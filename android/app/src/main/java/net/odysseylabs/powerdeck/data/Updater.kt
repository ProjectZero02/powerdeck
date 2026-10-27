package net.odysseylabs.powerdeck.data

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.net.Uri
import android.os.Build
import android.provider.Settings
import android.widget.Toast
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import net.odysseylabs.powerdeck.BuildConfig
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONObject
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.TimeUnit

data class Release(val version: String, val apkUrl: String, val sha256: String?, val notesUrl: String)

/**
 * In-app updates from GitHub Releases. Android only installs an update signed with the same key as the
 * installed app, so a tampered APK can't replace PowerDeck; the published SHA-256 is checked as well.
 */
object Updater {
    const val REPO = "ProjectZero02/powerdeck"
    const val APK = "PowerDeck.apk"
    private val http = OkHttpClient.Builder().connectTimeout(10, TimeUnit.SECONDS).readTimeout(60, TimeUnit.SECONDS).build()

    fun newer(a: String, b: String): Boolean {
        fun parts(v: String) = v.removePrefix("v").substringBefore('-').split('.').map { it.toIntOrNull() ?: 0 } + listOf(0, 0, 0)
        val pa = parts(a)
        val pb = parts(b)
        for (i in 0 until 3) if (pa[i] != pb[i]) return pa[i] > pb[i]
        return false
    }

    val isReleaseBuild get() = !BuildConfig.VERSION_NAME.contains("dev")

    /** The latest release with an APK, or null if it has none; throws when GitHub can't be reached. */
    suspend fun latest(): Release? = withContext(Dispatchers.IO) {
        val req = Request.Builder().url("https://api.github.com/repos/$REPO/releases/latest")
            .header("Accept", "application/vnd.github+json").build()
        run {
            http.newCall(req).execute().use { r ->
                if (!r.isSuccessful) throw java.io.IOException("GitHub answered HTTP ${r.code}")
                val o = JSONObject(r.body.string())
                val assets = o.getJSONArray("assets")
                val apk = (0 until assets.length()).map { assets.getJSONObject(it) }.firstOrNull { it.optString("name") == APK } ?: return@use null
                Release(
                    version = o.getString("tag_name").removePrefix("v"),
                    apkUrl = apk.getString("browser_download_url"),
                    sha256 = apk.optString("digest").takeIf { it.startsWith("sha256:") }?.removePrefix("sha256:"),
                    notesUrl = o.optString("html_url"),
                )
            }
        }
    }

    /** Update available for this build? */
    suspend fun available(): Release? = latest()?.takeIf { isReleaseBuild && newer(it.version, BuildConfig.VERSION_NAME) }

    fun canInstall(ctx: Context) = ctx.packageManager.canRequestPackageInstalls()

    fun allowInstallsIntent(ctx: Context) =
        Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${ctx.packageName}")).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)

    /** Downloads, verifies and hands the APK to the system installer (which asks the user to confirm). */
    suspend fun install(ctx: Context, release: Release) = withContext(Dispatchers.IO) {
        val file = File(ctx.cacheDir, "update.apk")
        http.newCall(Request.Builder().url(release.apkUrl).build()).execute().use { r ->
            check(r.isSuccessful) { "Download failed (HTTP ${r.code})" }
            file.outputStream().use { out -> r.body.byteStream().copyTo(out) }
        }
        release.sha256?.let { want ->
            val got = MessageDigest.getInstance("SHA-256").digest(file.readBytes()).joinToString("") { "%02x".format(it) }
            check(got.equals(want, ignoreCase = true)) { "The download is corrupt (checksum mismatch)" }
        }
        val installer = ctx.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
            setAppPackageName(ctx.packageName)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED)
            }
        }
        val id = installer.createSession(params)
        installer.openSession(id).use { s ->
            s.openWrite("PowerDeck.apk", 0, file.length()).use { out -> file.inputStream().use { it.copyTo(out) }; s.fsync(out) }
            val pi = PendingIntent.getBroadcast(
                ctx, id, Intent(ctx, UpdateReceiver::class.java),
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE,
            )
            s.commit(pi.intentSender)
        }
        file.delete()
    }
}

/** Receives the installer's status; shows the system confirmation when Android requires it. */
class UpdateReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        when (intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                @Suppress("DEPRECATION")
                val confirm = intent.getParcelableExtra<Intent>(Intent.EXTRA_INTENT) ?: return
                ctx.startActivity(confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
            }
            PackageInstaller.STATUS_SUCCESS -> Unit
            else -> Toast.makeText(ctx, "Update failed: ${intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE)}", Toast.LENGTH_LONG).show()
        }
    }
}
