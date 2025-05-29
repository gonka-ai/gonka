package com.productscience

import io.kubernetes.client.PortForward
import io.kubernetes.client.openapi.ApiException
import io.kubernetes.client.openapi.Configuration
import io.kubernetes.client.openapi.apis.CoordinationV1Api
import io.kubernetes.client.openapi.apis.CoreV1Api
import io.kubernetes.client.util.Config
import io.kubernetes.client.util.Streams
import org.tinylog.kotlin.Logger
import java.io.BufferedReader
import java.io.File
import java.io.IOException
import java.io.InputStreamReader
import java.net.ServerSocket
import java.net.Socket
import java.time.Duration
import java.util.concurrent.ConcurrentHashMap
import java.util.regex.Pattern

// Pattern for worker nodes: k8s-worker-\d
private val workerNodePattern = Pattern.compile("k8s-worker-(\\d+)")

// Pattern for genesis namespace
private const val GENESIS_NAMESPACE = "genesis"

// Pattern for join namespaces: join-k8s-worker-\d+
private val joinNamespacePattern = Pattern.compile("join-k8s-worker-(\\d+)")

// Pattern for CLI pods: node-*
private val cliPodPattern = Pattern.compile("node-.*")

// Pattern for API pods: api-*
private val apiPodPattern = Pattern.compile("api-.*")

// Map of port types to their port numbers
private val portTypeToNumber = mapOf(
    SERVER_TYPE_PUBLIC to 9000,
    SERVER_TYPE_ML to 9100,
    SERVER_TYPE_ADMIN to 9200
)

// Map to store attached logs
private val attachedLogs = ConcurrentHashMap<String, LogOutput>()

/**
 * Gets Kubernetes inference pairs by finding worker nodes and their pods.
 * Returns a K8sInferencePairsWithLease instance that holds both the pairs and the lease.
 * The lease is held until the returned instance is closed.
 *
 * @param config The application configuration
 * @param leaseTimeoutSeconds The timeout for the lease in seconds (default: 30)
 * @return A K8sInferencePairsWithLease instance that holds the pairs and the lease
 */
fun getK8sInferencePairs(
    config: ApplicationConfig,
    leaseTimeoutSeconds: Int = 30
): K8sInferencePairsWithLease {
    Logger.info("Getting Kubernetes inference pairs")
    val leaseName = "t8t-tests"
    val namespace = "default"
    val coreV1Api = initializeKubernetesClient()
    val coordinationV1Api = CoordinationV1Api()

    // Create the K8sInferencePairsWithLease instance early to use its lease methods
    val k8sPairsWithLease = K8sInferencePairsWithLease(
        pairs = emptyList(), // Will be populated later
        coordinationV1Api = coordinationV1Api,
        namespace = namespace,
        leaseName = leaseName
    )

    try {
        val leaseSuccess = k8sPairsWithLease.getOrWaitForLease(10) // Wait for up to 10 minutes
        check(leaseSuccess) { "Failed to acquire lease after waiting 10 minutes" }

        val namespaces = coreV1Api.listNamespace(null, null, null, null, null, null, null, null, null, null)
        Logger.info("Found ${namespaces.items.size} namespaces")

        val inferencePairs = mutableListOf<LocalInferencePair>()
        processGenesisNamespace(coreV1Api, namespaces, inferencePairs, config)
        processJoinNamespaces(coreV1Api, namespaces, inferencePairs, config)

        Logger.info("Found ${inferencePairs.size} Kubernetes inference pairs, waiting for ports to settle")
        Thread.sleep(Duration.ofSeconds(10))

        k8sPairsWithLease.pairs = inferencePairs
        return k8sPairsWithLease

    } catch (e: ApiException) {
        k8sPairsWithLease.releaseLeaseIfAcquired()
        Logger.error(e, "Kubernetes API error")
        throw e
    } catch (e: IOException) {
        k8sPairsWithLease.releaseLeaseIfAcquired()
        Logger.error("IO error: ${e.message}")
        throw IllegalStateException("Failed to get Kubernetes inference pairs", e)
    } catch (e: Exception) {
        k8sPairsWithLease.releaseLeaseIfAcquired()
        Logger.error(e, "Error getting Kubernetes")
        throw e
    }
}

/**
 * Initializes the Kubernetes client.
 *
 * @return The CoreV1Api client
 */
