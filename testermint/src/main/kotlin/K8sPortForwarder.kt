package com.productscience

import io.kubernetes.client.PortForward
import org.tinylog.ThreadContext
import org.tinylog.kotlin.Logger
import java.net.ServerSocket
import java.util.concurrent.ConcurrentHashMap

/**
 * Manages Kubernetes port forwarding to API pods
 */
class K8sPortForwarder {
    private val portForwardInstances = ConcurrentHashMap<String, PortForward>()
    private val serverSockets = ConcurrentHashMap<String, ServerSocket>()

    /**
     * Sets up port forwarding for the API pod
     * @return A map of server types to URLs (e.g. "public" -> "http://localhost:50123")
     */
    fun setupPortForwarding(
        namespace: String,
        apiPodName: String
    ): Map<String, String> {
        return logContext(mapOf("pair" to namespace, "source" to "k8s")) {
            K8sPortForwarderConfig.PORT_CONFIGURATIONS.associate { config ->
                setupPortForwardingForConfig(config, namespace, apiPodName)
            }
        }
    }

    private fun setupPortForwardingForConfig(
        config: PortForwardConfig,
        namespace: String,
        apiPodName: String
    ): Pair<String, String> {
        return try {
            val localPort = findFreePort()
            Logger.info(
                "Setting up port forwarding for ${config.serverType}: " +
                        "localhost:$localPort -> $apiPodName:${config.remotePort}"
            )

            val portForwardResult = setupPortForwardForPort(namespace, apiPodName, config.remotePort)
            val serverSocket = createServerSocket(localPort, config, portForwardResult, namespace)

            storeServerSocket(namespace, apiPodName, config.serverType, serverSocket)

            val url = "http://localhost:$localPort"
            Logger.info("Port forwarding set up for ${config.serverType}: $url")

            config.serverType to url
        } catch (e: Exception) {
            Logger.error("Failed to set up port forwarding for ${config.serverType}: ${e.message}")
            val fallbackUrl = "http://$apiPodName.$namespace.svc.cluster.local:${config.remotePort}"
            Logger.info("Using fallback URL for ${config.serverType}: $fallbackUrl")

            config.serverType to fallbackUrl
        }
    }

    private fun storeServerSocket(
        namespace: String,
        apiPodName: String,
        serverType: String,
        serverSocket: ServerSocket
    ) {
        val socketKey = "$namespace-$apiPodName-$serverType"
        serverSockets[socketKey] = serverSocket
    }

    private fun setupPortForwardForPort(
        namespace: String,
        podName: String,
        remotePort: Int
    ): PortForward.PortForwardResult {
        return logContext(mapOf("pair" to namespace, "source" to "k8s")) {
            val key = "$namespace-$podName"
            val portForward = portForwardInstances.computeIfAbsent(key) {
                Logger.info("Creating new PortForward instance for $namespace/$podName")
                PortForward()
            }

            val result = portForward.forward(namespace, podName, listOf(remotePort))
            Logger.info("Forwarding established for port $remotePort using shared PortForward instance")
            result
        }
    }

    private fun createServerSocket(
        localPort: Int,
        config: PortForwardConfig,
        portForwardResult: PortForward.PortForwardResult,
        namespace: String
    ): ServerSocket {
        val serverSocket = ServerSocket(localPort, config.serverSocketBacklog).apply {
            reuseAddress = true
        }

        Logger.info("Created server socket for ${config.serverType} on port $localPort with reuse address enabled")

        startConnectionHandlerThread(serverSocket, config, portForwardResult, namespace, localPort)
        addShutdownHook(serverSocket, config.serverType, localPort)

        return serverSocket
    }

    private fun startConnectionHandlerThread(
        serverSocket: ServerSocket,
        config: PortForwardConfig,
        portForwardResult: PortForward.PortForwardResult,
        namespace: String,
        localPort: Int
    ) {
        Thread {
            ThreadContext.put("pair", namespace)
            ThreadContext.put("source", "k8s")
            try {
                Logger.info("Starting connection handler thread for ${config.serverType} on port $localPort")
                ConnectionManager(serverSocket, config, portForwardResult, namespace, localPort).startHandling()
            } catch (e: Exception) {
                Logger.error("Port forwarding thread for ${config.serverType} terminated: ${e.message}")
                Logger.error(e, "Stack trace for port forwarding thread termination")
            } finally {
                closeServerSocket(serverSocket, config.serverType, localPort)
            }
        }.apply {
            name = "ServerSocket-${config.serverType}-$localPort"
            isDaemon = true
            start()
        }
    }

    private fun addShutdownHook(serverSocket: ServerSocket, serverType: String, localPort: Int) {
        Runtime.getRuntime().addShutdownHook(Thread {
            try {
                Logger.info("Shutdown hook closing server socket for $serverType on port $localPort")
                serverSocket.close()
            } catch (e: Exception) {
                Logger.error("Error closing server socket during shutdown: ${e.message}")
            }
        })
    }


    private fun closeServerSocket(serverSocket: ServerSocket, serverType: String, localPort: Int) {
        try {
            Logger.info("Closing server socket for $serverType on port $localPort")
            serverSocket.close()
        } catch (e: Exception) {
            Logger.error("Error closing server socket: ${e.message}")
        }
    }

    private fun findFreePort(): Int {
        return ServerSocket(0).use { it.localPort }
    }

    /**
     * Releases all resources associated with this port forwarder
     */
    fun close() {
        serverSockets.forEach { (key, socket) ->
            try {
                Logger.info("Closing server socket for $key")
                socket.close()
            } catch (e: Exception) {
                Logger.error("Error closing server socket for $key: ${e.message}")
            }
        }
        serverSockets.clear()
        portForwardInstances.clear()

        Logger.info("K8sPortForwarder resources have been closed")
    }
}
