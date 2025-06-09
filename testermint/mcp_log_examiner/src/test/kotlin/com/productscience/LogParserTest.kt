package com.productscience

import com.productscience.analyzer.LogParser
import org.junit.jupiter.api.Test
import org.assertj.core.api.Assertions.assertThat
import java.time.LocalTime
import java.time.format.DateTimeFormatter

class LogParserTest {

    @Test
    fun `should parse log line with colored subsystem`() {
        val parser = LogParser()

        // Test log line with colored subsystem format
        val logLine = "02:44:56.045 INFO  - node:pair=/join2    \"[1mAdding new account directly[0m [36maddress=[0mgonka1tdjlj0nwv3qtf7nk70ls5nevsgx9ywz8yufmqs [36mmodule=[0mx/inference [36msubsystem=[0mParticipants\" operation=base"
        val logEntry = parser.parseLine(logLine)

        // Verify that the log entry is parsed correctly
        assertThat(logEntry).isNotNull()
        assertThat(logEntry?.level).isEqualTo("INFO")
        assertThat(logEntry?.service).isEqualTo("node")
        assertThat(logEntry?.pair).isEqualTo("join2")
        assertThat(logEntry?.subsystem).isEqualTo("Participants")

        // Verify that ANSI codes are stripped from the message
        assertThat(logEntry?.message).doesNotContain("[1m")
        assertThat(logEntry?.message).doesNotContain("[0m")
        assertThat(logEntry?.message).doesNotContain("[36m")

        // Verify the clean message content
        assertThat(logEntry?.message).contains("Adding new account directly")
        assertThat(logEntry?.message).contains("address=")
        assertThat(logEntry?.message).contains("module=")
        assertThat(logEntry?.message).contains("subsystem=")
        assertThat(logEntry?.message).contains("Participants")
    }

    @Test
    fun `should parse log line with plain text subsystem`() {
        val parser = LogParser()

        // Test log line with plain text subsystem format
        val logLinePlainSubsystem = "02:44:53.958 INFO  - dapi:pair=/join2    \"Adding event to queue subsystem=EventProcessing type=\"\" id=1\" operation=base"
        val logEntryPlainSubsystem = parser.parseLine(logLinePlainSubsystem)

        // Verify that the log entry with plain text subsystem is parsed correctly
        assertThat(logEntryPlainSubsystem).isNotNull()
        assertThat(logEntryPlainSubsystem?.level).isEqualTo("INFO")
        assertThat(logEntryPlainSubsystem?.service).isEqualTo("dapi")
        assertThat(logEntryPlainSubsystem?.pair).isEqualTo("join2")
        assertThat(logEntryPlainSubsystem?.subsystem).isEqualTo("EventProcessing")
    }

    @Test
    fun `should parse log line without subsystem`() {
        val parser = LogParser()

        // Test log line without subsystem
        val logLineNoSubsystem = "02:44:56.045 INFO  - node:pair=/join2    \"[1mAdding new account directly[0m [36maddress=[0mgonka1tdjlj0nwv3qtf7nk70ls5nevsgx9ywz8yufmqs [36mmodule=[0mx/inference\" operation=base"
        val logEntryNoSubsystem = parser.parseLine(logLineNoSubsystem)

        // Verify that the log entry is parsed correctly
        assertThat(logEntryNoSubsystem).isNotNull()
        assertThat(logEntryNoSubsystem?.subsystem).isNull()
    }

    @Test
    fun `should handle multi-line log entries`() {
        val parser = LogParser()

        // Create a temporary file with multi-line log entries
        val tempFile = createTempFile()
        tempFile.writeText("""
            02:48:05.755 ERROR - test:pair=/join3    "Test failed:power to zero removes participant from validators(): com.google.gson.JsonSyntaxException: java.lang.IllegalStateException: Expected BEGIN_OBJECT but was STRING at line 1 column 1 path ${'$'}" operation=base
                at com.google.gson.internal.bind.ReflectiveTypeAdapterFactory${'$'}Adapter.read(ReflectiveTypeAdapterFactory.java:395)
                at com.google.gson.Gson.fromJson(Gson.java:1214)
                at com.google.gson.Gson.fromJson(Gson.java:1124)
            02:48:06.755 INFO  - test:pair=/join3    "Another log entry" operation=base
        """.trimIndent())

        try {
            // Parse the file
            val logEntries = parser.parseLogFile(tempFile)

            // Verify that we have two log entries
            assertThat(logEntries).hasSize(2)

            // Verify that the first log entry contains the multi-line message
            val firstEntry = logEntries[0]
            assertThat(firstEntry.level).isEqualTo("ERROR")
            assertThat(firstEntry.service).isEqualTo("test")
            assertThat(firstEntry.pair).isEqualTo("join3")
            assertThat(firstEntry.message).contains("Test failed:power to zero removes participant from validators()")
            assertThat(firstEntry.message).contains("at com.google.gson.internal.bind.ReflectiveTypeAdapterFactory")
            assertThat(firstEntry.message).contains("at com.google.gson.Gson.fromJson(Gson.java:1214)")
            assertThat(firstEntry.message).contains("at com.google.gson.Gson.fromJson(Gson.java:1124)")

            // Verify that the second log entry is parsed correctly
            val secondEntry = logEntries[1]
            assertThat(secondEntry.level).isEqualTo("INFO")
            assertThat(secondEntry.service).isEqualTo("test")
            assertThat(secondEntry.pair).isEqualTo("join3")
            assertThat(secondEntry.message).isEqualTo("Another log entry")
        } finally {
            // Clean up
            tempFile.delete()
        }
    }
    @Test
    fun `should parse log line with pair equals none`() {
        val parser = LogParser()

        // Test log line with pair=none format from the issue description
        val logLine = "01:52:11.867 INFO - test:pair=none \"TestSection:Found cluster, initializing\" operation=base"
        val logEntry = parser.parseLine(logLine)

        // Verify that the log entry is parsed correctly
        assertThat(logEntry).isNotNull()
        assertThat(logEntry?.timestamp).isEqualTo(LocalTime.parse("01:52:11.867", DateTimeFormatter.ofPattern("HH:mm:ss.SSS")))
        assertThat(logEntry?.level).isEqualTo("INFO")
        assertThat(logEntry?.service).isEqualTo("test")
        assertThat(logEntry?.pair).isEqualTo("none")
        assertThat(logEntry?.message).isEqualTo("TestSection:Found cluster, initializing")
    }
}
