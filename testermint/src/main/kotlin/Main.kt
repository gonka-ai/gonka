package com.productscience

import com.github.kittinunf.fuel.core.FuelError
import com.google.gson.FieldNamingPolicy
import com.google.gson.Gson
import com.google.gson.GsonBuilder
import com.productscience.data.*
import org.reflections.Reflections
import org.tinylog.kotlin.Logger
import java.time.Duration
import java.time.Instant

fun main() {
    val pairs = getLocalInferencePairs(inferenceConfig)
    val highestFunded = initialize(pairs)
    val inference = generateSequence {
        getInferenceResult(highestFunded)
    }.first { it.inference.executedBy != it.inference.requestedBy }

    println("ERC:" + inference.executorRefundChange)
    println("RRC:" + inference.requesterRefundChange)
    println("EOW:" + inference.executorOwedChange)
    println("ROW:" + inference.requesterOwedChange)
    println("EBC:" + inference.executorBalanceChange)
    println("RBC:" + inference.requesterBalanceChange)
}

fun getInferenceResult(
    highestFunded: LocalInferencePair,
    modelName: String? = null,
    seed: Int? = null,
    baseRequest:InferenceRequestPayload  = inferenceRequestObject
): InferenceResult {
    val beforeInferenceParticipants = highestFunded.api.getParticipants()
    val inferenceObject = baseRequest
        .copy(seed = seed ?: baseRequest.seed)
        .copy(model = modelName ?: baseRequest.model)
    val payload = inferenceObject.toJson()

    val inference = makeInferenceRequest(highestFunded, payload)
    val afterInference = highestFunded.api.getParticipants()
    return createInferenceResult(inference, afterInference, beforeInferenceParticipants)
}

fun getStreamingInferenceResult(
    highestFunded: LocalInferencePair,
    modelName: String? = null,
    seed: Int? = null
): InferenceResult {
    val beforeInferenceParticipants = highestFunded.api.getParticipants()
    val inferenceObject = inferenceRequestStreamObject
        .copy(seed = seed ?: inferenceRequestStreamObject.seed)
        .copy(model = modelName ?: inferenceRequestStreamObject.model)
    val payload = inferenceObject.toJson()

    val inference = makeStreamingInferenceRequest(highestFunded, payload)
    val afterInference = highestFunded.api.getParticipants()
    return createInferenceResult(inference, afterInference, beforeInferenceParticipants)
}

/**
 * Gets an inference result from an interrupted streaming request.
 * This is used to test billing and validation when a stream is interrupted.
 *
 * @param highestFunded The LocalInferencePair to use for the request
 * @param modelName Optional model name to use
 * @param seed Optional seed to use
 * @param maxLinesToRead The maximum number of lines to read before interrupting (default: 2)
 * @return The inference result
 */
fun getInterruptedStreamingInferenceResult(
    highestFunded: LocalInferencePair,
    modelName: String? = null,
    seed: Int? = null,
    maxLinesToRead: Int = 2,
    baseObject: InferenceRequestPayload = inferenceRequestStreamObject
): InferenceResult {
    val beforeInferenceParticipants = highestFunded.api.getParticipants().also { Logger.info("Before inference: $it") }
    val inferenceObject = baseObject
        .copy(seed = seed ?: baseObject.seed)
        .copy(model = modelName ?: baseObject.model)
    val payload = inferenceObject.toJson()

    val inference = makeInterruptedStreamingInferenceRequest(highestFunded, payload, maxLinesToRead, checkFinished = true)
    val afterInference = highestFunded.api.getParticipants().also { Logger.info("After inference: $it") }
    return createInferenceResult(inference, afterInference, beforeInferenceParticipants)
}

