package com.productscience

import com.github.dockerjava.api.async.ResultCallback
import com.github.dockerjava.api.model.Frame
import org.tinylog.ThreadContext
import org.tinylog.kotlin.Logger
import java.time.Instant
import java.time.format.DateTimeParseException

fun <T> logContext(context: Map<String, String>, block: () -> T): T {
    val outerContext = ThreadContext.getMapping()
    context.forEach {
        ThreadContext.put(it.key, it.value)
    }
    val result = block()
    context.keys.forEach {
        ThreadContext.remove(it)
    }
    outerContext.forEach {
        ThreadContext.put(it.key, it.value)
    }
    return result
}

interface HasConfig {
    val config: ApplicationConfig
    fun <T> wrapLog(operation: String, infoLevel: Boolean, block: () -> T): T =
        logContext(
            mapOf(
                "operation" to operation,
                "pair" to config.pairName,
                "source" to "testermint"
            )
        ) {
            if (infoLevel) {
                Logger.info("Start operation={}", operation)
            } else {
                Logger.debug("Start operation={}", operation)
            }
            val result = block()
            if (infoLevel) {
                Logger.info("End operation={}", operation)
            } else {
                Logger.debug("End operation={}", operation)
            }
            result
        }
}

class LogOutput(val name: String, val type: String) : ResultCallback.Adapter<Frame>() {
    var currentHeight = 0L
    val currentMessage = StringBuilder()
    val currentTimestamp: Instant? = null

    override fun onNext(frame: Frame) = logContext(
        mapOf(
            "operation" to type,
            "pair" to name,
            "source" to "container",
            "blockHeight" to currentHeight.toString()
        )
    ) {
        val logEntry = String(frame.payload).trim()
        val timestamp = extractTimestamp(logEntry)
        if (timestamp != null) {
            val entryWithoutTimestamp = logEntry.replaceFirst(timestampPattern, "").trim()
            if (currentMessage.isNotEmpty()) {
                log(currentMessage.toString())
                currentMessage.clear()
            }
            if (frame.payload.size < 1000) {
                log(entryWithoutTimestamp)
            } else {
                currentMessage.append(entryWithoutTimestamp)
            }
        } else {
            currentMessage.append(logEntry)
            if (frame.payload.size < 1000) {
                log(currentMessage.toString())
                currentMessage.clear()
            }
        }
        Unit
    }

    private fun log(logEntry: String) {
        if (logEntry.contains("committed state")) {
            // extract out height=123

            "height=?.+\\[0m(\\d+)".toRegex().find(logEntry)?.let {
                val height = it.groupValues[1].toLong()
                if (height > currentHeight) {
                    Logger.info("New block, height={}", height)
                    currentHeight = height
                }
            }
        }
        val warn = "WRN\u001B"
        if (logEntry.contains("INFO+")) {
            Logger.info(logEntry)
        } else if (logEntry.contains("INF ") || logEntry.contains(" INFO ")) {
            // We map this to debug as there is a LOT of info level logs
            Logger.debug(logEntry)
        } else if (logEntry.contains("ERR") || logEntry.contains(" ERROR ")) {
            Logger.error(logEntry)
        } else if (logEntry.contains("DBG ") || logEntry.contains(" DEBUG ")) {
            Logger.debug(logEntry)
        } else if (logEntry.contains(warn) || logEntry.contains(" WARN ")) {
            Logger.warn(logEntry)
        } else {
            Logger.trace(logEntry)
        }
    }
}

val timestampPattern = "^\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}\\.\\d{9}Z".toRegex()

fun extractTimestamp(entireLine: String): Instant? {
    val matchResult = timestampPattern.find(entireLine)
    return if (matchResult != null) {
        try {
            Instant.parse(matchResult.value)
        } catch (e: DateTimeParseException) {
            null
        }
    } else {
        null
    }
}

class ExecCaptureOutput : ResultCallback.Adapter<Frame>() {
    val output = mutableListOf<String>()
    override fun onNext(frame: Frame) {
        output.add(String(frame.payload).trim())
    }
}

