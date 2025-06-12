package com.productscience

import io.kubernetes.client.openapi.ApiException
import io.kubernetes.client.openapi.apis.CoordinationV1Api
import io.kubernetes.client.openapi.models.V1Lease
import io.kubernetes.client.openapi.models.V1LeaseSpec
import io.kubernetes.client.openapi.models.V1ObjectMeta
import org.tinylog.kotlin.Logger
import java.io.Closeable
import java.time.OffsetDateTime
import java.util.*
import java.util.concurrent.TimeUnit

/**
 * A wrapper class that holds Kubernetes inference pairs and the lease.
 * Implements Closeable to ensure the lease is released when the instance is closed.
 *
 * @property pairs The list of LocalInferencePair objects
 * @property coordinationV1Api The Kubernetes CoordinationV1Api client
 * @property namespace The namespace where the lease exists
 * @property leaseName The name of the lease
 */
class K8sInferencePairsWithLease(
    var pairs: List<LocalInferencePair>,
    private val coordinationV1Api: CoordinationV1Api,
    private val namespace: String,
    private val leaseName: String
) : Closeable, List<LocalInferencePair> by pairs {
    val holderIdentity = "$leaseName-${UUID.randomUUID()}"

    // Delegate to the current value of pairs
    override val size: Int get() = pairs.size
    override fun isEmpty(): Boolean = pairs.isEmpty()
    override fun iterator(): Iterator<LocalInferencePair> = pairs.iterator()
    override fun listIterator(): ListIterator<LocalInferencePair> = pairs.listIterator()
    override fun listIterator(index: Int): ListIterator<LocalInferencePair> = pairs.listIterator(index)
    override fun subList(fromIndex: Int, toIndex: Int): List<LocalInferencePair> = pairs.subList(fromIndex, toIndex)
    override fun lastIndexOf(element: LocalInferencePair): Int = pairs.lastIndexOf(element)
    override fun indexOf(element: LocalInferencePair): Int = pairs.indexOf(element)
    override fun containsAll(elements: Collection<LocalInferencePair>): Boolean = pairs.containsAll(elements)
    override fun contains(element: LocalInferencePair): Boolean = pairs.contains(element)
    override fun get(index: Int): LocalInferencePair = pairs[index]

    private var renewalThread: Thread? = null
    private var running = true
    private var leaseAcquired = false

    /**
     * Initializes and starts the lease renewal thread.
     * This should only be called after the lease has been acquired.
     */
    private fun startRenewalThread() {
        // Create a background thread to renew the lease every 20 seconds
        renewalThread = Thread {
            try {
                while (running) {
                    try {
                        // Sleep for 20 seconds
                        Thread.sleep(TimeUnit.SECONDS.toMillis(20))

                        // Renew the lease if we're still running
                        if (running) {
                            updateLease(30) // 30 seconds
                            Logger.info("Lease renewed successfully")
                        }
                    } catch (e: InterruptedException) {
                        // Thread was interrupted, check if we should continue
                        if (!running) {
                            break
                        }
                    } catch (e: Exception) {
                        Logger.error("Failed to renew lease: ${e.message}")
                        // Continue trying to renew even if there was an error
                    }
                }
            } catch (e: Exception) {
                Logger.error("Lease renewal thread encountered an error: ${e.message}")
            }
        }

        // Start the renewal thread
        renewalThread?.isDaemon = true
        renewalThread?.start()
        Logger.info("Lease renewal thread started")
    }

    /**
     * Attempts to acquire the lease.
     *
     * @param timeoutSeconds The timeout for the lease in seconds
     * @return true if the lease was acquired, false otherwise
     */
    private fun acquireLease(timeoutSeconds: Int): Boolean {
        try {
            val existingLease: V1Lease = coordinationV1Api.readNamespacedLease(leaseName, namespace, null)
            Logger.info("Lease exists, attempting to claim it")

            if (!existingLease.isAvailable()) {
                return false
            }
            // Try to update the lease with our holder identity
            updateLease(timeoutSeconds)
            Logger.info("Successfully claimed lease $leaseName")
            leaseAcquired = true

            // Start the renewal thread now that we have acquired the lease
            startRenewalThread()

            return true
        } catch (e: ApiException) {
            if (e.code == 404) {
                Logger.info("Lease $leaseName does not exist, creating it")
                createLease(timeoutSeconds)
                leaseAcquired = true

                // Start the renewal thread now that we have acquired the lease
                startRenewalThread()

                return true
            } else {
                Logger.error(e, "Failed to claim lease $leaseName")
                throw e
            }
        }
    }

    /**
     * Attempts to acquire the lease. If the lease is not available, waits for it to become available.
     * 
     * @param maxWaitMinutes The maximum time to wait for the lease in minutes
     * @return true if the lease was acquired, false if the wait timed out
     */
    fun getOrWaitForLease(maxWaitMinutes: Int): Boolean {
        Logger.info("Attempting to acquire lease: $leaseName")
        val leaseAcquired = acquireLease(30) // Default timeout of 30 seconds

        if (!leaseAcquired) {
            Logger.info("Lease is already taken, waiting for it to be available (up to $maxWaitMinutes minutes)")
            val waitSuccess = waitForLease(maxWaitMinutes)
            if (!waitSuccess) {
                Logger.error("Failed to acquire lease after waiting $maxWaitMinutes minutes")
                return false
            }
        }

        Logger.info("Lease acquired successfully")
        return true
    }

    /**
     * Creates a new lease in Kubernetes.
     *
     * @param timeoutSeconds The timeout for the lease in seconds
     */
    private fun createLease(timeoutSeconds: Int) {
        val lease = V1Lease()
            .metadata(V1ObjectMeta().name(leaseName))
            .spec(
                V1LeaseSpec()
                    .holderIdentity(holderIdentity)
                    .leaseDurationSeconds(timeoutSeconds)
            )

        coordinationV1Api.createNamespacedLease(namespace, lease, null, null, null, null)
        Logger.info("Created lease $leaseName in namespace $namespace")
    }

    /**
     * Updates the lease with a new timeout.
     *
     * @param timeoutSeconds The timeout for the lease in seconds
     */
    private fun updateLease(timeoutSeconds: Int) {
        val existingLease = coordinationV1Api.readNamespacedLease(leaseName, namespace, null)

        existingLease.spec = V1LeaseSpec()
            .holderIdentity(holderIdentity)
            .leaseDurationSeconds(timeoutSeconds)

        coordinationV1Api.replaceNamespacedLease(leaseName, namespace, existingLease, null, null, null, null)
        Logger.info("Updated lease $leaseName in namespace $namespace")
    }

    /**1
     * Waits for the lease to become available.
     *
     * @param maxWaitMinutes The maximum time to wait in minutes
     * @return true if the lease was acquired, false if the wait timed out
     */
    fun waitForLease(maxWaitMinutes: Int): Boolean {
        val startTime = System.currentTimeMillis()
        val maxWaitMillis = TimeUnit.MINUTES.toMillis(maxWaitMinutes.toLong())

        // Check every 10 seconds
        val checkIntervalMillis = TimeUnit.SECONDS.toMillis(10)

        while (System.currentTimeMillis() - startTime < maxWaitMillis) {
            try {
                // Try to acquire the lease
                if (acquireLease(30)) {
                    return true
                }

                // Wait before trying again
                Logger.info("Waiting for lease $leaseName to become available...")
                Thread.sleep(checkIntervalMillis)

            } catch (e: Exception) {
                Logger.error("Error while waiting for lease: ${e.message}")
                // Continue waiting despite errors
            }
        }

        Logger.warn("Timed out waiting for lease $leaseName after $maxWaitMinutes minutes")
        return false
    }

    /**
     * Releases the lease.
     */
    private fun releaseLease() {
        try {
            // We don't actually delete the lease, just update it to show it's released
            // by clearing the holder identity
            val existingLease = coordinationV1Api.readNamespacedLease(leaseName, namespace, null)

            existingLease.spec = V1LeaseSpec()
                .holderIdentity("") // Clear the holder identity to indicate it's released
                .renewTime(null)
                .acquireTime(null)

            coordinationV1Api.replaceNamespacedLease(leaseName, namespace, existingLease, null, null, null, null)
            Logger.info("Released lease $leaseName in namespace $namespace")
            leaseAcquired = false
        } catch (e: Exception) {
            Logger.error("Error releasing lease: ${e.message}")
            throw e
        }
    }

    /**
     * Releases the lease when the instance is closed.
     * Also stops the lease renewal thread and closes the port forwarder.
     */
    override fun close() {
        try {
            // Stop the renewal thread
            running = false
            renewalThread?.interrupt()
            try {
                renewalThread?.join(5000) // Wait up to 5 seconds for the thread to terminate
            } catch (e: InterruptedException) {
                Logger.warn("Interrupted while waiting for renewal thread to terminate")
            }

            // Release the lease if it was acquired
            if (leaseAcquired) {
                releaseLease()
                Logger.info("Lease released successfully")
            }
        } catch (e: Exception) {
            Logger.error("Failed to release lease: ${e.message}")
        } finally {
            portForwarder.close()
            Logger.info("Port forwarder closed successfully")
        }

    }

    /**
     * Releases the lease if it was acquired and there was an error.
     */
    fun releaseLeaseIfAcquired() {
        if (leaseAcquired) {
            try {
                releaseLease()
                Logger.info("Lease released due to error")
            } catch (releaseEx: Exception) {
                Logger.error(releaseEx, "Failed to release lease after error")
            }
        }
    }
}

fun V1Lease.isAvailable(): Boolean = spec == null || spec?.holderIdentity == null ||
        (spec?.acquireTime == null && spec?.renewTime == null) ||
        (spec?.renewTime ?: spec?.acquireTime)?.let { leaseTime ->
            val leaseDuration = (spec?.leaseDurationSeconds ?: 0).toLong()
            val expirationTime = leaseTime.plusSeconds(leaseDuration)
            OffsetDateTime.now().isAfter(expirationTime)
        } ?: true
