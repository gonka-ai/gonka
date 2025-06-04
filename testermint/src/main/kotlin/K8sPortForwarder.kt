package com.productscience

import io.kubernetes.client.PortForward
import io.kubernetes.client.openapi.apis.CoreV1Api
import io.kubernetes.client.util.Streams
import org.tinylog.ThreadContext
import org.tinylog.kotlin.Logger
import java.io.IOException
import java.net.ServerSocket
import java.net.Socket
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger

/**
 * Class responsible for managing Kubernetes port forwarding.
 * This class handles setting up port forwarding, creating server sockets,
 * and managing connections to forwarded ports.
 */
class K8sPortForwarder {
    // Map of port types to their port numbers
    private val portTypeToNumber = mapOf(
        SERVER_TYPE_PUBLIC to 9000,
        SERVER_TYPE_ML to 9100,
        SERVER_TYPE_ADMIN to 9200
    )

    // Shared PortForward instances for all pods
    private val portForwardInstances = ConcurrentHashMap<String, PortForward>()

    // Track active server sockets for cleanup
    private val serverSockets = ConcurrentHashMap<String, ServerSocket>()

    /**
     * Sets up port forwarding for the API pod using the Kubernetes Java client's PortForward class.
     *
     * @param coreV1Api The Kubernetes CoreV1Api client (not used in this implementation but kept for API compatibility)
     * @param namespace The namespace of the API pod
     * @param apiPodName The name of the API pod
     * @return A map of server types to URLs
     */
    fun setupPortForwarding(
        coreV1Api: CoreV1Api,
        namespace: String,
        apiPodName: String
    ): Map<String, String> {
        val apiUrls = mutableMapOf<String, String>()
        val portForwardResults = mutableMapOf<Int, PortForward.PortForwardResult>()

        return logContext(mapOf("pair" to namespace, "source" to "k8s")) {
            // Set up port forwarding for each port type
            for ((serverType, remotePort) in portTypeToNumber) {
                try {
                    // Find a free local port
                    val localPort = findFreePort()

                    Logger.info("Setting up port forwarding for $serverType: localhost:$localPort -> $apiPodName:$remotePort")

                    // Set up port forwarding and create server socket
                    val result = setupPortForwardForPort(namespace, apiPodName, remotePort)
                    portForwardResults[remotePort] = result

                    // Create and start server socket for handling connections
                    val serverSocket = createServerSocket(localPort, serverType, remotePort, result, namespace)

                    // Store the server socket for cleanup
                    val socketKey = "$namespace-$apiPodName-$serverType"
                    serverSockets[socketKey] = serverSocket

                    // Create URL for the forwarded port
                    apiUrls[serverType] = "http://localhost:$localPort"
                    Logger.info("Port forwarding set up for $serverType: localhost:$localPort -> $apiPodName:$remotePort")

                } catch (e: Exception) {
                    Logger.error("Failed to set up port forwarding for $serverType: ${e.message}")
                    // Use a fallback URL that points to the pod directly
                    apiUrls[serverType] = "http://$apiPodName.$namespace.svc.cluster.local:$remotePort"
                    Logger.info("Using fallback URL for $serverType: ${apiUrls[serverType]}")
                }
            }
            apiUrls
        }
    }

    /**
     * Sets up port forwarding for a specific port.
     *
     * @param namespace The namespace of the API pod
     * @param podName The name of the API pod
     * @param remotePort The remote port to forward
     * @return The PortForwardResult object
     */
    private fun setupPortForwardForPort(
        namespace: String,
        podName: String,
        remotePort: Int
    ): PortForward.PortForwardResult {
        // Create a list of ports to forward
        return logContext(mapOf("pair" to namespace, "source" to "k8s")) {
            val ports = ArrayList<Int>()
            ports.add(remotePort)

            // Create a key for the portForwardInstances map
            val key = "$namespace-$podName"

            // Get or create a PortForward instance
            val portForward = portForwardInstances.computeIfAbsent(key) {
                Logger.info("Creating new PortForward instance for $namespace/$podName")
                PortForward()
            }

            // Set up port forwarding
            val result = portForward.forward(namespace, podName, ports)
            Logger.info("Forwarding established for port $remotePort using shared PortForward instance")
            result
        }
    }

