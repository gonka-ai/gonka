package com.productscience

import org.tinylog.core.LogEntry
import org.tinylog.writers.AbstractFormatPatternWriter
import org.tinylog.writers.FileWriter

class TestFilesWriter(val properties: java.util.Map<String, String>) :
    AbstractFormatPatternWriter(properties as Map<String?, String?>?) {
    private val writers = mutableMapOf<String, FileWriter>()

    override fun write(logEntry: LogEntry?) {
        if (logEntry != null && currentTest != null) {
            val writer = writers.getOrPut(currentTest!!) {
                val path = properties.get("path") ?: "./"
                val thisProperties = HashMap(properties as Map<String, String>)
                thisProperties["file"] = path + currentTest + ".log"
                FileWriter(thisProperties)
            }
            writer.write(logEntry)
        }
    }

    override fun flush() {
        writers.values.forEach { it.flush() }
    }

    override fun close() {
        writers.values.forEach { it.close() }
        writers.clear()
    }

    companion object {
        var currentTest: String? = null
    }
}
