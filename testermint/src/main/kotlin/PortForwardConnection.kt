package com.productscience

import io.kubernetes.client.PortForward
import io.kubernetes.client.util.Streams
import org.tinylog.ThreadContext
import org.tinylog.kotlin.Logger
import java.io.IOException
import java.net.Socket
import java.util.concurrent.atomic.AtomicBoolean

class PortForwardConnection(
    private val socket: Socket,
    private val serverType: String,
    private val remotePort: Int,
    private val portForwardResult: PortForward.PortForwardResult,
    private val namespace: String
) {
    private val socketClosed = AtomicBoolean(false)
    private val streamState = SocketStreamState()

    fun handle() {
        configureSocket()
        startStreamThreads()
    }

    private fun configureSocket() {
        socket.apply {
            keepAlive = true
            soTimeout = 120000
            tcpNoDelay = true
        }
        Logger.info("Configured socket for keep-alive for $serverType")
    }

    private fun startStreamThreads() {
        startOutboundThread()
        startInboundThread()
    }

    private fun startOutboundThread() {
        Thread {
            ThreadContext.put("pair", namespace)
            ThreadContext.put("source", "k8s")
            handleOutboundStream()
        }.apply {
            name = "OutboundStream-$serverType-${System.currentTimeMillis()}"
            isDaemon = true
            start()
        }
    }

    private fun startInboundThread() {
        Thread {
            ThreadContext.put("pair", namespace)
            ThreadContext.put("source", "k8s")
            handleInboundStream()
        }.apply {
            name = "InboundStream-$serverType-${System.currentTimeMillis()}"
            isDaemon = true
            start()
        }
    }

    private fun handleOutboundStream() {
        try {
            Streams.copy(socket.getInputStream(), portForwardResult.getOutboundStream(remotePort))
            Logger.info("Outbound stream completed normally for $serverType")
            streamState.setOutboundCompleted()
        } catch (e: InterruptedException) {
            Thread.currentThread().interrupt()
            Logger.info("Outbound stream thread interrupted for $serverType")
            streamState.setOutboundError()
        } catch (e: IOException) {
            handleStreamError("outbound", e)
        } catch (e: Exception) {
            handleStreamError("outbound", e, unexpected = true)
        }
    }

    private fun handleInboundStream() {
        try {
            Streams.copy(portForwardResult.getInputStream(remotePort), socket.getOutputStream())
            Logger.info("Inbound stream completed normally for $serverType")
            streamState.setInboundCompleted()
        } catch (e: InterruptedException) {
            Thread.currentThread().interrupt()
            Logger.info("Inbound stream thread interrupted for $serverType")
            streamState.setInboundError()
        } catch (e: IOException) {
            handleStreamError("inbound", e)
        } catch (e: Exception) {
            handleStreamError("inbound", e, unexpected = true)
        }
    }

    private fun handleStreamError(streamType: String, e: Exception, unexpected: Boolean = false) {
        if (!socketClosed.get()) {
            val errorType = if (unexpected) "Unexpected error" else "Error"
            Logger.error("$errorType in $streamType stream for $serverType: ${e.message}")

            if (streamType == "outbound") {
                streamState.setOutboundError()
            } else {
                streamState.setInboundError()
            }

            if (streamState.shouldCloseSocket()) {
                closeSocketSafely("$streamType stream error")
            }
        }
    }

    private fun closeSocketSafely(reason: String) {
        if (socketClosed.compareAndSet(false, true)) {
            try {
                Logger.info("Closing socket due to $reason for $serverType")
                socket.close()
            } catch (e: Exception) {
                Logger.error("Error closing socket: ${e.message}")
            }
        }
    }
}

class SocketStreamState(
    private val outboundCompleted: AtomicBoolean = AtomicBoolean(false),
    private val inboundCompleted: AtomicBoolean = AtomicBoolean(false),
    private val outboundError: AtomicBoolean = AtomicBoolean(false),
    private val inboundError: AtomicBoolean = AtomicBoolean(false)
) {
    fun setOutboundCompleted() = outboundCompleted.set(true)
    fun setInboundCompleted() = inboundCompleted.set(true)
    fun setOutboundError() = outboundError.set(true)
    fun setInboundError() = inboundError.set(true)

    fun shouldCloseSocket(): Boolean {
        if (outboundError.get() && inboundError.get()) {
            return true // Both streams have errors
        }

        if ((outboundError.get() && inboundCompleted.get()) ||
            (inboundError.get() && outboundCompleted.get())) {
            return true // One stream has error, other completed
        }

        if (outboundCompleted.get() && inboundCompleted.get()) {
            return false // Both streams completed normally
        }

        return false // One stream has error but other is still active
    }
}