    /**
     * Creates a server socket for handling connections to the forwarded port.
     *
     * @param localPort The local port to bind the server socket to
     * @param serverType The type of server (public, ml, admin)
     * @param remotePort The remote port being forwarded
     * @param portForwardResult The PortForwardResult object
     * @param namespace The namespace of the pod
     * @return The created ServerSocket
     */
    private fun createServerSocket(
        localPort: Int,
        serverType: String,
        remotePort: Int,
        portForwardResult: PortForward.PortForwardResult,
        namespace: String
    ): ServerSocket {
        // Create a server socket to accept connections with a larger backlog
        val serverSocket = ServerSocket(localPort, 50)

        // Configure the server socket for reuse
        serverSocket.reuseAddress = true

        Logger.info("Created server socket for $serverType on port $localPort with reuse address enabled")

        // Start a thread to handle connections
        Thread {
            ThreadContext.put("pair", namespace)
            ThreadContext.put("source", "k8s")
            try {
                Logger.info("Starting connection handler thread for $serverType on port $localPort")
                handleConnections(serverSocket, serverType, localPort, remotePort, portForwardResult, namespace)
            } catch (e: Exception) {
                Logger.error("Port forwarding thread for $serverType terminated: ${e.message}")
                Logger.error(e, "Stack trace for port forwarding thread termination")
            } finally {
                try {
                    Logger.info("Closing server socket for $serverType on port $localPort")
                    serverSocket.close()
                } catch (e: Exception) {
                    Logger.error("Error closing server socket: ${e.message}")
                }
            }
        }.apply {
            name = "ServerSocket-$serverType-$localPort"
            isDaemon = true
            start()
        }

        // Add shutdown hook to close the server socket
        Runtime.getRuntime().addShutdownHook(Thread {
            try {
                Logger.info("Shutdown hook closing server socket for $serverType on port $localPort")
                serverSocket.close()
            } catch (e: Exception) {
                Logger.error("Error closing server socket during shutdown: ${e.message}")
            }
        })

        return serverSocket
    }

    /**
     * Handles connections to the server socket.
     *
     * @param serverSocket The server socket to accept connections from
     * @param serverType The type of server (public, ml, admin)
     * @param localPort The local port the server socket is bound to
     * @param remotePort The remote port being forwarded
     * @param portForwardResult The PortForwardResult object
     * @param namespace The namespace of the pod
     */
    private fun handleConnections(
        serverSocket: ServerSocket,
        serverType: String,
        localPort: Int,
        remotePort: Int,
        portForwardResult: PortForward.PortForwardResult,
        namespace: String
    ) {
        Logger.info("Starting to handle connections for $serverType on port $localPort")

        // Track active connections to ensure we don't have too many
        val activeConnections = AtomicInteger(0)
        val maxConnections = 10 // Maximum number of concurrent connections

        while (!Thread.currentThread().isInterrupted) {
            try {
                // Accept a connection
                val socket = serverSocket.accept()
                val connectionCount = activeConnections.incrementAndGet()

                Logger.info("Accepted connection for $serverType on port $localPort (active: $connectionCount)")

                // If we have too many connections, log a warning
                if (connectionCount > maxConnections) {
                    Logger.warn("Too many active connections for $serverType: $connectionCount > $maxConnections")
                }

                // Create a thread to handle the connection and decrement the counter when done
                Thread {
                    try {
                        // Handle the socket connection
                        handleSocketConnection(socket, serverType, remotePort, portForwardResult, namespace)
                    } finally {
                        val remaining = activeConnections.decrementAndGet()
                        Logger.info("Connection for $serverType completed (active: $remaining)")
                    }
                }.apply {
                    name = "Connection-$serverType-$localPort-${System.currentTimeMillis()}"
                    isDaemon = true
                    start()
                }
            } catch (e: java.net.SocketException) {
                // Socket exceptions are expected when the server socket is closed
                if (!Thread.currentThread().isInterrupted && !serverSocket.isClosed) {
                    Logger.error("Socket exception accepting connection for $serverType: ${e.message}")
                }
            } catch (e: Exception) {
                if (!Thread.currentThread().isInterrupted) {
                    Logger.error("Error accepting connection for $serverType: ${e.message}")
                    Logger.error(e, "Stack trace for connection acceptance error")

                    // Add a small delay to avoid spinning in case of persistent errors
                    try {
                        Thread.sleep(1000)
                    } catch (ie: InterruptedException) {
                        Thread.currentThread().interrupt()
                        break
                    }
                }
            }
        }

        Logger.info("Connection handler for $serverType on port $localPort is shutting down")
    }