private fun initializeKubernetesClient(): CoreV1Api {
    Logger.info("Initializing Kubernetes client")
    try {
        Logger.info("Creating Kubernetes client...")
        val client = Config.defaultClient()
        Logger.info("Successfully created Kubernetes client")
        Logger.info("  Client base path: ${client.basePath}")
        Logger.info("  Authentication enabled: ${client.authentications.isNotEmpty()}")
        Logger.info("  Verifying SSL: ${client.isVerifyingSsl}")

        Configuration.setDefaultApiClient(client)
        val coreApi = CoreV1Api()

        // Test the API connection
        try {
            Logger.info("Testing API connection...")
            val nodes = coreApi.listNode(null, null, null, null, null, null, null, null, 1, null)
            Logger.info("API connection successful! Found ${nodes.items.size} nodes")
        } catch (e: Exception) {
            Logger.error("Failed to connect to Kubernetes API: ${e.message}")
            Logger.error("API connection error details:", e)
        }

        return coreApi
    } catch (e: Exception) {
        Logger.error("Failed to initialize Kubernetes client: ${e.message}")
        Logger.error("Initialization error details:", e)
        throw e
    }
}

/**
 * Processes the genesis namespace to find and create an inference pair.
 *
 * @param coreV1Api The Kubernetes CoreV1Api client
 * @param namespaces The list of namespaces
 * @param inferencePairs The list to add inference pairs to
 * @param config The application configuration
 */
private fun processGenesisNamespace(
    coreV1Api: CoreV1Api,
    namespaces: io.kubernetes.client.openapi.models.V1NamespaceList,
    inferencePairs: MutableList<LocalInferencePair>,
    config: ApplicationConfig
) {
    val genesisNamespace = namespaces.items.find { it.metadata?.name == GENESIS_NAMESPACE }
    if (genesisNamespace != null) {
        Logger.info("Found genesis namespace: ${genesisNamespace.metadata?.name}")
        val genesisPair = createInferencePairForNamespace(coreV1Api, GENESIS_NAMESPACE, "genesis", config)
        genesisPair?.let { inferencePairs.add(it) }
    }
}

/**
 * Processes join namespaces to find and create inference pairs.
 *
 * @param coreV1Api The Kubernetes CoreV1Api client
 * @param namespaces The list of namespaces
 * @param inferencePairs The list to add inference pairs to
 * @param config The application configuration
 */
private fun processJoinNamespaces(
    coreV1Api: CoreV1Api,
    namespaces: io.kubernetes.client.openapi.models.V1NamespaceList,
    inferencePairs: MutableList<LocalInferencePair>,
    config: ApplicationConfig
) {
    // Find join namespaces
    val joinNamespaces = namespaces.items.filter {
        it.metadata?.name?.let { name -> joinNamespacePattern.matcher(name).matches() } ?: false
    }
    Logger.info("Found ${joinNamespaces.size} join namespaces")

    // Process each join namespace
    joinNamespaces.forEach { namespace ->
        processJoinNamespace(coreV1Api, namespace, inferencePairs, config)
    }
}

/**
 * Processes a single join namespace to create an inference pair.
 *
 * @param coreV1Api The Kubernetes CoreV1Api client
 * @param namespace The namespace to process
 * @param inferencePairs The list to add inference pairs to
 * @param config The application configuration
 */
private fun processJoinNamespace(
    coreV1Api: CoreV1Api,
    namespace: io.kubernetes.client.openapi.models.V1Namespace,
    inferencePairs: MutableList<LocalInferencePair>,
    config: ApplicationConfig
) {
    val namespaceName = namespace.metadata?.name ?: return
    val matcher = joinNamespacePattern.matcher(namespaceName)
    if (matcher.find()) {
        val workerId = matcher.group(1)
        val nodeName = "k8s-worker-$workerId"
        Logger.info("Processing join namespace: $namespaceName for node: $nodeName")

        val joinPair = createInferencePairForNamespace(coreV1Api, namespaceName, nodeName, config)
        joinPair?.let { inferencePairs.add(it) }
    }
}

/**
 * Creates an inference pair for a specific namespace.
 *
 * @param coreV1Api The Kubernetes CoreV1Api client
 * @param namespace The namespace to create the inference pair for
 * @param nodeName The name of the node
 * @param config The application configuration
 * @return A LocalInferencePair object, or null if the required pods are not found
 */