fun createInferenceResult(
    inference: InferencePayload,
    afterInference: List<Participant>,
    beforeInferenceParticipants: List<Participant>,
): InferenceResult {
    val requester = inference.requestedBy
    val executor = inference.executedBy
    check(requester != null) { "Requester not found in participants after inference" }
    check(executor != null) { "Executor not found in inference" }
    val requesterParticipantAfter = afterInference.find { it.id == requester }
    val executorParticipantAfter = afterInference.find { it.id == executor }
    val requesterParticipantBefore = beforeInferenceParticipants.find { it.id == requester }
    val executorParticipantBefore = beforeInferenceParticipants.find { it.id == executor }
    check(requesterParticipantAfter != null) { "Requester not found in participants after inference" }
    check(executorParticipantAfter != null) { "Executor not found in participants after inference" }
    check(requesterParticipantBefore != null) { "Requester not found in participants before inference" }
    check(executorParticipantBefore != null) { "Executor not found in participants before inference" }
    return InferenceResult(
        inference = inference,
        requesterBefore = requesterParticipantBefore,
        executorBefore = executorParticipantBefore,
        requesterAfter = requesterParticipantAfter,
        executorAfter = executorParticipantAfter,
        beforeParticipants = beforeInferenceParticipants,
        afterParticipants = afterInference,
    )
}

data class InferenceResult(
    val inference: InferencePayload,
    val requesterBefore: Participant,
    val executorBefore: Participant,
    val requesterAfter: Participant,
    val executorAfter: Participant,
    val beforeParticipants: List<Participant>,
    val afterParticipants: List<Participant>,
) {
    val requesterOwedChange = requesterAfter.coinsOwed - requesterBefore.coinsOwed
    val executorOwedChange = executorAfter.coinsOwed - executorBefore.coinsOwed
    val requesterRefundChange = requesterAfter.refundsOwed - requesterBefore.refundsOwed
    val executorRefundChange = executorAfter.refundsOwed - executorBefore.refundsOwed
    val requesterBalanceChange = requesterAfter.balance - requesterBefore.balance
    val executorBalanceChange = executorAfter.balance - executorBefore.balance
}

fun makeInferenceRequest(highestFunded: LocalInferencePair, payload: String): InferencePayload {
    highestFunded.waitForFirstValidators()
    val response = highestFunded.makeInferenceRequest(payload)
    Logger.info("Inference response: ${response.choices.first().message.content}")
    val inferenceId = response.id

    val inference = generateSequence {
        highestFunded.node.waitForNextBlock(2)
        try {
            highestFunded.api.getInference(inferenceId)
        } catch (_: FuelError) {
            InferencePayload.empty()
        }
    }.take(5).firstOrNull { it.checkComplete() }
    check(inference != null) { "Inference never logged in chain" }
    return inference
}

private fun makeStreamingInferenceRequest(highestFunded: LocalInferencePair, payload: String): InferencePayload {
    highestFunded.waitForFirstValidators()

    // Create a stream connection
    val streamConnection = highestFunded.streamInferenceRequest(payload)

    // Read all lines from the stream to get the inference ID and complete the request
    var inferenceId: String? = null
    var lineCount = 0
    var done = false

    try {
        // Read lines until we find the [DONE] event
        while (!done) {
            val line = streamConnection.readLine() ?: break
            lineCount++

            // Check if this is the [DONE] event
            if (line.contains("[DONE]")) {
                done = true
                Logger.info("Received [DONE] event after reading $lineCount lines")
                continue
            }

            Logger.info("Read line: $line")
            // Parse the line to extract the inference ID if we haven't found it yet
            if (inferenceId == null && line.startsWith("data: ") && !line.contains("[DONE]")) {
                val jsonData = line.substring(6) // Remove "data: " prefix
                try {
                    val jsonNode = cosmosJson.fromJson(jsonData, Map::class.java)
                    inferenceId = jsonNode["id"] as? String
                    if (inferenceId != null) {
                        Logger.info("Found inference ID: $inferenceId")
                    }
                } catch (e: Exception) {
                    Logger.warn("Failed to parse JSON from stream: $e")
                }
            }
        }

        // Close the stream after reading all lines
        streamConnection.close()
        Logger.info("Completed stream request, read $lineCount lines total")
    } catch (e: Exception) {
        Logger.error(e, "Error reading from stream")
        streamConnection.close()
    }

    check(inferenceId != null) { "Failed to get inference ID from stream" }

    // Wait for the inference to be logged in the chain
    val inference = generateSequence {
        highestFunded.node.waitForNextBlock(2)
        try {
            highestFunded.api.getInference(inferenceId)
        } catch (_: FuelError) {
            InferencePayload.empty()
        }
    }.take(5).firstOrNull { it.checkComplete() }

    check(inference != null) { "Inference never logged in chain" }
    return inference
}

