/*
package com.productscience

import org.tinylog.core.LogEntry
import org.tinylog.core.LogEntryValue
import org.tinylog.writers.FileWriter
import org.tinylog.writers.Writer
import java.util.HashMap

class TestFilesWriter(val properties: java.util.Map<String, String>) : Writer {
    private val writers = mutableMapOf<String, FileWriter>()
    override fun getRequiredLogEntryValues(): Collection<LogEntryValue?>? {
    return writers.get("")!!.requiredLogEntryValues
    }

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
*/

package com.productscience;

import org.tinylog.core.LogEntry;
import org.tinylog.core.LogEntryValue;
import org.tinylog.writers.FileWriter;
import org.tinylog.writers.Writer;

import java.io.IOException;
import java.util.Collection;
import java.util.HashMap;
import java.util.Map;

public class TestFiles2Writer implements Writer {
    private final Map<String, String> properties;
    private final Map<String, FileWriter> writers = new HashMap<>();

    public TestFiles2Writer(Map<String, String> properties) {
        this.properties = properties;
    }

    @Override
    public Collection<LogEntryValue> getRequiredLogEntryValues() {
        return writers.get("").getRequiredLogEntryValues();
    }

    @Override
    public void write(LogEntry logEntry) throws IOException {
        if (logEntry != null && currentTest != null) {
            FileWriter writer = writers.computeIfAbsent(currentTest, key -> {
                String path = properties.getOrDefault("path", "./");
                Map<String, String> thisProperties = new HashMap<>(properties);
                thisProperties.put("file", path + currentTest + ".log");
                try {
                    return new FileWriter(thisProperties);
                } catch (IOException e) {
                    throw new RuntimeException(e);
                }
            });
            writer.write(logEntry);
        }
    }

    @Override
    public void flush() throws IOException {
        for (FileWriter writer : writers.values()) {
            try {
                writer.flush();
            } catch (IOException e) {
                throw new RuntimeException("Failed to flush writer", e);
            }
        }
    }

    @Override
    public void close() {
        writers.values().forEach(writer -> {
            try {
                writer.close();
            } catch (IOException e) {
                throw new RuntimeException("Failed to close writer", e);
            }
        });
        writers.clear();
    }

    public static String currentTest = null;
}