private fun createInferencePairForNamespace(
    coreV1Api: CoreV1Api,
    namespace: String,
    nodeName: String,
    config: ApplicationConfig
): LocalInferencePair? {
    try {
        // Find CLI and API pods
        val podInfo = findPodsInNamespace(coreV1Api, namespace) ?: return null
        val (cliPodName, apiPodName) = podInfo

        // Create config with node name
        val configWithName = config.copy(pairName = nodeName)

        // Set up port forwarding for API pod
        val apiUrls = setupPortForwarding(coreV1Api, namespace, apiPodName)

        // Create executor and attach logs
        val executor = createExecutor(cliPodName, namespace, configWithName)
        val logs = attachLogsForPods(coreV1Api, namespace, nodeName, cliPodName, apiPodName)

        // Create and return the inference pair
        return createLocalInferencePair(nodeName, configWithName, executor, apiUrls, logs)

    } catch (e: ApiException) {
        Logger.error("Kubernetes API error for namespace $namespace: ${e.message}")
        return null
    } catch (e: Exception) {
        Logger.error("Error creating inference pair for namespace $namespace: ${e.message}")
        return null
    }
}

/**
 * Finds CLI and API pods in a namespace.
 *
 * @param coreV1Api The Kubernetes CoreV1Api client
 * @param namespace The namespace to search in
 * @return A Pair of CLI pod name and API pod name, or null if either pod is not found
 */
private fun findPodsInNamespace(
    coreV1Api: CoreV1Api,
    namespace: String
): Pair<String, String>? {
    // Get all pods in the namespace
    val pods = coreV1Api.listNamespacedPod(
        namespace, null, null, null, null, null, null, null, null, null, null
    )
    Logger.info("Found ${pods.items.size} pods in namespace $namespace")

    // Find CLI pod (starts with "node-")
    val cliPod = pods.items.find {
        it.metadata?.name?.let { name -> cliPodPattern.matcher(name).matches() } ?: false
    }
    if (cliPod == null) {
        Logger.warn("No CLI pod found in namespace $namespace")
        return null
    }
    val cliPodName = cliPod.metadata?.name ?: return null
    Logger.info("Found CLI pod: $cliPodName in namespace $namespace")

    // Find API pod (starts with "api-")
    val apiPod = pods.items.find {
        it.metadata?.name?.let { name -> apiPodPattern.matcher(name).matches() } ?: false
    }
    if (apiPod == null) {
        Logger.warn("No API pod found in namespace $namespace")
        return null
    }
    val apiPodName = apiPod.metadata?.name ?: return null
    Logger.info("Found API pod: $apiPodName in namespace $namespace")

    return Pair(cliPodName, apiPodName)
}

/**
 * Creates a KubeExecutor for the CLI pod.
 *
 * @param cliPodName The name of the CLI pod
 * @param namespace The namespace of the pod
 * @param config The application configuration
 * @return A KubeExecutor instance
 */
private fun createExecutor(
    cliPodName: String,
    namespace: String,
    config: ApplicationConfig
): KubeExecutor {
    return KubeExecutor(
        podName = cliPodName,
        namespace = namespace,
        config = config
    )
}

/**
 * Attaches logs for CLI and API pods.
 *
 * @param coreV1Api The Kubernetes CoreV1Api client
 * @param namespace The namespace of the pods
 * @param nodeName The name of the node
 * @param cliPodName The name of the CLI pod
 * @param apiPodName The name of the API pod
 * @return A Pair of LogOutput objects for node and API logs
 */
private fun attachLogsForPods(
    coreV1Api: CoreV1Api,
    namespace: String,
    nodeName: String,
    cliPodName: String,
    apiPodName: String
): Pair<LogOutput, LogOutput> {
    val nodeLogs = attachK8sLogs(coreV1Api, namespace, nodeName, "node", cliPodName)
    val apiLogs = attachK8sLogs(coreV1Api, namespace, nodeName, "api", apiPodName)
    return Pair(nodeLogs, apiLogs)
}

/**
 * Creates a LocalInferencePair instance.
 *
 * @param nodeName The name of the node
 * @param config The application configuration
 * @param executor The KubeExecutor for the CLI pod
 * @param apiUrls The URLs for the API pod
 * @param logs A Pair of LogOutput objects for node and API logs
 * @return A LocalInferencePair instance
 */