/**
 * Makes a streaming inference request and interrupts it after reading a few lines.
 * This is used to test billing and validation when a stream is interrupted.
 *
 * @param highestFunded The LocalInferencePair to use for the request
 * @param payload The request payload
 * @param maxLinesToRead The maximum number of lines to read before interrupting (default: 2)
 * @return The inference payload
 */
fun makeInterruptedStreamingInferenceRequest(
    highestFunded: LocalInferencePair,
    payload: String,
    maxLinesToRead: Int = 1,
    checkStarted: Boolean = true,
    checkFinished: Boolean = false,
): InferencePayload {
    highestFunded.waitForFirstValidators()

    // Create a stream connection
    val streamConnection = highestFunded.streamInferenceRequest(payload)

    // Read only a few lines from the stream to get the inference ID and then interrupt
    var inferenceId: String? = null
    var lineCount = 0

    try {
        // Read only a limited number of lines
        while (lineCount < maxLinesToRead) {
            val line = streamConnection.readLine() ?: break
            lineCount++

            Logger.info("Read line: $line")
            // Parse the line to extract the inference ID if we haven't found it yet
            if (inferenceId == null && line.startsWith("data: ") && !line.contains("[DONE]")) {
                val jsonData = line.substring(6) // Remove "data: " prefix
                try {
                    val jsonNode = cosmosJson.fromJson(jsonData, Map::class.java)
                    inferenceId = jsonNode["id"] as? String
                    if (inferenceId != null) {
                        Logger.info("Found inference ID: $inferenceId")
                    }
                } catch (e: Exception) {
                    Logger.warn("Failed to parse JSON from stream: $e")
                }
            }
        }

        // Deliberately interrupt the stream by closing the connection
        Logger.info("Deliberately interrupting stream after reading $lineCount lines")
        streamConnection.close()
    } catch (e: Exception) {
        Logger.error(e, "Error reading from stream")
        streamConnection.close()
    }

    logSection("Waiting for stream to complete")
    Thread.sleep(10000)
    if (!checkStarted && !checkFinished) {
        return InferencePayload.empty()
    }
    check(inferenceId != null) { "Failed to get inference ID from stream before interruption" }

    // Wait for the inference to be logged in the chain
    val inference = generateSequence {
        highestFunded.node.waitForNextBlock(2)
        try {
            highestFunded.api.getInference(inferenceId)
        } catch (_: FuelError) {
            InferencePayload.empty()
        }
    }
        .take(5)
        .firstOrNull {
            it.inferenceId.isNotEmpty() && (!checkFinished || it.checkComplete())
        }

    // Note: We don't check if the inference is complete, as it may not be due to interruption
    return inference ?: InferencePayload.empty()
}

