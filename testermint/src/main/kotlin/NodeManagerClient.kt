package com.productscience

import com.productscience.nodemanager.NodeManagerGrpc
import com.productscience.nodemanager.NodeManagerProto
import io.grpc.ManagedChannel
import io.grpc.ManagedChannelBuilder
import java.io.Closeable
import java.util.concurrent.TimeUnit

class NodeManagerClient(host: String, port: Int) : Closeable {
    private val channel: ManagedChannel = ManagedChannelBuilder
        .forAddress(host, port)
        .usePlaintext()
        .build()

    private val stub = NodeManagerGrpc.newBlockingStub(channel)

    fun acquireMLNode(model: String, excludedNodes: List<String> = emptyList()): NodeManagerProto.AcquireMLNodeResponse {
        val request = NodeManagerProto.AcquireMLNodeRequest.newBuilder()
            .setModel(model)
            .addAllExcludedNodes(excludedNodes)
            .build()
        return stub.withDeadlineAfter(30, TimeUnit.SECONDS).acquireMLNode(request)
    }

    fun releaseMLNode(lockId: String, outcome: NodeManagerProto.ReleaseOutcome = NodeManagerProto.ReleaseOutcome.SUCCESS): NodeManagerProto.ReleaseMLNodeResponse {
        val request = NodeManagerProto.ReleaseMLNodeRequest.newBuilder()
            .setLockId(lockId)
            .setOutcome(outcome)
            .build()
        return stub.withDeadlineAfter(30, TimeUnit.SECONDS).releaseMLNode(request)
    }

    /**
     * @param maxWaitSeconds null = omit field (legacy 3a client); 0 = explicit immediate reply.
     */
    fun getRuntimeConfig(
        clientParamsBlockHeight: Long = 0,
        maxWaitSeconds: Int? = null,
    ): NodeManagerProto.GetRuntimeConfigResponse {
        val requestBuilder = NodeManagerProto.GetRuntimeConfigRequest.newBuilder()
            .setClientParamsBlockHeight(clientParamsBlockHeight)
        if (maxWaitSeconds != null) {
            requestBuilder.maxWaitSeconds = maxWaitSeconds
        }
        val deadlineSeconds = when {
            maxWaitSeconds == null || maxWaitSeconds <= 0 -> 10L
            else -> maxWaitSeconds.toLong() + 5L
        }
        return stub.withDeadlineAfter(deadlineSeconds, TimeUnit.SECONDS)
            .getRuntimeConfig(requestBuilder.build())
    }

    override fun close() {
        channel.shutdown().awaitTermination(5, TimeUnit.SECONDS)
    }
}
