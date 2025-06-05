package com.productscience

import com.productscience.analyzer.LogAnalyzer
import java.io.File


class LogAnalyzerSession {
    private var currentAnalyzer: LogAnalyzer? = null
    private var currentLogFile: File? = null

    fun loadLog(file: File): LogAnalyzer {
        currentLogFile = file
        currentAnalyzer = LogAnalyzer(file)
        return currentAnalyzer!!
    }

    fun getCurrentAnalyzer(): LogAnalyzer {
        return currentAnalyzer ?: throw IllegalStateException("No log file loaded")
    }

    fun getCurrentLogFile(): File? = currentLogFile

    fun isLogLoaded(): Boolean = currentAnalyzer != null

    fun clearSession() {
        currentAnalyzer = null
        currentLogFile = null
    }
}