fun initialize(pairs: List<LocalInferencePair>, resetMlNodes: Boolean = true): LocalInferencePair {
    pairs.forEach {
        it.waitForFirstBlock()
        it.waitForFirstValidators()

        if (resetMlNodes) {
            resetMlNodesToDefault(it)
        }

        it.mock?.resetMocks()
        it.mock?.setInferenceResponse(defaultInferenceResponseObject, streamDelay = Duration.ofMillis(200))
        val params = it.getParams()
        
        // Sanity check: verify PoC v2 is enabled (model_id set in poc_params)
        val pocParams = params.pocParams
        if (pocParams.modelId.isNullOrEmpty()) {
            Logger.warn("PoC v2 is NOT enabled! Chain params show poc_params.model_id is empty. " +
                "Tests may be using old PoC v1 implementation. Check genesis spec configuration.")
        } else {
            Logger.info("PoC v2 enabled: modelId={}, seqLen={}", pocParams.modelId, pocParams.seqLen)
        }
        
        it.node.getColdAddress()
        it.node.getWarmAddress()
    }

    val balances = pairs.zip(pairs.map { it.node.getSelfBalance(it.node.config.denom) })

    val (fundedPairs, unfundedPairs) = balances.partition { it.second > 0 }
    val funded = fundedPairs.map { it.first }
    val unfunded = unfundedPairs.map { it.first }
    val highestFunded = balances.maxByOrNull { it.second }?.first
    if (highestFunded == null) {
        println("No funded nodes")
        throw IllegalStateException("No funded nodes")
    }
    val currentParticipants = highestFunded.api.getParticipants()
    for (pair in funded) {
        if (currentParticipants.none { it.id == pair.node.getColdAddress() }) {
            pair.addSelfAsParticipant(listOf(defaultModel))
        }
    }

    highestFunded.node.waitForNextBlock(2)
    pairs.forEach { pair ->
        pair.waitForBlock((highestFunded.getParams().epochParams.epochLength * 2).toInt() + 2) {
            val address = pair.node.getColdAddress()
            val stats = pair.node.getParticipantCurrentStats()
            val weight = stats.participantCurrentStats?.find { it.participantId == address }?.weight ?: 0
            weight != 0L
        }
    }

    pairs.forEach { pair ->
        pair.waitForMlNodesToLoad()
    }

    return highestFunded
}

private fun resetMlNodesToDefault(pair: LocalInferencePair) {
    val pairName = pair.name.trim('/')
    val defaultNode = validNode.copy(host = "ml-0000.$pairName.test")

    // We're not really supposed to change nodes in the middle of an epoch
    // This optimization might help avoid unnecessary changes
    val actualNodes = pair.api.getNodes()
    if (actualNodes.size == 1) {
        val currentNode = actualNodes.first()
        if (currentNode.node.host == defaultNode.host
            && currentNode.node.pocPort == defaultNode.pocPort
            && currentNode.node.inferencePort == defaultNode.inferencePort
            && currentNode.node.models == defaultNode.models
            && currentNode.node.id == defaultNode.id
            && currentNode.node.maxConcurrent == defaultNode.maxConcurrent) {
            Logger.info("Node already set to default: {}", currentNode.node.host)
            return
        }
    }

    Logger.info { "Resetting ml nodes" }
    pair.waitForNextInferenceWindow(windowSizeInBlocks = 5)
    pair.api.setNodesTo(defaultNode)
}

private fun TxResponse.assertSuccess() {
    if (code != 0) {
        throw IllegalStateException("Transaction failed: $rawLog")
    }
}

val defaultFunding = 20_000_000L
fun GsonBuilder.registerCosmosTypes(): GsonBuilder {
    return this.registerTypeAdapter(Instant::class.java, InstantDeserializer())
        .registerTypeAdapter(Duration::class.java, DurationDeserializer())
        .registerTypeAdapter(Duration::class.java, DurationSerializer())
        .registerTypeAdapter(Pubkey2::class.java, Pubkey2Deserializer())
        .registerTypeAdapter(Int::class.java, IntDeserializer())
        .registerTypeAdapter(Integer::class.java, IntDeserializer())
        .registerTypeAdapter(Long::class.java, LongDeserializer())
        .registerTypeAdapter(java.lang.Long::class.java, LongSerializer())
        .registerTypeAdapter(java.lang.Long::class.java, LongDeserializer())
        .registerTypeAdapter(java.lang.Double::class.java, DoubleSerializer())
        .registerTypeAdapter(java.lang.Float::class.java, FloatSerializer())
        .registerTypeAdapter(ConfirmationPoCPhase::class.java, ConfirmationPoCPhaseDeserializer())
        .registerTypeAdapter(InferenceStatus::class.java, InferenceStatusDeserializer())
        .registerTypeAdapter(SubnetInferenceStatus::class.java, SubnetInferenceStatusDeserializer())
        .registerTypeAdapter(ProposalStatus::class.java, ProposalStatusDeserializer())
}

