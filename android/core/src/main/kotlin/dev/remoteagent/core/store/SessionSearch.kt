package dev.remoteagent.core.store

import dev.remoteagent.core.protocol.SearchResult
import dev.remoteagent.core.protocol.SearchResults
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlin.time.Duration
import kotlin.time.Duration.Companion.milliseconds

/**
 * Search across every server: finds sessions by what was said in them, and
 * by their title and branch (`ServerConnection.search`). A query of two
 * characters or more is searched, 250 ms after the last change, on every
 * connected server that can, at once; each server's results are merged in
 * as it answers (`mergeSearchResults`). An answer to an older query is
 * dropped. `connections` gives the servers to search, in the app's order.
 * Its state changes on `scope` (the app: the main thread). Mirrors
 * SessionSearch in RAKit and web/src/store/sessionSearch.ts.
 */
class SessionSearch(
    private val scope: CoroutineScope,
    private val debounce: Duration = 250.milliseconds,
    private val connections: () -> List<ServerConnection>,
) {
    /** A session found on a server. */
    data class Hit(val serverId: String, val result: SearchResult) {
        /** For the lists' keys: "<server id>/<session id>". */
        val id: String get() = "$serverId/${result.session.id}"
    }

    /** A server whose search failed, and why. */
    data class Failure(val serverId: String, val server: String, val message: String)

    private val _query = MutableStateFlow("")
    val query: StateFlow<String> = _query.asStateFlow()
    private val _includeArchived = MutableStateFlow(true)
    /** Archived sessions are searched too; on until turned off, for as long as the app runs. */
    val includeArchived: StateFlow<Boolean> = _includeArchived.asStateFlow()
    private val _active = MutableStateFlow(false)
    /** The query is long enough to search: the results show in place of the sessions. */
    val active: StateFlow<Boolean> = _active.asStateFlow()
    private val _hits = MutableStateFlow<List<Hit>>(emptyList())
    val hits: StateFlow<List<Hit>> = _hits.asStateFlow()
    private val _isSearching = MutableStateFlow(false)
    /** A search is due, or a server hasn't answered it yet. */
    val isSearching: StateFlow<Boolean> = _isSearching.asStateFlow()
    private val _failures = MutableStateFlow<List<Failure>>(emptyList())
    val failures: StateFlow<List<Failure>> = _failures.asStateFlow()
    private val _more = MutableStateFlow(false)
    /** A server found more sessions than it listed. */
    val more: StateFlow<Boolean> = _more.asStateFlow()
    private val _indexing = MutableStateFlow<List<String>>(emptyList())
    /** The names of the servers still indexing sessions an older rad wrote. */
    val indexing: StateFlow<List<String>> = _indexing.asStateFlow()
    private val _unsupported = MutableStateFlow<List<String>>(emptyList())
    /** The names of the connected servers whose rad can't search: it needs an update. */
    val unsupported: StateFlow<List<String>> = _unsupported.asStateFlow()

    private val _answered = MutableStateFlow(0)
    /** How many servers answered the search, failures aside: "no sessions match" only once one has. */
    val answered: StateFlow<Int> = _answered.asStateFlow()
    private val _noServers = MutableStateFlow(false)
    /** No server could be searched: none is connected, or none that searches. */
    val noServers: StateFlow<Boolean> = _noServers.asStateFlow()

    /** Counts the searches asked for: an answer to an older one is dropped. */
    private var generation = 0
    private var job: Job? = null

    fun setQuery(query: String) {
        if (query == _query.value) return
        _query.value = query
        changed()
    }

    fun setIncludeArchived(on: Boolean) {
        if (on == _includeArchived.value) return
        _includeArchived.value = on
        changed()
    }

    /** Empties the field, and the results with it. */
    fun clear() = setQuery("")

    private fun changed() {
        generation += 1
        job?.cancel()
        job = null
        val q = _query.value.trim()
        _active.value = isSearchable(q)
        if (!_active.value) {
            _hits.value = emptyList()
            _isSearching.value = false
            _failures.value = emptyList()
            _more.value = false
            _indexing.value = emptyList()
            _unsupported.value = emptyList()
            _answered.value = 0
            _noServers.value = false
            return
        }
        // The results so far stay until the first answer replaces them.
        _isSearching.value = true
        val gen = generation
        val archived = _includeArchived.value
        job = scope.launch {
            delay(debounce)
            search(gen, q, archived)
        }
    }

    private sealed interface Answer {
        class Found(val results: SearchResults) : Answer
        class Failed(val message: String) : Answer
    }

    private suspend fun search(gen: Int, query: String, archived: Boolean) {
        val connected = connections().filter { it.state.value == ConnectionState.Connected }
        val (targets, older) = connected.partition { it.info.value?.search == true }
        val answers = HashMap<String, Answer>()

        fun publish() {
            if (gen != generation) return
            val found = targets.mapNotNull { c -> (answers[c.server.value.id] as? Answer.Found)?.let { c to it.results } }
            _hits.value = mergeSearchResults(found.map { (c, r) -> r.results.map { Hit(c.server.value.id, it) } })
            _more.value = found.any { (_, r) -> r.more }
            _indexing.value = found.filter { (_, r) -> r.indexing }.map { (c, _) -> c.server.value.name }
            _failures.value = targets.mapNotNull { c ->
                (answers[c.server.value.id] as? Answer.Failed)?.let { Failure(c.server.value.id, c.server.value.name, it.message) }
            }
            _answered.value = found.size
            _noServers.value = targets.isEmpty() && older.isEmpty()
            _isSearching.value = answers.size < targets.size
        }

        _unsupported.value = older.map { it.server.value.name }
        if (targets.isEmpty()) {
            publish()
            return
        }
        // Children of the search's job, which a newer search cancels.
        coroutineScope {
            for (c in targets) launch {
                val answer = try {
                    Answer.Found(c.search(query, archived = archived, limit = LIMIT))
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    Answer.Failed(e.message ?: "The request failed")
                }
                if (gen != generation) return@launch
                answers[c.server.value.id] = answer
                publish()
            }
        }
    }

    companion object {
        /** Sessions to ask each server for. */
        const val LIMIT = 30
        /** The shortest query searched. */
        const val MIN_LENGTH = 2

        fun isSearchable(query: String) = query.trim().length >= MIN_LENGTH
    }
}

/**
 * The servers' results as one list: round-robin by rank, in the servers'
 * order (every server's first, then every second…), each server's own order
 * kept. Their scores can't be compared across servers.
 */
fun <T> mergeSearchResults(lists: List<List<T>>): List<T> {
    val merged = ArrayList<T>(lists.sumOf { it.size })
    val longest = lists.maxOfOrNull { it.size } ?: 0
    for (rank in 0 until longest) {
        for (list in lists) if (rank < list.size) merged += list[rank]
    }
    return merged
}
