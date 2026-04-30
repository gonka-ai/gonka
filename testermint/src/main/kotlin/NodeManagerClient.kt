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
        .withDeadlineAfter(30, TimeUnit.SECONDS)

    fun acquireMLNode(model: String, excludedNodes: List<String> = emptyList()): NodeManagerProto.AcquireMLNodeResponse {
        val request = NodeManagerProto.AcquireMLNodeRequest.newBuilder()
            .setModel(model)
            .addAllExcludedNodes(excludedNodes)
            .build()
        return stub.acquireMLNode(request)
    }

    fun releaseMLNode(lockId: String, outcome: NodeManagerProto.ReleaseOutcome = NodeManagerProto.ReleaseOutcome.SUCCESS): NodeManagerProto.ReleaseMLNodeResponse {
        val request = NodeManagerProto.ReleaseMLNodeRequest.newBuilder()
            .setLockId(lockId)
            .setOutcome(outcome)
            .build()
        return stub.releaseMLNode(request)
    }

    override fun close() {
        channel.shutdown().awaitTermination(5, TimeUnit.SECONDS)
    }
}
