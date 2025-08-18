package com.productscience

import io.kubernetes.client.openapi.ApiException
import io.kubernetes.client.openapi.Configuration
import io.kubernetes.client.openapi.apis.CoreV1Api
import io.kubernetes.client.util.Config
import org.tinylog.kotlin.Logger
import java.io.BufferedReader
import java.io.IOException
import java.io.InputStreamReader

/**
 * KubeExecutor implements the CliExecutor interface for Kubernetes.
 * It uses the official Kubernetes Java client to interact with Kubernetes pods.
 *
 * @param podName The name of the Kubernetes pod to execute commands on
 * @param namespace The Kubernetes namespace where the pod is located
 * @param config The application configuration
 * @param containerName Optional container name if the pod has multiple containers
 */
data class KubeExecutor(
    val podName: String,
    val namespace: String = "default",
    val config: ApplicationConfig,
    val containerName: String? = null
) : CliExecutor {
    private val coreV1Api: CoreV1Api

    init {
        try {
            // Load the default Kubernetes configuration from ~/.kube/config
            val client = Config.defaultClient()
            Configuration.setDefaultApiClient(client)
            coreV1Api = CoreV1Api()

            // Verify that the pod exists
            verifyPodExists()
        } catch (e: IOException) {
            Logger.error("Failed to initialize Kubernetes client: ${e.message}")
            throw IllegalStateException("Failed to initialize Kubernetes client", e)
        }
    }

    /**
     * Executes a command in the Kubernetes pod and returns the output.
     *
     * @param args The command arguments to execute
     * @return The command output as a list of strings
     */
    override fun exec(args: List<String>, stdin:String?): List<String> {
        try {
            Logger.trace("Executing command in pod $podName: ${args.joinToString(" ")}")

            // Build the kubectl exec command
            val kubectlCmd = mutableListOf("kubectl", "exec", "-n", namespace, podName)
            if (containerName != null) {
                kubectlCmd.addAll(listOf("-c", containerName))
            }
            kubectlCmd.add("--")
            kubectlCmd.addAll(args)

            // Execute the command
            val process = ProcessBuilder(kubectlCmd)
                .redirectErrorStream(true)
                .start()

            // Read the output
            val reader = BufferedReader(InputStreamReader(process.inputStream))
            val output = mutableListOf<String>()
            var line: String?
            while (reader.readLine().also { line = it } != null) {
                line?.let { output.add(it) }
            }

            // Wait for the process to complete
            val exitCode = process.waitFor()
            if (exitCode != 0) {
                Logger.warn("Command execution returned non-zero exit code: $exitCode")
            }

            Logger.trace("Command complete: output=$output")
            return output
        } catch (e: IOException) {
            Logger.error("IO error during command execution in pod $podName: ${e.message}")
            throw IllegalStateException("IO error during command execution", e)
        } catch (e: Exception) {
            Logger.error("Unexpected error during command execution in pod $podName: ${e.message}")
            throw IllegalStateException("Unexpected error during command execution", e)
        }
    }

    /**
     * Creates a Kubernetes pod.
     * Note: In Kubernetes, pods are typically created through deployments, statefulsets, etc.
     * This method is a placeholder for compatibility with the CliExecutor interface.
     *
     * @param doNotStartChain Whether to start the chain or not
     */
    override fun createContainer(doNotStartChain: Boolean) {
        Logger.info("Creating/verifying Kubernetes pod $podName in namespace $namespace")

        try {
            // Check if pod exists first
            if (podExists()) {
                Logger.info("Pod $podName already exists in namespace $namespace")
                return
            }

            // In a real implementation, we would create the pod here
            // For now, we'll just throw an exception if the pod doesn't exist
            throw IllegalStateException("Pod $podName does not exist in namespace $namespace and cannot be created automatically. Please create it manually.")
        } catch (e: ApiException) {
            Logger.error("Failed to check if pod exists: ${e.message}")
            throw IllegalStateException("Failed to check if pod exists", e)
        }
    }

    /**
     * Kills the Kubernetes pod.
     * Note: In Kubernetes, pods are typically managed by controllers.
     * This method is a placeholder for compatibility with the CliExecutor interface.
     */
    override fun kill() {
        throw NotImplementedError("Kubernetes does not support pod killing")
//        Logger.info("Killing Kubernetes pod $podName in namespace $namespace")

//        try {
//            // Execute kubectl delete pod command
//            val deleteCmd = listOf("kubectl", "delete", "pod", "-n", namespace, podName)
//            val process = ProcessBuilder(deleteCmd)
//                .redirectErrorStream(true)
//                .start()
//
//            // Read the output
//            val reader = BufferedReader(InputStreamReader(process.inputStream))
//            val output = mutableListOf<String>()
//            var line: String?
//            while (reader.readLine().also { line = it } != null) {
//                line?.let { output.add(it) }
//            }
//
//            // Wait for the process to complete
//            val exitCode = process.waitFor()
//            if (exitCode != 0) {
//                Logger.warn("Pod deletion returned non-zero exit code: $exitCode, output: ${output.joinToString("\n")}")
//                throw IllegalStateException("Failed to delete pod, exit code: $exitCode")
//            }
//
//            Logger.info("Successfully deleted pod $podName in namespace $namespace")
//        } catch (e: Exception) {
//            Logger.error("Failed to delete pod $podName: ${e.message}")
//            throw IllegalStateException("Failed to delete pod", e)
//        }
    }

    /**
     * Checks if the pod exists in the Kubernetes cluster.
     *
     * @return true if the pod exists, false otherwise
     */
    private fun podExists(): Boolean {
        try {
            val pod = coreV1Api.readNamespacedPod(podName, namespace, null)

            // If containerName is specified, verify that it exists in the pod
            if (containerName != null) {
                val containerExists = pod.spec?.containers?.any { it.name == containerName } ?: false
                if (!containerExists) {
                    Logger.warn("Container $containerName does not exist in pod $podName")
                    return false
                }
            }

            return true
        } catch (e: ApiException) {
            // 404 means the pod doesn't exist
            if (e.code == 404) {
                return false
            }
            // For other API exceptions, rethrow
            throw e
        }
    }

    /**
     * Verifies that the pod exists in the Kubernetes cluster.
     *
     * @throws IllegalStateException if the pod does not exist
     */
    private fun verifyPodExists() {
        if (!podExists()) {
            throw IllegalStateException("Pod $podName does not exist in namespace $namespace")
        }
        Logger.info("Verified Kubernetes pod $podName exists in namespace $namespace")
    }
}
