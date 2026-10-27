package net.odysseylabs.powerdeck.ui

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.provider.Settings
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageProxy
import androidx.camera.view.CameraController
import androidx.camera.view.LifecycleCameraController
import androidx.camera.view.PreviewView
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.rounded.ArrowBack
import androidx.compose.material.icons.rounded.FlashlightOff
import androidx.compose.material.icons.rounded.FlashlightOn
import androidx.compose.material.icons.rounded.NoPhotography
import androidx.compose.material3.Button
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.CompositingStrategy
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalInspectionMode
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LifecycleEventEffect
import androidx.lifecycle.compose.LocalLifecycleOwner
import com.google.zxing.BarcodeFormat
import com.google.zxing.BinaryBitmap
import com.google.zxing.DecodeHintType
import com.google.zxing.PlanarYUVLuminanceSource
import com.google.zxing.common.HybridBinarizer
import com.google.zxing.qrcode.QRCodeReader
import kotlinx.coroutines.delay
import net.odysseylabs.powerdeck.data.AddLink
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Scans the code a helper shows, inside the app. CameraX and ZXing ship with PowerDeck, so this
 * works without Google Play services. Only PowerDeck add codes are accepted.
 */
@Composable
fun ScanScreen(onCode: (String) -> Unit, onBack: () -> Unit) {
    val ctx = LocalContext.current
    fun granted() = ContextCompat.checkSelfPermission(ctx, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED
    var allowed by remember { mutableStateOf(granted()) }
    var asked by remember { mutableStateOf(false) }
    val ask = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { allowed = it; asked = true }
    LaunchedEffect(Unit) { if (!allowed) ask.launch(Manifest.permission.CAMERA) }
    // Back from system settings with the permission turned on.
    LifecycleEventEffect(Lifecycle.Event.ON_RESUME) { if (!allowed && granted()) allowed = true }

    Box(Modifier.fillMaxSize().background(Color.Black)) {
        when {
            allowed && !LocalInspectionMode.current -> Camera(onCode)
            allowed -> Unit
            asked -> NoCamera(
                onAllow = { ask.launch(Manifest.permission.CAMERA) },
                onSettings = {
                    ctx.startActivity(Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.fromParts("package", ctx.packageName, null)))
                },
            )
        }
        if (allowed) Viewfinder()
        IconButton(
            onClick = onBack,
            colors = IconButtonDefaults.iconButtonColors(containerColor = Color.Black.copy(alpha = 0.4f), contentColor = Color.White),
            modifier = Modifier.safeDrawingPadding().padding(12.dp),
        ) { Icon(Icons.AutoMirrored.Rounded.ArrowBack, contentDescription = "Back") }
    }
}

@Composable
private fun Camera(onCode: (String) -> Unit) {
    val ctx = LocalContext.current
    val owner = LocalLifecycleOwner.current
    val latestOnCode by rememberUpdatedState(onCode)
    var torch by remember { mutableStateOf(false) }
    var hasTorch by remember { mutableStateOf(false) }
    var noCamera by remember { mutableStateOf(false) }
    var wrong by remember { mutableStateOf(0L) }
    val done = remember { AtomicBoolean(false) }
    val controller = remember {
        LifecycleCameraController(ctx).apply { setEnabledUseCases(CameraController.IMAGE_ANALYSIS) }
    }
    DisposableEffect(owner, controller) {
        val main = ContextCompat.getMainExecutor(ctx)
        val worker = Executors.newSingleThreadExecutor()
        val reader = QRCodeReader()
        controller.setImageAnalysisAnalyzer(worker) { image ->
            val text = image.use { decodeQr(reader, it) } ?: return@setImageAnalysisAnalyzer
            main.execute {
                when {
                    AddLink.parse(text) != null -> if (done.compareAndSet(false, true)) latestOnCode(text)
                    else -> wrong = System.currentTimeMillis()
                }
            }
        }
        controller.bindToLifecycle(owner)
        controller.initializationFuture.addListener({
            noCamera = runCatching { controller.initializationFuture.get(); !controller.hasCamera(CameraSelector.DEFAULT_BACK_CAMERA) }.getOrDefault(true)
            hasTorch = controller.cameraInfo?.hasFlashUnit() == true
        }, main)
        onDispose {
            controller.clearImageAnalysisAnalyzer()
            controller.unbind()
            worker.shutdown()
        }
    }
    // The camera turns the light off when the app goes to the background.
    LifecycleEventEffect(Lifecycle.Event.ON_RESUME) { if (torch) controller.enableTorch(true) }
    if (noCamera) {
        Box(Modifier.fillMaxSize().padding(32.dp), contentAlignment = Alignment.Center) {
            Text(
                "This phone's camera isn't available. Go back and type the computer's Tailscale name instead.",
                style = MaterialTheme.typography.bodyLarge, color = Color.White, textAlign = TextAlign.Center,
            )
        }
        return
    }
    AndroidView(
        factory = { c -> PreviewView(c).apply { scaleType = PreviewView.ScaleType.FILL_CENTER; this.controller = controller } },
        modifier = Modifier.fillMaxSize(),
    )
    // Someone else's QR code: say so for a moment, keep scanning.
    var showWrong by remember { mutableStateOf(false) }
    LaunchedEffect(wrong) {
        if (wrong == 0L) return@LaunchedEffect
        showWrong = true
        delay(2500)
        showWrong = false
    }
    Column(
        Modifier.fillMaxSize().safeDrawingPadding().padding(24.dp),
        verticalArrangement = Arrangement.Bottom,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        if (showWrong) Pill("That isn't a PowerDeck code")
        Spacer(Modifier.height(16.dp))
        if (hasTorch) IconButton(
            onClick = { torch = !torch; controller.enableTorch(torch) },
            colors = IconButtonDefaults.iconButtonColors(containerColor = Color.Black.copy(alpha = 0.4f), contentColor = Color.White),
            modifier = Modifier.size(56.dp),
        ) { Icon(if (torch) Icons.Rounded.FlashlightOff else Icons.Rounded.FlashlightOn, contentDescription = if (torch) "Turn off the light" else "Turn on the light") }
    }
}