val cosmosJson: Gson = GsonBuilder()
    .setFieldNamingPolicy(com.google.gson.FieldNamingPolicy.LOWER_CASE_WITH_UNDERSCORES)
    .registerCosmosTypes()
    .registerMessages("com.productscience.data", FieldNamingPolicy.LOWER_CASE_WITH_UNDERSCORES)
    .create()

val openAiJson: Gson = GsonBuilder()
    .setFieldNamingPolicy(com.google.gson.FieldNamingPolicy.LOWER_CASE_WITH_UNDERSCORES)
    .registerTypeAdapter(Instant::class.java, InstantDeserializer())
    .registerTypeAdapter(Duration::class.java, DurationDeserializer())
    .create()

val gsonCamelCase = createGsonWithTxMessageSerializers("com.productscience.data")

fun createGsonWithTxMessageSerializers(packageName: String): Gson {
    return GsonBuilder()
        .setFieldNamingPolicy(com.google.gson.FieldNamingPolicy.IDENTITY)
        .registerCosmosTypes()
        .registerMessages(packageName, FieldNamingPolicy.IDENTITY)
        .create()
}

private fun GsonBuilder.registerMessages(packageName: String, fieldNamingPolicy: FieldNamingPolicy): GsonBuilder {
    // Scan the package to get all `TxMessage` implementations
    val reflections = Reflections(packageName)
    val txMessageSubtypes = reflections.getSubTypesOf(TxMessage::class.java)

    // Register `MessageSerializer` for each implementation of `TxMessage`
    txMessageSubtypes.forEach { subclass ->
        if (!subclass.isInterface) { // Ignore interfaces and abstract classes
            registerTypeAdapter(subclass, MessageSerializer(fieldNamingPolicy))
        }
    }
    return this
}

val inferenceConfig = ApplicationConfig(
    appName = "inferenced",
    chainId = "prod-sim",
    nodeImageName = "ghcr.io/product-science/inferenced",
    genesisNodeImage = "ghcr.io/product-science/inferenced",
    mockImageName = "inference-mock-server",
    apiImageName = "ghcr.io/product-science/api",
    denom = "ngonka",
    stateDirName = ".inference",
    // TODO: probably need to add more to the spec here, so if tests change them we change back
    genesisSpec = createSpec()
)