    /**
     * Handles a socket connection by setting up bidirectional data streams.
     *
     * @param socket The socket connection to handle
     * @param serverType The type of server (public, ml, admin)
     * @param remotePort The remote port being forwarded
     * @param portForwardResult The PortForwardResult object
     * @param namespace The namespace of the pod
     */
    private fun handleSocketConnection(
        socket: Socket,
        serverType: String,
        remotePort: Int,
        portForwardResult: PortForward.PortForwardResult,
        namespace: String
    ) {
        try {
            // Configure socket for keep-alive and better timeout handling
            socket.keepAlive = true
            socket.soTimeout = 120000  // 2 minutes timeout, matching the API timeout
            socket.tcpNoDelay = true   // Disable Nagle's algorithm for better responsiveness

            Logger.info("Configured socket for keep-alive for $serverType")

            // Use a flag to track if the socket has been closed
            val socketClosed = AtomicBoolean(false)

            // Track the state of each stream
            val outboundStreamCompleted = AtomicBoolean(false)
            val inboundStreamCompleted = AtomicBoolean(false)
            val outboundStreamError = AtomicBoolean(false)
            val inboundStreamError = AtomicBoolean(false)

            // Start a thread to copy data from the socket to the port forward
            Thread {
                ThreadContext.put("pair", namespace)
                ThreadContext.put("source", "k8s")

                try {
                    // Copy data from socket to port forward
                    Streams.copy(socket.getInputStream(), portForwardResult.getOutboundStream(remotePort))

                    // If we get here, the stream completed normally (client closed connection)
                    // This is expected behavior when a client closes their connection
                    Logger.info("Outbound stream completed normally for $serverType")
                    outboundStreamCompleted.set(true)

                    // Don't close the socket here, just exit the thread
                    // The socket will be closed by the client if needed
                } catch (e: InterruptedException) {
                    // Thread was interrupted, exit
                    Thread.currentThread().interrupt()
                    Logger.info("Outbound stream thread interrupted for $serverType")
                    outboundStreamError.set(true)
                } catch (e: IOException) {
                    if (!socketClosed.get()) {
                        Logger.error("Error in outbound stream for $serverType: ${e.message}")
                        outboundStreamError.set(true)

                        // Only close the socket if both streams have errors or completed
                        if (shouldCloseSocket(outboundStreamCompleted, inboundStreamCompleted, outboundStreamError, inboundStreamError)) {
                            closeSocketSafely(socket, socketClosed, "outbound stream error", serverType)
                        }
                    }
                } catch (e: Exception) {
                    if (!socketClosed.get()) {
                        Logger.error("Unexpected error in outbound stream for $serverType: ${e.message}")
                        outboundStreamError.set(true)

                        // Only close the socket if both streams have errors or completed
                        if (shouldCloseSocket(outboundStreamCompleted, inboundStreamCompleted, outboundStreamError, inboundStreamError)) {
                            closeSocketSafely(socket, socketClosed, "outbound stream unexpected error", serverType)
                        }
                    }
                }
            }.apply {
                name = "OutboundStream-$serverType-${System.currentTimeMillis()}"
                isDaemon = true
                start()
            }

            // Start a thread to copy data from the port forward to the socket
            Thread {
                ThreadContext.put("pair", namespace)
                ThreadContext.put("source", "k8s")

                try {
                    // Copy data from port forward to socket
                    Streams.copy(portForwardResult.getInputStream(remotePort), socket.getOutputStream())

                    // If we get here, the stream completed normally
                    // This is expected behavior when a client closes their connection
                    Logger.info("Inbound stream completed normally for $serverType")
                    inboundStreamCompleted.set(true)

                    // Don't close the socket here, just exit the thread
                    // The socket will be closed by the client if needed
                } catch (e: InterruptedException) {
                    // Thread was interrupted, exit
                    Thread.currentThread().interrupt()
                    Logger.info("Inbound stream thread interrupted for $serverType")
                    inboundStreamError.set(true)
                } catch (e: IOException) {
                    if (!socketClosed.get()) {
                        Logger.error("Error in inbound stream for $serverType: ${e.message}")
                        inboundStreamError.set(true)

                        // Only close the socket if both streams have errors or completed
                        if (shouldCloseSocket(outboundStreamCompleted, inboundStreamCompleted, outboundStreamError, inboundStreamError)) {
                            closeSocketSafely(socket, socketClosed, "inbound stream error", serverType)
                        }
                    }
                } catch (e: Exception) {
                    if (!socketClosed.get()) {
                        Logger.error("Unexpected error in inbound stream for $serverType: ${e.message}")
                        inboundStreamError.set(true)

                        // Only close the socket if both streams have errors or completed
                        if (shouldCloseSocket(outboundStreamCompleted, inboundStreamCompleted, outboundStreamError, inboundStreamError)) {
                            closeSocketSafely(socket, socketClosed, "inbound stream unexpected error", serverType)
                        }
                    }
                }
            }.apply {
                name = "InboundStream-$serverType-${System.currentTimeMillis()}"
                isDaemon = true
                start()
            }
        } catch (e: Exception) {
            Logger.error("Error setting up socket connection for $serverType: ${e.message}")
            try {
                socket.close()
            } catch (e: Exception) {
                Logger.error("Error closing socket after setup error: ${e.message}")
            }
        }
    }