private val qrHints = mapOf(DecodeHintType.POSSIBLE_FORMATS to listOf(BarcodeFormat.QR_CODE), DecodeHintType.TRY_HARDER to true)

/** The QR code's text in a camera frame, or null. Reads only the brightness plane. */
private fun decodeQr(reader: QRCodeReader, image: ImageProxy): String? {
    val plane = image.planes[0]
    val w = image.width
    val h = image.height
    val stride = plane.rowStride
    val buf = plane.buffer.apply { rewind() }
    val data = ByteArray(w * h)
    if (stride == w) buf.get(data, 0, minOf(data.size, buf.remaining()))
    else for (row in 0 until h) {
        buf.position(row * stride)
        buf.get(data, row * w, w)
    }
    return decodeLuma(reader, data, w, h)
}

/** The QR code's text in a [w]×[h] greyscale image, or null. */
internal fun decodeLuma(reader: QRCodeReader, data: ByteArray, w: Int, h: Int): String? {
    val source = PlanarYUVLuminanceSource(data, w, h, 0, 0, w, h, false)
    return try {
        reader.decode(BinaryBitmap(HybridBinarizer(source)), qrHints).text
    } catch (_: Exception) {
        null // no code in this frame
    } finally {
        reader.reset()
    }
}

@Composable
private fun Pill(text: String) {
    Surface(shape = RoundedCornerShape(50), color = Color.Black.copy(alpha = 0.6f), contentColor = Color.White) {
        Text(text, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp))
    }
}

/** Dims everything but a rounded square, with the hint above it. */
@Composable
private fun Viewfinder() {
    Box(Modifier.fillMaxSize()) {
        Canvas(Modifier.fillMaxSize().graphicsLayer { compositingStrategy = CompositingStrategy.Offscreen }) {
            val side = minOf(size.width, size.height) * 0.68f
            val tl = Offset((size.width - side) / 2, (size.height - side) / 2.4f)
            val r = CornerRadius(28.dp.toPx())
            drawRect(Color.Black.copy(alpha = 0.55f))
            drawRoundRect(Color.Transparent, tl, Size(side, side), r, blendMode = BlendMode.Clear)
            drawRoundRect(Color.White, tl, Size(side, side), r, style = Stroke(3.dp.toPx()))
        }
        Column(Modifier.fillMaxWidth().safeDrawingPadding().padding(top = 72.dp, start = 32.dp, end = 32.dp), horizontalAlignment = Alignment.CenterHorizontally) {
            Text("Scan the helper's code", style = MaterialTheme.typography.titleLarge, color = Color.White)
            Spacer(Modifier.height(6.dp))
            Text(
                "It's in the PowerDeck window on the computer.",
                style = MaterialTheme.typography.bodyMedium, color = Color.White.copy(alpha = 0.8f), textAlign = TextAlign.Center,
            )
        }
    }
}

@Composable
private fun NoCamera(onAllow: () -> Unit, onSettings: () -> Unit) {
    Column(
        Modifier.fillMaxSize().safeDrawingPadding().padding(32.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Icon(Icons.Rounded.NoPhotography, contentDescription = null, tint = Color.White, modifier = Modifier.size(48.dp))
        Spacer(Modifier.height(16.dp))
        Text("PowerDeck needs the camera to scan the code", style = MaterialTheme.typography.titleMedium, color = Color.White, textAlign = TextAlign.Center)
        Spacer(Modifier.height(6.dp))
        Text(
            "It's only used while this screen is open. You can also go back and type the computer's Tailscale name.",
            style = MaterialTheme.typography.bodyMedium, color = Color.White.copy(alpha = 0.8f), textAlign = TextAlign.Center,
        )
        Spacer(Modifier.height(20.dp))
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            TextButton(onClick = onSettings) { Text("Open settings", color = Color.White) }
            Button(onClick = onAllow) { Text("Allow camera") }
        }
    }
}