fun createSpec(epochLength: Long = 15L, epochShift: Int = 0): Spec<AppState> = spec {
    this[AppState::gov] = spec<GovState> {
        this[GovState::params] = spec<GovParams> {
            this[GovParams::votingPeriod] = Duration.ofSeconds(30)
            this[GovParams::minDeposit] = listOf(Coin("ngonka", 1000))
        }
    }
    this[AppState::inference] = spec<InferenceState> {
        this[InferenceState::params] = spec<InferenceParams> {
            this[InferenceParams::epochParams] = spec<EpochParams> {
                this[EpochParams::epochLength] = epochLength
                this[EpochParams::pocStageDuration] = 2L
                this[EpochParams::pocExchangeDuration] = 1L
                this[EpochParams::pocValidationDelay] = 1L
                this[EpochParams::pocValidationDuration] = 2L
                this[EpochParams::setNewValidatorsDelay] = 1L
                this[EpochParams::epochShift] = epochShift
            }
            this[InferenceParams::validationParams] = spec<ValidationParams> {
                this[ValidationParams::minValidationAverage] = Decimal.fromDouble(0.01)
                this[ValidationParams::maxValidationAverage] = Decimal.fromDouble(1.0)
                this[ValidationParams::epochsToMax] = 100L // Easy to calculate/check
                this[ValidationParams::fullValidationTrafficCutoff] = 100L
                this[ValidationParams::minValidationHalfway] = Decimal.fromDouble(0.05)
                this[ValidationParams::minValidationTrafficCutoff] = 10L
                this[ValidationParams::expirationBlocks] = 7L
                this[ValidationParams::claimValidationEnabled] = true
            }
            this[InferenceParams::dynamicPricingParams] = spec<DynamicPricingParams> {
                this[DynamicPricingParams::stabilityZoneLowerBound] = Decimal.fromDouble(0.40)
                this[DynamicPricingParams::stabilityZoneUpperBound] = Decimal.fromDouble(0.60)
                this[DynamicPricingParams::priceElasticity] = Decimal.fromDouble(0.05)
                this[DynamicPricingParams::utilizationWindowDuration] = 60L
                this[DynamicPricingParams::minPerTokenPrice] = 1000L  // Set to match DEFAULT_TOKEN_COST
                this[DynamicPricingParams::basePerTokenPrice] = 1000L // Set to match DEFAULT_TOKEN_COST
                this[DynamicPricingParams::gracePeriodEndEpoch] = 0L   // Disable grace period
                this[DynamicPricingParams::gracePeriodPerTokenPrice] = 0L
            }
            // Enable PoC v2 by setting model_id and seq_len in poc_params
            this[InferenceParams::pocParams] = spec<PocParams> {
                this[PocParams::modelId] = defaultModel
                this[PocParams::seqLen] = 256L
                this[PocParams::pocV2Enabled] = true
                this[PocParams::validationSlots] = 2L
                this[PocParams::pocNormalizationEnabled] = false
            }
        }
        this[InferenceState::modelList] = listOf(
            ModelListItem(
                proposedBy = "genesis",
                id = secondModel,
                unitsOfComputePerToken = "1000",
                hfRepo = secondModel,
                hfCommit = "976055f8c83f394f35dbd3ab09a285a984907bd0",
                modelArgs = listOf("--quantization", "fp8", "--kv-cache-dtype", "fp8"),
                vRam = "32",
                throughputPerNonce = "1000",
                validationThreshold = Decimal.fromDouble(0.85),
            ),
            ModelListItem(
                proposedBy = "genesis",
                id = defaultModel,
                unitsOfComputePerToken = "100",
                hfRepo = defaultModel,
                hfCommit = "a09a35458c702b33eeacc393d103063234e8bc28",
                modelArgs = listOf("--quantization", "fp8"),
                vRam = "16",
                throughputPerNonce = "10000",
                validationThreshold = Decimal.fromDouble(0.85),
            )
        )
    }

    // Default restrictions module params (tests can override via spec in test files)
    this[AppState::restrictions] = spec<RestrictionsState> {
        this[RestrictionsState::params] = spec<RestrictionsParams> {
            // Set a sane default far in the future so tests relying on default behavior keep working
            this[RestrictionsParams::restrictionEndBlock] = 1_555_000L
            this[RestrictionsParams::emergencyTransferExemptions] = emptyList<EmergencyTransferExemption>()
            this[RestrictionsParams::exemptionUsageTracking] = emptyList<ExemptionUsageEntry>()
        }
    }
}


data class ChatMessage(
    val role: String,
    val content: String,
    val toolCalls: List<Any>? = null
)

data class InferenceRequestPayload(
    val model: String,
    val temperature: Double,
    val messages: List<ChatMessage>,
    val seed: Int,
    val maxCompletionTokens: Int? = null,
    val maxTokens: Int? = null,
    val stream: Boolean = false
) {
    fun toJson() = cosmosJson.toJson(this)

    fun textLength(): Int {
        var promptText = ""
        for (message in messages) {
            promptText += message.content + "\n"
        }
        return promptText.length
    }
}

const val defaultModel = "Qwen/Qwen2.5-7B-Instruct"
const val secondModel = "Qwen/QwQ-32B"

val inferenceRequestObject = InferenceRequestPayload(
    model = defaultModel,
    temperature = 0.8,
    messages = listOf(
        ChatMessage("system", "Regardless of the language of the question, answer in english"),
        ChatMessage("user", "When did Hawaii become a state")
    ),
    seed = -25
)

val inferenceRequest = cosmosJson.toJson(inferenceRequestObject)

// Raw JSON fixture for OpenAI-style multipart content (text + image_url parts).
// Kept as a string to preserve the heterogeneous `content` array shape.
val inferenceRequestMultipart = """
{
  "model": "$defaultModel",
  "temperature": 0.8,
  "messages": [
    {
      "role": "system",
      "content": "Answer briefly and include the image context when present."
    },
    {
      "role": "user",
      "content": [
        { "type": "text", "text": "What is in this image?" },
        { "type": "image_url", "image_url": { "url": "https://example.com/cat.png" } },
        { "type": "text", "text": "Respond in one sentence." }
      ]
    }
  ],
  "seed": -25
}
""".trimIndent()

