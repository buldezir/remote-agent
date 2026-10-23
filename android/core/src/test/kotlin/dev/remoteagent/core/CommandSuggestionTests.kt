package dev.remoteagent.core

import dev.remoteagent.core.protocol.SlashCommand
import dev.remoteagent.core.protocol.commandQuery
import dev.remoteagent.core.protocol.commandSuggestions
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

class CommandSuggestionTests {
    @Test fun matchesOnlyLeadingCommandToken() {
        val commands = listOf(SlashCommand("compact"), SlashCommand("skill:prototype", "Answer a design question"))
        assertEquals(commands, commandSuggestions("/", commands))
        assertEquals(listOf(commands[1]), commandSuggestions("/PROT", commands))
        assertEquals(listOf(commands[1]), commandSuggestions("/design", commands))
        assertEquals(commands, commandSuggestions("/", commands + commands))
        assertEquals(emptyList(), commandSuggestions("/missing", commands))
        for (text in listOf("", "write /prot", " /prot", "/prototype ", "/prototype\n", "/prototype\targs")) {
            assertNull(commandQuery(text))
            assertEquals(emptyList(), commandSuggestions(text, commands))
        }
    }
    @Test fun ranksNamesBeforeDescriptions() {
        val catalog = listOf(SlashCommand("code-check", "Review code"), SlashCommand("preview"), SlashCommand("review-long"), SlashCommand("review"))
        assertEquals(listOf("review", "review-long", "preview", "code-check"), commandSuggestions("/review", catalog).map { it.name })
    }

}
