package com.productscience.analyzer

import org.jetbrains.exposed.dao.id.IntIdTable
import org.jetbrains.exposed.sql.*
import org.jetbrains.exposed.sql.javatime.time
import org.jetbrains.exposed.sql.transactions.transaction
import org.jetbrains.exposed.sql.addLogger
import org.jetbrains.exposed.sql.StdOutSqlLogger
import java.io.File

/**
 * Table definition for log entries
 */
object LogEntries : IntIdTable() {
    val timestamp = time("timestamp")
    val level = varchar("level", 10)
    val context = varchar("context", 255).nullable()
    val service = varchar("service", 50)
    val pair = varchar("pair", 100)
    val message = text("message")
    val subsystem = varchar("subsystem", 100).nullable()
}

/**
 * Class for loading log entries into an SQLite database
 */
class LogDatabase {
    private val parser = LogParser()
    private val db: Database
    private val tempDirectory: File
    private val dbFile: File

    init {
        // Create a temporary directory for the database
        tempDirectory = File(System.getProperty("java.io.tmpdir"), "log-analyzer-${System.currentTimeMillis()}")
        if (!tempDirectory.exists()) {
            tempDirectory.mkdirs()
        }

        // Define the path for the SQLite database in the temp directory
        dbFile = File(tempDirectory, "logs.db")
        
        // Connect to a file-based SQLite database in the temp directory
        db = Database.connect("jdbc:sqlite:${dbFile.absolutePath}", "org.sqlite.JDBC")

        // Initialize database schema
        transaction(db) {
            addLogger(StdOutSqlLogger)
            SchemaUtils.drop(LogEntries)
            SchemaUtils.create(LogEntries)
        }
        
        // Register shutdown hook to clean up temp files
        Runtime.getRuntime().addShutdownHook(Thread {
            try {
                tempDirectory.deleteRecursively()
                println("Cleaned up temp directory: ${tempDirectory.absolutePath}")
            } catch (e: Exception) {
                System.err.println("Failed to clean up temp directory: ${e.message}")
            }
        })
        
        println("Database initialized at: ${dbFile.absolutePath}")
    }

    /**
     * Load a log file into the database
     *
     * @param file The log file to load
     * @return The number of entries loaded
     */
    fun loadLogFile(file: File): Int {
        val entries = parser.parseLogFile(file)

        transaction(db) {
            entries.forEach { entry ->
                LogEntries.insert {
                    it[timestamp] = entry.timestamp
                    it[level] = entry.level
                    it[context] = entry.context
                    it[service] = entry.service
                    it[pair] = entry.pair
                    it[message] = entry.message
                    it[subsystem] = entry.subsystem
                }
            }
        }

        return entries.size
    }

    /**
     * Get the total number of log entries
     *
     * @return The total number of log entries
     */
    fun getTotalLines(): Int = transaction(db) {
        LogEntries.selectAll().count().toInt()
    }

    /**
     * Get the number of log entries with a specific level
     *
     * @param level The log level to count
     * @return The number of log entries with the specified level
     */
    fun getCountByLevel(level: String): Int = transaction(db) {
        LogEntries.select { LogEntries.level eq level }.count().toInt()
    }

    /**
     * Get the number of error log entries
     *
     * @return The number of error log entries
     */
    fun getErrorCount(): Int = getCountByLevel("ERROR")

    /**
     * Get the number of warning log entries
     *
     * @return The number of warning log entries
     */
    fun getWarnCount(): Int = getCountByLevel("WARN")

    /**
     * Execute a custom SQL query
     *
     * @param query The SQL query to execute
     * @return The query result as a list of maps
     */
    fun executeQuery(query: String): List<Map<String, Any?>> = transaction(db) {
        exec(query) { rs ->
            val result = mutableListOf<Map<String, Any?>>()
            val metaData = rs.metaData
            val columnCount = metaData.columnCount

            while (rs.next()) {
                val row = mutableMapOf<String, Any?>()
                for (i in 1..columnCount) {
                    val columnName = metaData.getColumnName(i)
                    row[columnName] = rs.getObject(i)
                }
                result.add(row)
            }

            result
        } ?: emptyList()
    }
}
