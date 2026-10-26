package dev.remoteagent.core

import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit

/** The server end of /v1/ws, answering calls the way rad does. */
class FakeRad : WebSocketListener() {
    val received = LinkedBlockingQueue<JsonObject>()
    @Volatile var socket: WebSocket? = null
    /** The result for a call; null leaves it unanswered. Runs on OkHttp's thread. */
    @Volatile var answer: (FakeRad, String, JsonObject) -> JsonElement? = { _, method, _ ->
        when (method) {
            "server.info" -> serverInfo
            "slow" -> null
            else -> JsonObject(emptyMap())
        }
    }

    override fun onOpen(webSocket: WebSocket, response: Response) {
        socket = webSocket
    }

    override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
        webSocket.close(code, null)
    }

    /** Ends the server's side for good, so MockWebServer can shut down. */
    fun shutDown() {
        runCatching { socket?.close(1001, null) }
    }

    override fun onMessage(webSocket: WebSocket, text: String) {
        val o = kotlinx.serialization.json.Json.parseToJsonElement(text).jsonObject
        received.put(o)
        val method = o.getValue("method").jsonPrimitive.content
        val result = answer(this, method, o["params"]?.jsonObject ?: JsonObject(emptyMap())) ?: return
        webSocket.send(buildJsonObject {
            put("id", o.getValue("id"))
            if (result is JsonObject && result["error"] != null) put("error", result.getValue("error")) else put("result", result)
        }.toString())
    }

    fun notify(method: String, params: JsonObject) {
        socket!!.send(buildJsonObject {
            put("method", method)
            put("params", params)
        }.toString())
    }

    /** The next call with this method, waiting for it. */
    fun nextCall(method: String, seconds: Long = 5): JsonObject {
        val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(seconds)
        while (true) {
            val left = deadline - System.nanoTime()
            check(left > 0) { "no $method call" }
            val o = received.poll(left, TimeUnit.NANOSECONDS) ?: continue
            if (o.getValue("method").jsonPrimitive.content == method) return o
        }
    }

    companion object {
        val serverInfo = buildJsonObject {
            put("serverId", "srv")
            put("name", "Test")
            put("protocolVersion", 1)
            put("version", "0.1")
            put("terminal", true)
        }

        fun error(code: String, message: String) = buildJsonObject {
            put("error", buildJsonObject {
                put("code", code)
                put("message", message)
            })
        }

        fun project(id: String) = buildJsonObject {
            put("id", id)
            put("path", "/src/$id")
            put("name", id)
            put("isGitRepo", true)
            put("createdAt", "2026-10-09T12:00:00Z")
        }

        fun session(id: String, updatedAt: String = "2026-10-09T12:00:00Z", archived: Boolean = false, unread: Boolean = false) = buildJsonObject {
            put("id", id)
            put("projectId", "p1")
            put("harness", "fake")
            put("workspace", buildJsonObject {
                put("kind", "root")
                put("path", "/src/p1")
            })
            put("status", "idle")
            put("title", "Session $id")
            put("archived", archived)
            // As rad: left out unless set.
            if (unread) put("unread", true)
            put("createdAt", "2026-10-09T12:00:00Z")
            put("updatedAt", updatedAt)
        }

        fun events(stream: String, vararg events: Pair<Long, JsonObject>) = buildJsonObject {
            put("stream", stream)
            put("events", buildJsonArray {
                for ((seq, ev) in events) {
                    add(JsonObject(ev + mapOf("stream" to JsonPrimitive(stream), "seq" to JsonPrimitive(seq))))
                }
            })
        }

        fun upserted(kind: String, payload: JsonObject) = buildJsonObject {
            put("type", "$kind.upserted")
            put(kind, payload)
            put("ts", "2026-10-09T12:00:00Z")
        }
    }
}

fun MockWebServer.base() = "http://$hostName:$port"

fun MockWebServer.upgrade(rad: FakeRad) = enqueue(MockResponse.Builder().webSocketUpgrade(rad).build())

/** An address nothing listens on. */
const val DEAD_URL = "http://127.0.0.1:1"
