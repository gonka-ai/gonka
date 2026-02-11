package com.productscience.mockserver.service

import java.util.concurrent.CopyOnWriteArrayList

/**
 * Service that records the value of the HTTP Host header for each incoming request.
 *
 * Thread-safe and lightweight. Keeps a bounded in-memory history to avoid unbounded growth.
 */
class HostHeaderService(
    private val maxHistory: Int = 1000
) {
    private val history = CopyOnWriteArrayList<String>()

    /**
     * Record a Host header value. Null values are recorded as an empty string for visibility.
     */
    fun record(host: String?) {
        val value = host ?: ""
        history.add(value)
        // Trim to maxHistory if needed
        if (history.size > maxHistory) {
            val overflow = history.size - maxHistory
            // Remove oldest entries
            repeat(overflow) { _ ->
                if (history.isNotEmpty()) history.removeAt(0)
            }
        }
    }

    /** Returns a snapshot of the recorded Host values (oldest first). */
    fun getAll(): List<String> = history.toList()

    /** Returns the most recent Host value if present. */
    fun getLast(): String? = history.lastOrNull()
}
