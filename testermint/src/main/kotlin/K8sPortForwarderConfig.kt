package com.productscience

/**
 * Configuration for a single port forward
 */
data class PortForwardConfig(
    val serverType: String,  // public, ml, or admin
    val remotePort: Int,     // port on the remote pod
    val maxConnections: Int = 10,
    val socketTimeout: Int = 120000, // 2 minutes
    val serverSocketBacklog: Int = 50
)

object K8sPortForwarderConfig {
    val PORT_CONFIGURATIONS = listOf(
        PortForwardConfig(SERVER_TYPE_PUBLIC, 9000),
        PortForwardConfig(SERVER_TYPE_ML, 9100),
        PortForwardConfig(SERVER_TYPE_ADMIN, 9200)
    )
}