val inferenceRequestStreamObject = inferenceRequestObject.copy(stream = true)
val inferenceRequestStream = cosmosJson.toJson(inferenceRequestStreamObject)

val validNode = InferenceNode(
    host = "36.189.234.237:19009/",
    pocPort = 8080,
    inferencePort = 8080,
    models = mapOf(
        defaultModel to ModelConfig(
            args = emptyList()
        )
    ),
    id = "wiremock2",
    maxConcurrent = 1000
)

val defaultInferenceResponse = """
    {"id": "chatcmpl-9278f63dd6e04c16847aa2f558caeadd", "object": "chat.completion", "created": 1750456846, "model": "$defaultModel", "choices": [
        {"index": 0, "message": {"role": "assistant", "reasoning_content": null, "content": "Hello! I'm just a large language model, so I don't have feelings or physical form. How can I assist you today?", "tool_calls": []
            }, "logprobs": {"content": [
                    {"token": "9707", "logprob": 0.0, "bytes": [72, 101, 108, 108, 111], "top_logprobs": [
                            {"token": "9707", "logprob": 0.0, "bytes": [57, 55, 48, 55]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "0", "logprob": 0.0, "bytes": [33], "top_logprobs": [
                            {"token": "0", "logprob": 0.0, "bytes": [48]},
                            {"token": "2", "logprob": -9999.0, "bytes": [50]},
                            {"token": "4", "logprob": -9999.0, "bytes": [52]}
                        ]
                    },
                    {"token": "358", "logprob": 0.0, "bytes": [32, 73], "top_logprobs": [
                            {"token": "358", "logprob": 0.0, "bytes": [51, 53, 56]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "2776", "logprob": 0.0, "bytes": [39, 109], "top_logprobs": [
                            {"token": "2776", "logprob": 0.0, "bytes": [50, 55, 55, 54]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "1101", "logprob": -1.2367870807647705, "bytes": [32, 106, 117, 115, 116], "top_logprobs": [
                            {"token": "1101", "logprob": -1.2367870807647705, "bytes": [49, 49, 48, 49]},
                            {"token": "458", "logprob": -0.34293481707572937, "bytes": [52, 53, 56]},
                            {"token": "0", "logprob": -9999.0, "bytes": [48]}
                        ]
                    },
                    {"token": "264", "logprob": 0.0, "bytes": [32, 97], "top_logprobs": [
                            {"token": "264", "logprob": 0.0, "bytes": [50, 54, 52]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "3460", "logprob": -1.3776320219039917, "bytes": [32, 108, 97, 114, 103, 101], "top_logprobs": [
                            {"token": "3460", "logprob": -1.3776320219039917, "bytes": [51, 52, 54, 48]},
                            {"token": "7377", "logprob": -1.0200899839401245, "bytes": [55, 51, 55, 55]},
                            {"token": "6366", "logprob": -1.5564039945602417, "bytes": [54, 51, 54, 54]}
                        ]
                    },
                    {"token": "4128", "logprob": 0.0, "bytes": [32, 108, 97, 110, 103, 117, 97, 103, 101], "top_logprobs": [
                            {"token": "4128", "logprob": 0.0, "bytes": [52, 49, 50, 56]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "1614", "logprob": 0.0, "bytes": [32, 109, 111, 100, 101, 108], "top_logprobs": [
                            {"token": "1614", "logprob": 0.0, "bytes": [49, 54, 49, 52]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "11", "logprob": 0.0, "bytes": [44], "top_logprobs": [
                            {"token": "11", "logprob": 0.0, "bytes": [49, 49]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "773", "logprob": 0.0, "bytes": [32, 115, 111], "top_logprobs": [
                            {"token": "773", "logprob": 0.0, "bytes": [55, 55, 51]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "358", "logprob": 0.0, "bytes": [32, 73], "top_logprobs": [
                            {"token": "358", "logprob": 0.0, "bytes": [51, 53, 56]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "1513", "logprob": 0.0, "bytes": [32, 100, 111, 110], "top_logprobs": [
                            {"token": "1513", "logprob": 0.0, "bytes": [49, 53, 49, 51]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "944", "logprob": 0.0, "bytes": [39, 116], "top_logprobs": [
                            {"token": "944", "logprob": 0.0, "bytes": [57, 52, 52]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "614", "logprob": 0.0, "bytes": [32, 104, 97, 118, 101], "top_logprobs": [
                            {"token": "614", "logprob": 0.0, "bytes": [54, 49, 52]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "15650", "logprob": 0.0, "bytes": [32, 102, 101, 101, 108, 105, 110, 103, 115], "top_logprobs": [
                            {"token": "15650", "logprob": 0.0, "bytes": [49, 53, 54, 53, 48]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "476", "logprob": 0.0, "bytes": [32, 111, 114], "top_logprobs": [
                            {"token": "476", "logprob": 0.0, "bytes": [52, 55, 54]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "6961", "logprob": 0.0, "bytes": [32, 112, 104, 121, 115, 105, 99, 97, 108], "top_logprobs": [
                            {"token": "6961", "logprob": 0.0, "bytes": [54, 57, 54, 49]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "1352", "logprob": -0.3982061743736267, "bytes": [32, 102, 111, 114, 109], "top_logprobs": [
                            {"token": "1352", "logprob": -0.3982061743736267, "bytes": [49, 51, 53, 50]},
                            {"token": "1584", "logprob": -1.1132903099060059, "bytes": [49, 53, 56, 52]},
                            {"token": "0", "logprob": -9999.0, "bytes": [48]}
                        ]
                    },
                    {"token": "13", "logprob": 0.0, "bytes": [46], "top_logprobs": [
                            {"token": "13", "logprob": 0.0, "bytes": [49, 51]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "2585", "logprob": 0.0, "bytes": [32, 72, 111, 119], "top_logprobs": [
                            {"token": "2585", "logprob": 0.0, "bytes": [50, 53, 56, 53]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "646", "logprob": 0.0, "bytes": [32, 99, 97, 110], "top_logprobs": [
                            {"token": "646", "logprob": 0.0, "bytes": [54, 52, 54]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "358", "logprob": 0.0, "bytes": [32, 73], "top_logprobs": [
                            {"token": "358", "logprob": 0.0, "bytes": [51, 53, 56]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "7789", "logprob": 0.0, "bytes": [32, 97, 115, 115, 105, 115, 116], "top_logprobs": [
                            {"token": "7789", "logprob": 0.0, "bytes": [55, 55, 56, 57]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "498", "logprob": 0.0, "bytes": [32, 121, 111, 117], "top_logprobs": [
                            {"token": "498", "logprob": 0.0, "bytes": [52, 57, 56]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "3351", "logprob": 0.0, "bytes": [32, 116, 111, 100, 97, 121], "top_logprobs": [
                            {"token": "3351", "logprob": 0.0, "bytes": [51, 51, 53, 49]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "30", "logprob": 0.0, "bytes": [63], "top_logprobs": [
                            {"token": "30", "logprob": 0.0, "bytes": [51, 48]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    },
                    {"token": "151645", "logprob": 0.0, "bytes": [], "top_logprobs": [
                            {"token": "151645", "logprob": 0.0, "bytes": [49, 53, 49, 54, 52, 53]},
                            {"token": "1", "logprob": -9999.0, "bytes": [49]},
                            {"token": "3", "logprob": -9999.0, "bytes": [51]}
                        ]
                    }
                ]
            }, "finish_reason": "stop", "stop_reason": null
        }
    ], "usage": {"prompt_tokens": 35, "total_tokens": 63, "completion_tokens": 28, "prompt_tokens_details": null
    }, "prompt_logprobs": null, "kv_transfer_params": null
    }
""".trimIndent()

val defaultInferenceResponseObject = cosmosJson.fromJson(defaultInferenceResponse, OpenAIResponse::class.java)

