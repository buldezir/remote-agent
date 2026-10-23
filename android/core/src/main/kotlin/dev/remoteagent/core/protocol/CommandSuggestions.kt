package dev.remoteagent.core.protocol

/** Only complete the leading command token; arguments and prose stay untouched. */
fun commandQuery(draft: String): String? =
    if (draft.startsWith("/") && draft.none { it.isWhitespace() }) draft.drop(1).lowercase() else null

fun commandSuggestions(draft: String, commands: List<SlashCommand>): List<SlashCommand> {
    val query = commandQuery(draft) ?: return emptyList()
    return commands.distinctBy { it.name }.mapNotNull { command ->
        val name = command.name.lowercase()
        val short = name.substringAfterLast(':')
        val rank = when {
            name == query || short == query -> 0
            name.startsWith(query) || short.startsWith(query) -> 1
            name.contains(query) -> 2
            command.description?.lowercase()?.contains(query) == true -> 3
            else -> return@mapNotNull null
        }
        command to rank
    }.sortedBy { it.second }.map { it.first }
}
