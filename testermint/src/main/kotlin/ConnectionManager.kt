package com.productscience

import io.kubernetes.client.PortForward
import org.tinylog.kotlin.Logger
import java.net.ServerSocket
import java.util.concurrent.atomic.AtomicInteger

class ConnectionManager(
    private val serverSocket: ServerSocket,
    private val config: PortForwardConfig,
    private val portForwardResult: PortForward.PortForwardResult,
    private val namespace: String,
    private val localPort: Int
) {
    private val activeConnections = AtomicInteger(0)

    fun startHandling() {
        Logger.info("Starting to handle connections for ${config.serverType} on port $localPort")

        while (!Thread.currentThread().isInterrupted) {
            try {
                handleNewConnection()
            } catch (e: java.net.SocketException) {
                handleSocketException(e)
            } catch (e: Exception) {
                handleGeneralException(e)
            }
        }

        Logger.info("Connection handler for ${config.serverType} on port $localPort is shutting down")
    }

    private fun handleNewConnection() {
        val socket = serverSocket.accept()
        val connectionCount = activeConnections.incrementAndGet()

        Logger.info("Accepted connection for ${config.serverType} on port $localPort (active: $connectionCount)")

        if (connectionCount > config.maxConnections) {
            Logger.warn("Too many active connections for ${config.serverType}: $connectionCount > ${config.maxConnections}")
        }

        Thread {
            try {
                PortForwardConnection(socket, config.serverType, config.remotePort, portForwardResult, namespace).handle()
            } finally {
                val remaining = activeConnections.decrementAndGet()
                Logger.info("Connection for ${config.serverType} completed (active: $remaining)")
            }
        }.apply {
            name = "Connection-${config.serverType}-$localPort-${System.currentTimeMillis()}"
            isDaemon = true
            start()
        }
    }

    private fun handleSocketException(e: java.net.SocketException) {
        if (!Thread.currentThread().isInterrupted && !serverSocket.isClosed) {
            Logger.error("Socket exception accepting connection for ${config.serverType}: ${e.message}")
        }
    }

    private fun handleGeneralException(e: Exception) {
        if (!Thread.currentThread().isInterrupted) {
            Logger.error("Error accepting connection for ${config.serverType}: ${e.message}")
            Logger.error(e, "Stack trace for connection acceptance error")

            try {
                Thread.sleep(1000)
            } catch (ie: InterruptedException) {
                Thread.currentThread().interrupt()
                return
            }
        }
    }
}
