package dev.remoteagent.app.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Icon
import androidx.compose.material3.Slider
import androidx.compose.material3.SliderDefaults
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.adaptive.currentWindowAdaptiveInfoV2
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.unit.dp
import androidx.window.core.layout.WindowSizeClass
import dev.remoteagent.app.platform.AppSettings.Keys
import dev.remoteagent.app.platform.state
import kotlin.math.roundToInt

/** The app's settings. Mirrors apple/Shared/Views/SettingsView.swift. */
@Composable
fun SettingsView(onBack: () -> Unit) {
    val settings = LocalSettings.current
    val appearance by settings.state(Keys.appearance, Appearance.SYSTEM.name)
    val follows by settings.state(Keys.interfaceFollowsSystem, true)
    val interfaceStep by settings.state(Keys.interfaceTextSize, TextSize.DEFAULT_INTERFACE)
    val messages by settings.state(Keys.messageTextSize, TextSize.DEFAULT_MESSAGES)
    val placement by settings.state(Keys.terminalPlacement, TerminalPlacement.OVER.name)
    val layoutName by settings.state(Keys.sidebarLayout, SidebarLayout.INBOX.stored)
    val sidebarLayout = SidebarLayout.parse(layoutName)
    val systemScale = LocalConfiguration.current.fontScale
    // The sidebar's layout and where the terminal opens: for a wide window. A
    // phone shows the Inbox and each server's sessions as screens of their
    // own, and the terminal in place of the messages, as there's no room
    // beside them.
    val wide = currentWindowAdaptiveInfoV2().windowSizeClass.isWidthAtLeastBreakpoint(WindowSizeClass.WIDTH_DP_EXPANDED_LOWER_BOUND)

    fun setAppearance(a: Appearance) {
        settings.put(Keys.appearance, a.name)
        settings.applyAppearance(a)
    }

    val isDefault = appearance == Appearance.SYSTEM.name && follows && messages == TextSize.DEFAULT_MESSAGES &&
        placement == TerminalPlacement.OVER.name && sidebarLayout == SidebarLayout.INBOX

    Screen(title = { Text("Settings", style = Type.headline) }, onBack = onBack, background = palette.mantle) { padding ->
        Column(Modifier.fillMaxSize().padding(padding).verticalScroll(rememberScrollState()).formWidth()) {
            FormSection("Appearance", footer = AnnotatedString("Catppuccin Latte when light, Frappé when dark.")) {
                Appearance.entries.forEachIndexed { i, a ->
                    if (i > 0) FormDivider()
                    ChoiceRow(a.title, selected = appearance == a.name) { setAppearance(a) }
                }
            }
            FormSection(
                "Interface",
                footer = AnnotatedString(if (follows) "Lists and labels, at the font size set in the Settings app." else "Lists and labels."),
            ) {
                SwitchRow("Match System", follows) { on ->
                    // Turning it off starts the slider at the system's size.
                    if (!on) {
                        val system = 17f * systemScale
                        settings.put(Keys.interfaceTextSize, TextSize.interfaceSizes.indices.minBy { kotlin.math.abs(TextSize.interfaceSizes[it] - system) })
                    }
                    settings.put(Keys.interfaceFollowsSystem, on)
                }
                if (!follows) {
                    FormDivider()
                    val step = TextSize.step(interfaceStep)
                    SizeSlider(step.toFloat(), 0f..TextSize.interfaceSizes.lastIndex.toFloat(), TextSize.interfaceSizes.size - 2, "${TextSize.interfaceSizes[step].toInt()} pt") {
                        settings.put(Keys.interfaceTextSize, it.roundToInt())
                    }
                }
            }
            FormSection("Messages", footer = AnnotatedString("Your prompts, the agent's replies and its tool calls.")) {
                val size = TextSize.messages(messages)
                SizeSlider(size, TextSize.messageSizes, (TextSize.messageSizes.endInclusive - TextSize.messageSizes.start).toInt() - 1, "${size.toInt()} pt") {
                    settings.put(Keys.messageTextSize, it.roundToInt().toFloat())
                }
            }
            FormSection("Preview") {
                SettingsPreview(Modifier.padding(12.dp))
            }
            if (wide) {
                FormSection("Sidebar", footer = AnnotatedString(sidebarLayout.summary)) {
                    PickerRow("Layout", sidebarLayout.title, SidebarLayout.entries.map { it.stored to it.title }, tag = "sidebar-layout") {
                        settings.put(Keys.sidebarLayout, it)
                    }
                }
                FormSection("Terminal", footer = AnnotatedString("Where a session's terminal opens. In a narrow window it takes the place of the messages.")) {
                    TerminalPlacement.entries.forEachIndexed { i, p ->
                        if (i > 0) FormDivider()
                        ChoiceRow(p.title, selected = placement == p.name) { settings.put(Keys.terminalPlacement, p.name) }
                    }
                }
            }
            FormSection {
                Text(
                    "Restore Defaults",
                    color = if (isDefault) palette.overlay else palette.accent,
                    style = Type.body,
                    modifier = Modifier.fillMaxWidth().clickable(enabled = !isDefault) {
                        setAppearance(Appearance.SYSTEM)
                        settings.put(Keys.interfaceFollowsSystem, true)
                        settings.put(Keys.interfaceTextSize, TextSize.DEFAULT_INTERFACE)
                        settings.put(Keys.messageTextSize, TextSize.DEFAULT_MESSAGES)
                        settings.put(Keys.terminalPlacement, TerminalPlacement.OVER.name)
                        settings.put(Keys.sidebarLayout, SidebarLayout.INBOX.stored)
                    }.padding(12.dp),
                )
            }
        }
    }
}