    /**
     * Determines if the socket should be closed based on the state of both streams.
     * 
     * @param outboundStreamCompleted Whether the outbound stream completed normally
     * @param inboundStreamCompleted Whether the inbound stream completed normally
     * @param outboundStreamError Whether the outbound stream encountered an error
     * @param inboundStreamError Whether the inbound stream encountered an error
     * @return true if the socket should be closed, false otherwise
     */
    private fun shouldCloseSocket(
        outboundStreamCompleted: AtomicBoolean,
        inboundStreamCompleted: AtomicBoolean,
        outboundStreamError: AtomicBoolean,
        inboundStreamError: AtomicBoolean
    ): Boolean {
        // Close if both streams have errors
        if (outboundStreamError.get() && inboundStreamError.get()) {
            return true
        }

        // Close if one stream has an error and the other has completed
        if ((outboundStreamError.get() && inboundStreamCompleted.get()) ||
            (inboundStreamError.get() && outboundStreamCompleted.get())) {
            return true
        }

        // Don't close if both streams completed normally
        if (outboundStreamCompleted.get() && inboundStreamCompleted.get()) {
            return false
        }

        // Don't close if one stream has an error but the other is still active
        return false
    }

    /**
     * Closes the socket safely, ensuring it's only closed once.
     * 
     * @param socket The socket to close
     * @param socketClosed The flag tracking if the socket has been closed
     * @param reason The reason for closing the socket
     * @param serverType The type of server (public, ml, admin)
     */
    private fun closeSocketSafely(
        socket: Socket,
        socketClosed: AtomicBoolean,
        reason: String,
        serverType: String
    ) {
        if (socketClosed.compareAndSet(false, true)) {
            try {
                Logger.info("Closing socket due to $reason for $serverType")
                socket.close()
            } catch (e: Exception) {
                Logger.error("Error closing socket: ${e.message}")
            }
        }
    }

    /**
     * Finds a free local port.
     *
     * @return A free local port
     */
    private fun findFreePort(): Int {
        return ServerSocket(0).use { socket ->
            socket.localPort
        }
    }

    /**
     * Closes all resources associated with this port forwarder.
     * This should be called when the port forwarder is no longer needed.
     */
    fun close() {
        // Close all server sockets
        serverSockets.forEach { (key, socket) ->
            try {
                Logger.info("Closing server socket for $key")
                socket.close()
            } catch (e: Exception) {
                Logger.error("Error closing server socket for $key: ${e.message}")
            }
        }
        serverSockets.clear()

        // Clear port forward instances
        portForwardInstances.clear()

        Logger.info("K8sPortForwarder resources have been closed")
    }
}
