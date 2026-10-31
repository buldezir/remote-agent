package dev.remoteagent.core

import dev.remoteagent.core.store.Storage
import dev.remoteagent.core.store.TokenStore
import java.io.File

/** The golden frames rad's tests write (protocol/fixtures); the path comes from Gradle. */
fun fixture(name: String): String = File(System.getProperty("fixtures") ?: "../../protocol/fixtures", name).readText()

/** The cases every client's shared formatting checks (protocol/vectors); the path comes from Gradle. */
fun vector(name: String): String = File(System.getProperty("vectors") ?: "../../protocol/vectors", name).readText()

class MemoryStorage : Storage {
    private val values = HashMap<String, String>()
    override fun get(key: String) = values[key]
    override fun set(key: String, value: String?) {
        if (value == null) values.remove(key) else values[key] = value
    }
}

class MemoryTokenStore : TokenStore {
    val tokens = HashMap<String, String>()
    override fun get(serverId: String) = tokens[serverId]
    override fun set(serverId: String, token: String) {
        tokens[serverId] = token
    }
    override fun delete(serverId: String) {
        tokens.remove(serverId)
    }
}