@Composable
fun ChoiceRow(title: String, selected: Boolean, onClick: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().clickable(onClick = onClick).padding(horizontal = 12.dp, vertical = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(title, style = Type.body, color = palette.text, modifier = Modifier.weight(1f))
        if (selected) Icon(painterResource(Icons.check), "Selected", Modifier.size(20.dp), tint = palette.accent)
    }
}

@Composable
fun SwitchRow(title: String, checked: Boolean, enabled: Boolean = true, onChange: (Boolean) -> Unit) {
    Row(
        Modifier.fillMaxWidth().clickable(enabled = enabled) { onChange(!checked) }.padding(horizontal = 12.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(title, style = Type.body, color = if (enabled) palette.text else palette.overlay, modifier = Modifier.weight(1f))
        Switch(
            checked, onChange, enabled = enabled,
            colors = SwitchDefaults.colors(
                checkedTrackColor = palette.accent, checkedThumbColor = palette.onAccent,
                uncheckedTrackColor = palette.surface0, uncheckedThumbColor = palette.overlay, uncheckedBorderColor = palette.surface1,
            ),
        )
    }
}

/** A text size slider, with the size it gives. */
@Composable
private fun SizeSlider(value: Float, range: ClosedFloatingPointRange<Float>, steps: Int, text: String, onChange: (Float) -> Unit) {
    Column(Modifier.padding(horizontal = 12.dp, vertical = 8.dp), verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Row {
            Text("Text size", style = Type.body, color = palette.text, modifier = Modifier.weight(1f))
            Text(text, style = Type.body, color = palette.subtext)
        }
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("A", style = Type.caption, color = palette.subtext)
            Slider(
                value, onChange, valueRange = range, steps = steps, modifier = Modifier.weight(1f),
                colors = SliderDefaults.colors(thumbColor = palette.accent, activeTrackColor = palette.accent, inactiveTrackColor = palette.surface0,
                    activeTickColor = palette.onAccent, inactiveTickColor = palette.surface1),
            )
            Text("A", style = Type.title, color = palette.subtext)
        }
    }
}
