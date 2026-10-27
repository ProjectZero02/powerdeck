package net.odysseylabs.powerdeck.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.ListItem
import androidx.compose.material3.ListItemDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import net.odysseylabs.powerdeck.data.MAX_COUNTDOWN
import net.odysseylabs.powerdeck.data.Settings

/** How Shut down and Restart behave: hold to activate, and how long they count down. */
@Composable
fun ButtonSettings(settings: Settings, setHold: (Boolean) -> Unit, setCountdown: (Int) -> Unit) {
    Column {
        ListItem(
            headlineContent = { Text("Hold for ${HOLD_MILLIS / 1000} s to activate") },
            supportingContent = { Text("Shut down and Restart, so a stray tap does nothing") },
            trailingContent = { Switch(checked = settings.confirmShutdown, onCheckedChange = setHold) },
            colors = ListItemDefaults.colors(containerColor = Color.Transparent),
        )
        CountdownRow(settings.countdownSeconds, setCountdown)
    }
}

@Composable
private fun CountdownRow(seconds: Int, onChange: (Int) -> Unit) {
    var text by rememberSaveable { mutableStateOf(seconds.toString()) }
    LaunchedEffect(seconds) { if (text.toIntOrNull() != seconds) text = seconds.toString() }
    val value = text.toIntOrNull()
    val valid = value != null && value in 0..MAX_COUNTDOWN
    val focus = LocalFocusManager.current
    val save = { if (valid && value != seconds) onChange(value!!) }
    ListItem(
        headlineContent = { Text("Countdown to action") },
        supportingContent = {
            Text(if (valid || text.isEmpty()) "Seconds before it shuts down or restarts. You can cancel until then. 0 is right away."
                else "Use 0 to $MAX_COUNTDOWN seconds")
        },
        trailingContent = {
            OutlinedTextField(
                value = text,
                onValueChange = { t -> text = t.filter(Char::isDigit).take(3) },
                singleLine = true,
                isError = !valid && text.isNotEmpty(),
                suffix = { Text("s") },
                textStyle = MaterialTheme.typography.bodyLarge,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number, imeAction = ImeAction.Done),
                keyboardActions = KeyboardActions(onDone = { save(); focus.clearFocus() }),
                modifier = Modifier.width(96.dp).onFocusChanged { if (!it.isFocused) save() },
            )
        },
        colors = ListItemDefaults.colors(containerColor = Color.Transparent),
    )
}