private fun createLocalInferencePair(
    nodeName: String,
    config: ApplicationConfig,
    executor: KubeExecutor,
    apiUrls: Map<String, String>,
    logs: Pair<LogOutput, LogOutput>
): LocalInferencePair {
    val (nodeLogs, apiLogs) = logs

    Logger.info("Creating Kubernetes inference pair for $nodeName")
    Logger.info("API URLs:")
    Logger.info("  $SERVER_TYPE_PUBLIC: ${apiUrls[SERVER_TYPE_PUBLIC]}")
    Logger.info("  $SERVER_TYPE_ML: ${apiUrls[SERVER_TYPE_ML]}")
    Logger.info("  $SERVER_TYPE_ADMIN: ${apiUrls[SERVER_TYPE_ADMIN]}")

    return LocalInferencePair(
        node = ApplicationCLI(config, nodeLogs, executor),
        api = ApplicationAPI(apiUrls, config, apiLogs),
        mock = null, // No mock for Kubernetes
        name = nodeName,
        config = config
    )
}

/**
 * Sets up port forwarding for the API pod using the Kubernetes Java client's PortForward class.
 *
 * @param coreV1Api The Kubernetes CoreV1Api client
 * @param namespace The namespace of the API pod
 * @param apiPodName The name of the API pod
 * @return A map of server types to URLs
 */
private fun setupPortForwarding(
    coreV1Api: CoreV1Api,
    namespace: String,
    apiPodName: String
): Map<String, String> {
    val apiUrls = mutableMapOf<String, String>()
    val portForwardResults = mutableMapOf<Int, PortForward.PortForwardResult>()

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
            val serverSocket = createServerSocket(localPort, serverType, remotePort, result)

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

    return apiUrls
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
    val ports = ArrayList<Int>()
    ports.add(remotePort)

    // Set up port forwarding
    val portForward = PortForward()
    val result = portForward.forward(namespace, podName, ports)
    Logger.info("Forwarding established for port $remotePort")

    return result
}

/**
 * Creates a server socket for handling connections to the forwarded port.
 *
 * @param localPort The local port to bind the server socket to
 * @param serverType The type of server (public, ml, admin)
 * @param remotePort The remote port being forwarded
 * @param portForwardResult The PortForwardResult object
 * @return The created ServerSocket
 */
