package com.productscience

import com.github.dockerjava.api.async.ResultCallback
import com.github.dockerjava.api.model.Frame
import com.github.dockerjava.core.DockerClientBuilder
import com.google.gson.reflect.TypeToken
import com.productscience.data.BalanceResponse
import com.productscience.data.NodeInfoResponse
import com.productscience.data.TxResponse
import com.productscience.data.Validator
import org.tinylog.ThreadContext
import org.tinylog.kotlin.Logger

interface HasConfig {
    val config: ApplicationConfig
    fun <T> wrapLog(operation: String, block: () -> T): T {
        val outerContext = ThreadContext.getMapping()
        ThreadContext.put("operation", operation)
        ThreadContext.put("pair", config.pairName)
        Logger.info("Start $operation")
        val result = block()
        Logger.debug("End $operation")
        ThreadContext.remove("operation")
        ThreadContext.remove("pair")
        outerContext.forEach {
            ThreadContext.put(it.key, it.value)
        }
        return result
    }

}

// Usage
data class ApplicationCLI(val containerId: String, override val config: ApplicationConfig) : HasConfig {
    private val dockerClient = DockerClientBuilder.getInstance()
        .build()

    fun waitForMinimumBlock(minBlockHeight: Long) {
        wrapLog("waitForMinimumBlock") {
            while (true) {
                val currentState = getStatus()
                val currentBlock = currentState.syncInfo.latestBlockHeight
                if (currentBlock >= minBlockHeight) {
                    Logger.info("Block height reached $currentBlock")
                    break
                }
                Logger.info("Current block height is $currentBlock, waiting...")
                Thread.sleep(1000)
            }
        }
    }

    fun waitForNextBlock(blocksToWait: Int = 1) {
        wrapLog("waitForNextBlock") {
            val currentState = getStatus()
            val currentBlock = currentState.syncInfo.latestBlockHeight
            Logger.info("Waiting for block + $blocksToWait after $currentBlock")
            while (true) {
                val newState = getStatus()
                val newBlock = newState.syncInfo.latestBlockHeight
                if (newBlock >= currentBlock + blocksToWait) {
                    Logger.info("New block height is $newBlock")
                    break
                }
                Logger.info("Current block height is $newBlock, waiting...")
                Thread.sleep(1000)
            }
        }
    }

    fun getStatus(): NodeInfoResponse = wrapLog("getStatus") { execAndParse(listOf("status")) }

    var addresss: String? = null
    fun getAddress(): String = wrapLog("getAddress") {
        if (addresss == null) {
            addresss = getKeys()[0].address
        }
        addresss!!
    }

    // Use TypeToken to properly deserialize List<Validator>
    fun getKeys(): List<Validator> = wrapLog("getKeys") {
        execAndParseWithType(
            object : TypeToken<List<Validator>>() {},
            listOf("keys", "list")
        )
    }

    fun getSelfBalance(denom: String): Long = wrapLog("getSelfBalance") {
        val account = getKeys()[0].address
        val balance = getBalance(account, denom)
        balance.balance.amount
    }

    fun getBalance(address: String, denom: String): BalanceResponse = wrapLog("getBalance") {
        execAndParse(listOf("query", "bank", "balance", address, denom))
    }

    // Reified type parameter to abstract out exec and then json to a particular type
    private inline fun <reified T> execAndParse(args: List<String>): T {
        val argsWithJson = listOf(config.appName) + args + "--output" + "json"
        Logger.debug("Executing command: ${argsWithJson.joinToString(" ")}")
        val response = exec(argsWithJson)
        val output = response.joinToString("\n")
        Logger.debug("Output:$output")
        return gson.fromJson(output, T::class.java)
    }

    // New function that allows using TypeToken for proper deserialization of generic types
    private fun <T> execAndParseWithType(typeToken: TypeToken<T>, args: List<String>): T {
        val argsWithJson = (listOf(config.appName) + args + "--output" + "json")
        Logger.debug("Executing command: ${argsWithJson.joinToString(" ")}")
        val response = exec(argsWithJson)
        val output = response.joinToString("\n")
        Logger.debug("Output:$output")
        return gson.fromJson(output, typeToken.type)
    }


    fun exec(args: List<String>): List<String> {
        val execCreateCmdResponse = dockerClient.execCreateCmd(containerId)
            .withAttachStdout(true)
            .withAttachStderr(true)
            .withTty(true)
            .withCmd(*args.toTypedArray())
            .exec()

        val output = ExecCaptureOutput()
        val execResponse = dockerClient.execStartCmd(execCreateCmdResponse.id).exec(output)
        execResponse.awaitCompletion()
        return output.output
    }

    fun signPayload(payload: String): String = wrapLog("signPayload") {
        val response = this.exec(
            listOf(
                config.appName,
                "signature",
                "create",
                // Do we need single quotes here?
                payload
            )
        )

        // so hacky
        response[1].dropWhile { it != ' ' }.drop(1)
    }

    fun transferMoneyTo(destinationNode: ApplicationCLI, amount: Long): TxResponse = wrapLog("transferMoneyTo") {
        val sourceAccount = this.getKeys()[0].address
        val destAccount = destinationNode.getKeys()[0].address
        val response = this.execAndParse<TxResponse>(
            listOf(
                "tx",
                "bank",
                "send",
                sourceAccount,
                destAccount,
                "$amount${config.denom}",
                "--keyring-backend",
                "test",
                "--chain-id=${config.chainId}",
                "--keyring-dir=/root/${config.stateDirName}",
                "--yes",
                "--output",
                "json"
            )
        )
        response
    }

}

class ExecCaptureOutput : ResultCallback.Adapter<Frame>() {
    val output = mutableListOf<String>()
    override fun onNext(frame: Frame) {
        output.add(String(frame.payload).trim())
    }
}
