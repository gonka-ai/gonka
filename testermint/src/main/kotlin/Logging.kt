package com.productscience

import org.tinylog.ThreadContext
import org.tinylog.kotlin.Logger

fun <T> logContext(context: Map<String, String>, block: () -> T): T {
    val outerContext = ThreadContext.getMapping()
    context.forEach {
        ThreadContext.put(it.key, it.value)
    }
    val result = block()
    context.keys.forEach {
        ThreadContext.remove(it)
    }
    outerContext.forEach {
        ThreadContext.put(it.key, it.value)
    }
    return result
}

interface HasConfig {
    val config: ApplicationConfig
    fun <T> wrapLog(operation: String, infoLevel: Boolean, block: () -> T): T =
        logContext(
            mapOf(
                "operation" to operation,
                "pair" to config.pairName,
                "source" to "testermint"
            )
        ) {
            if (infoLevel) {
                Logger.info("Start $operation")
            } else {
                Logger.debug("Start $operation")
            }
            val result = block()
            if (infoLevel) {
                Logger.info("End $operation")
            } else {
                Logger.debug("End $operation")
            }
            result
        }
}