private fun createServerSocket(
    localPort: Int,
    serverType: String,
    remotePort: Int,
    portForwardResult: PortForward.PortForwardResult
): ServerSocket {
    // Create a server socket to accept connections
    val serverSocket = ServerSocket(localPort)

    // Start a thread to handle connections
    Thread {
        try {
            handleConnections(serverSocket, serverType, localPort, remotePort, portForwardResult)
        } catch (e: Exception) {
            Logger.error("Port forwarding thread for $serverType terminated: ${e.message}")
        } finally {
            try {
                serverSocket.close()
            } catch (e: Exception) {
                Logger.error("Error closing server socket: ${e.message}")
            }
        }
    }.apply {
        isDaemon = true
        start()
    }

    // Add shutdown hook to close the server socket
    Runtime.getRuntime().addShutdownHook(Thread {
        try {
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
 */
private fun handleConnections(
    serverSocket: ServerSocket,
    serverType: String,
    localPort: Int,
    remotePort: Int,
    portForwardResult: PortForward.PortForwardResult
) {
    while (!Thread.currentThread().isInterrupted) {
        try {
            // Accept a connection
            val socket = serverSocket.accept()
            Logger.info("Accepted connection for $serverType on port $localPort")

            // Handle the socket connection
            handleSocketConnection(socket, serverType, remotePort, portForwardResult)

        } catch (e: Exception) {
            if (!Thread.currentThread().isInterrupted) {
                Logger.error("Error accepting connection for $serverType: ${e.message}")
            }
        }
    }
}

/**
 * Handles a socket connection by setting up bidirectional data streams.
 *
 * @param socket The socket connection to handle
 * @param serverType The type of server (public, ml, admin)
 * @param remotePort The remote port being forwarded
 * @param portForwardResult The PortForwardResult object
 */
private fun handleSocketConnection(
    socket: Socket,
    serverType: String,
    remotePort: Int,
    portForwardResult: PortForward.PortForwardResult
) {
    // Start a thread to copy data from the socket to the port forward
    Thread {
        try {
            Streams.copy(socket.getInputStream(), portForwardResult.getOutboundStream(remotePort))
        } catch (e: IOException) {
            Logger.error("Error in outbound stream for $serverType: ${e.message}")
        } catch (e: Exception) {
            Logger.error("Unexpected error in outbound stream for $serverType: ${e.message}")
        } finally {
            try {
                socket.close()
            } catch (e: Exception) {
                Logger.error("Error closing socket: ${e.message}")
            }
        }
    }.start()

    // Start a thread to copy data from the port forward to the socket
    Thread {
        try {
            Streams.copy(portForwardResult.getInputStream(remotePort), socket.getOutputStream())
        } catch (e: IOException) {
            Logger.error("Error in inbound stream for $serverType: ${e.message}")
        } catch (e: Exception) {
            Logger.error("Unexpected error in inbound stream for $serverType: ${e.message}")
        } finally {
            try {
                socket.close()
            } catch (e: Exception) {
                Logger.error("Error closing socket: ${e.message}")
            }
        }
    }.start()
}

/**
 * Finds a free port on the local machine.
 *
 * @return A free port number
 */
private fun findFreePort(): Int {
    return ServerSocket(0).use { it.localPort }
}

/**
 * Attaches logs for a Kubernetes pod.
 *
 * @param coreV1Api The Kubernetes CoreV1Api client
 * @param namespace The namespace of the pod
 * @param nodeName The name of the node
 * @param type The type of logs (node or api)
 * @param podName The name of the pod
 * @return A LogOutput object for the pod
 */
private fun attachK8sLogs(
    coreV1Api: CoreV1Api,
    namespace: String,
    nodeName: String,
    type: String,
    podName: String
): LogOutput {
    val key = "$namespace-$podName"
    return attachedLogs.computeIfAbsent(key) {
        val logOutput = LogOutput(nodeName, type)

        // Start a thread to stream logs
        startLogStreamingThread(namespace, podName, logOutput)

        logOutput
    }
}

/**
 * Starts a thread to stream logs from a Kubernetes pod.
 *
 * @param namespace The namespace of the pod
 * @param podName The name of the pod
 * @param logOutput The LogOutput object to send log entries to
 */
private fun startLogStreamingThread(
    namespace: String,
    podName: String,
    logOutput: LogOutput
) {
    Thread {
        try {
            // Create and start the log streaming process
            val process = createLogStreamingProcess(namespace, podName)

            // Process the log stream
            processLogStream(process, podName, namespace, logOutput)

            Logger.info("Log streaming ended for pod $podName in namespace $namespace")
        } catch (e: Exception) {
            Logger.error("Failed to attach logs for pod $podName in namespace $namespace: ${e.message}")
        }
    }.apply {
        isDaemon = true
        start()
    }
}

/**
 * Creates a process to stream logs from a Kubernetes pod.
 *
 * @param namespace The namespace of the pod
 * @param podName The name of the pod
 * @return The created Process
 */
private fun createLogStreamingProcess(
    namespace: String,
    podName: String
): Process {
    // Use kubectl logs command to stream logs
    val logsCmd = listOf(
        "kubectl", "logs",
        "-n", namespace,
        "-f", // Follow logs
        "--tail=0", // Only show new logs after connecting, not historical logs
        podName
    )

    Logger.info("Starting log streaming: ${logsCmd.joinToString(" ")}")

    return ProcessBuilder(logsCmd)
        .redirectErrorStream(true)
        .start()
}

/**
 * Processes the log stream from a Kubernetes pod.
 *
 * @param process The process streaming the logs
 * @param podName The name of the pod
 * @param namespace The namespace of the pod
 * @param logOutput The LogOutput object to send log entries to
 */
private fun processLogStream(
    process: Process,
    podName: String,
    namespace: String,
    logOutput: LogOutput
) {
    // Read logs from the process output
    val reader = BufferedReader(InputStreamReader(process.inputStream))
    var line: String?

    while (reader.readLine().also { line = it } != null) {
        // Convert log line to Frame and send to LogOutput
        sendLogLineToOutput(line, logOutput)
    }
}

/**
 * Converts a log line to a Frame and sends it to the LogOutput.
 *
 * @param line The log line
 * @param logOutput The LogOutput object to send the frame to
 */
private fun sendLogLineToOutput(
    line: String?,
    logOutput: LogOutput
) {
    // Create a Frame object with the log line as payload
    // This is a workaround since LogOutput expects Frame objects
    val frame = com.github.dockerjava.api.model.Frame(
        com.github.dockerjava.api.model.StreamType.STDOUT,
        line?.toByteArray() ?: ByteArray(0)
    )

    // Pass the frame to LogOutput's onNext method
    logOutput.onNext(frame)
}
