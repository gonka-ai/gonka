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
        highestFunded.node.waitForNextBlock()
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
        highestFunded.node.waitForNextBlock()
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
        highestFunded.node.waitForNextBlock()
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

        it.mock?.setInferenceResponse(defaultInferenceResponseObject, streamDelay = Duration.ofMillis(200))
        it.getParams()
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
//    addUnfundedDirectly(unfunded, currentParticipants, highestFunded)
//    fundUnfunded(unfunded, highestFunded)

    highestFunded.node.waitForNextBlock()
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
    val defaultNode = validNode.copy(host = "${pair.name.trim('/')}-mock-server")

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

private fun addUnfundedDirectly(
    unfunded: List<LocalInferencePair>,
    currentParticipants: List<Participant>,
    highestFunded: LocalInferencePair,
) {
    for (pair in unfunded) {
        if (currentParticipants.none { it.id == pair.node.getColdAddress() }) {
            val selfKey = pair.node.getKeys()[0]
            val status = pair.node.getStatus()
            val validatorInfo = status.validatorInfo
            val valPubKey: PubKey = validatorInfo.pubKey
            Logger.debug("PubKey extracted pubkey={}", selfKey.pubkey)
            highestFunded.api.addUnfundedInferenceParticipant(
                UnfundedInferenceParticipant(
                    url = "http://${pair.name}-api:8080",
                    models = listOf(defaultModel),
                    validatorKey = valPubKey.value,
                    pubKey = selfKey.pubkey.key,
                    address = selfKey.address,
                )
            )
        }
    }
}

private fun TxResponse.assertSuccess() {
    if (code != 0) {
        throw IllegalStateException("Transaction failed: $rawLog")
    }
}

val defaultFunding = 20_000_000L
val cosmosJson: Gson = GsonBuilder()
    .setFieldNamingPolicy(com.google.gson.FieldNamingPolicy.LOWER_CASE_WITH_UNDERSCORES)
    .registerTypeAdapter(Instant::class.java, InstantDeserializer())
    .registerTypeAdapter(Duration::class.java, DurationDeserializer())
    .registerTypeAdapter(Duration::class.java, DurationSerializer())
    .registerTypeAdapter(Pubkey2::class.java, Pubkey2Deserializer())
    .registerTypeAdapter(Long::class.java, LongDeserializer())
    .registerTypeAdapter(java.lang.Long::class.java, LongSerializer())
    .registerTypeAdapter(java.lang.Long::class.java, LongDeserializer())
    .registerTypeAdapter(java.lang.Double::class.java, DoubleSerializer())
    .registerTypeAdapter(java.lang.Float::class.java, FloatSerializer())
    .registerTypeAdapter(ConfirmationPoCPhase::class.java, ConfirmationPoCPhaseDeserializer())
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
        .registerTypeAdapter(Instant::class.java, InstantDeserializer())
        .registerTypeAdapter(Duration::class.java, DurationDeserializer())
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
        }
        this[InferenceState::genesisOnlyParams] = spec<GenesisOnlyParams> {
            this[GenesisOnlyParams::topRewardPeriod] = Duration.ofDays(365).toSeconds()
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
    val toolCalls: List<Any> = emptyList()
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
    {
        "id": "cmpl-1c7b769de9b0494694eeee854da0a014",
        "object": "chat.completion",
        "created": 1736220740,
        "model": "$defaultModel",
        "choices": [
            {
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": "Hawaii became a state on August 21, 1959, after it was admitted to the Union as the 50th state of the United States.",
                    "tool_calls": []
                },
                "logprobs": {
                    "content": [
                        {
                            "token": "H",
                            "logprob": -0.05506780371069908,
                            "bytes": [
                                72
                            ],
                            "top_logprobs": [
                                {
                                    "token": "H",
                                    "logprob": -0.05506780371069908,
                                    "bytes": [
                                        72
                                    ]
                                },
                                {
                                    "token": "As",
                                    "logprob": -3.820692777633667,
                                    "bytes": [
                                        65,
                                        115
                                    ]
                                },
                                {
                                    "token": "In",
                                    "logprob": -4.711318016052246,
                                    "bytes": [
                                        73,
                                        110
                                    ]
                                }
                            ]
                        },
                        {
                            "token": "aw",
                            "logprob": -2.3841830625315197e-06,
                            "bytes": [
                                97,
                                119
                            ],
                            "top_logprobs": [
                                {
                                    "token": "aw",
                                    "logprob": -2.3841830625315197e-06,
                                    "bytes": [
                                        97,
                                        119
                                    ]
                                },
                                {
                                    "token": "on",
                                    "logprob": -14.093751907348633,
                                    "bytes": [
                                        111,
                                        110
                                    ]
                                },
                                {
                                    "token": "awa",
                                    "logprob": -14.328126907348633,
                                    "bytes": [
                                        97,
                                        119,
                                        97
                                    ]
                                }
                            ]
                        },
                        {
                            "token": "ai",
                            "logprob": -1.1920922133867862e-06,
                            "bytes": [
                                97,
                                105
                            ],
                            "top_logprobs": [
                                {
                                    "token": "ai",
                                    "logprob": -1.1920922133867862e-06,
                                    "bytes": [
                                        97,
                                        105
                                    ]
                                },
                                {
                                    "token": "ah",
                                    "logprob": -14.609375953674316,
                                    "bytes": [
                                        97,
                                        104
                                    ]
                                },
                                {
                                    "token": "aw",
                                    "logprob": -15.617188453674316,
                                    "bytes": [
                                        97,
                                        119
                                    ]
                                }
                            ]
                        },
                        {
                            "token": "i",
                            "logprob": -4.291525328881107e-06,
                            "bytes": [
                                105
                            ],
                            "top_logprobs": [
                                {
                                    "token": "i",
                                    "logprob": -4.291525328881107e-06,
                                    "bytes": [
                                        105
                                    ]
                                },
                                {
                                    "token": "'",
                                    "logprob": -13.281253814697266,
                                    "bytes": [
                                        39
                                    ]
                                },
                                {
                                    "token": "ʻ",
                                    "logprob": -13.515628814697266,
                                    "bytes": [
                                        202,
                                        187
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " became",
                            "logprob": -0.025658821687102318,
                            "bytes": [
                                32,
                                98,
                                101,
                                99,
                                97,
                                109,
                                101
                            ],
                            "top_logprobs": [
                                {
                                    "token": " became",
                                    "logprob": -0.025658821687102318,
                                    "bytes": [
                                        32,
                                        98,
                                        101,
                                        99,
                                        97,
                                        109,
                                        101
                                    ]
                                },
                                {
                                    "token": " was",
                                    "logprob": -4.24440860748291,
                                    "bytes": [
                                        32,
                                        119,
                                        97,
                                        115
                                    ]
                                },
                                {
                                    "token": " officially",
                                    "logprob": -5.44753360748291,
                                    "bytes": [
                                        32,
                                        111,
                                        102,
                                        102,
                                        105,
                                        99,
                                        105,
                                        97,
                                        108,
                                        108,
                                        121
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " a",
                            "logprob": -0.010615901090204716,
                            "bytes": [
                                32,
                                97
                            ],
                            "top_logprobs": [
                                {
                                    "token": " a",
                                    "logprob": -0.010615901090204716,
                                    "bytes": [
                                        32,
                                        97
                                    ]
                                },
                                {
                                    "token": " the",
                                    "logprob": -4.713740825653076,
                                    "bytes": [
                                        32,
                                        116,
                                        104,
                                        101
                                    ]
                                },
                                {
                                    "token": " an",
                                    "logprob": -6.541865825653076,
                                    "bytes": [
                                        32,
                                        97,
                                        110
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " state",
                            "logprob": -0.05423527956008911,
                            "bytes": [
                                32,
                                115,
                                116,
                                97,
                                116,
                                101
                            ],
                            "top_logprobs": [
                                {
                                    "token": " state",
                                    "logprob": -0.05423527956008911,
                                    "bytes": [
                                        32,
                                        115,
                                        116,
                                        97,
                                        116,
                                        101
                                    ]
                                },
                                {
                                    "token": " U",
                                    "logprob": -3.1636102199554443,
                                    "bytes": [
                                        32,
                                        85
                                    ]
                                },
                                {
                                    "token": " United",
                                    "logprob": -5.413610458374023,
                                    "bytes": [
                                        32,
                                        85,
                                        110,
                                        105,
                                        116,
                                        101,
                                        100
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " on",
                            "logprob": -0.1038203239440918,
                            "bytes": [
                                32,
                                111,
                                110
                            ],
                            "top_logprobs": [
                                {
                                    "token": " on",
                                    "logprob": -0.1038203239440918,
                                    "bytes": [
                                        32,
                                        111,
                                        110
                                    ]
                                },
                                {
                                    "token": " in",
                                    "logprob": -2.494445323944092,
                                    "bytes": [
                                        32,
                                        105,
                                        110
                                    ]
                                },
                                {
                                    "token": " of",
                                    "logprob": -4.416320323944092,
                                    "bytes": [
                                        32,
                                        111,
                                        102
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " August",
                            "logprob": -0.09941038489341736,
                            "bytes": [
                                32,
                                65,
                                117,
                                103,
                                117,
                                115,
                                116
                            ],
                            "top_logprobs": [
                                {
                                    "token": " August",
                                    "logprob": -0.09941038489341736,
                                    "bytes": [
                                        32,
                                        65,
                                        117,
                                        103,
                                        117,
                                        115,
                                        116
                                    ]
                                },
                                {
                                    "token": " July",
                                    "logprob": -3.42753529548645,
                                    "bytes": [
                                        32,
                                        74,
                                        117,
                                        108,
                                        121
                                    ]
                                },
                                {
                                    "token": " January",
                                    "logprob": -3.77128529548645,
                                    "bytes": [
                                        32,
                                        74,
                                        97,
                                        110,
                                        117,
                                        97,
                                        114,
                                        121
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " ",
                            "logprob": -1.4305104514278355e-06,
                            "bytes": [
                                32
                            ],
                            "top_logprobs": [
                                {
                                    "token": " ",
                                    "logprob": -1.4305104514278355e-06,
                                    "bytes": [
                                        32
                                    ]
                                },
                                {
                                    "token": ",",
                                    "logprob": -13.617188453674316,
                                    "bytes": [
                                        44
                                    ]
                                },
                                {
                                    "token": " first",
                                    "logprob": -16.617189407348633,
                                    "bytes": [
                                        32,
                                        102,
                                        105,
                                        114,
                                        115,
                                        116
                                    ]
                                }
                            ]
                        },
                        {
                            "token": "2",
                            "logprob": -0.000620768463704735,
                            "bytes": [
                                50
                            ],
                            "top_logprobs": [
                                {
                                    "token": "2",
                                    "logprob": -0.000620768463704735,
                                    "bytes": [
                                        50
                                    ]
                                },
                                {
                                    "token": "1",
                                    "logprob": -7.3912458419799805,
                                    "bytes": [
                                        49
                                    ]
                                },
                                {
                                    "token": "3",
                                    "logprob": -12.96937084197998,
                                    "bytes": [
                                        51
                                    ]
                                }
                            ]
                        },
                        {
                            "token": "1",
                            "logprob": -5.364403477869928e-06,
                            "bytes": [
                                49
                            ],
                            "top_logprobs": [
                                {
                                    "token": "1",
                                    "logprob": -5.364403477869928e-06,
                                    "bytes": [
                                        49
                                    ]
                                },
                                {
                                    "token": ",",
                                    "logprob": -12.953130722045898,
                                    "bytes": [
                                        44
                                    ]
                                },
                                {
                                    "token": "9",
                                    "logprob": -13.359380722045898,
                                    "bytes": [
                                        57
                                    ]
                                }
                            ]
                        },
                        {
                            "token": ",",
                            "logprob": -0.0023636280093342066,
                            "bytes": [
                                44
                            ],
                            "top_logprobs": [
                                {
                                    "token": ",",
                                    "logprob": -0.0023636280093342066,
                                    "bytes": [
                                        44
                                    ]
                                },
                                {
                                    "token": "st",
                                    "logprob": -6.049238681793213,
                                    "bytes": [
                                        115,
                                        116
                                    ]
                                },
                                {
                                    "token": " (",
                                    "logprob": -14.658613204956055,
                                    "bytes": [
                                        32,
                                        40
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " ",
                            "logprob": -3.3378546504536644e-06,
                            "bytes": [
                                32
                            ],
                            "top_logprobs": [
                                {
                                    "token": " ",
                                    "logprob": -3.3378546504536644e-06,
                                    "bytes": [
                                        32
                                    ]
                                },
                                {
                                    "token": "1",
                                    "logprob": -12.68750286102295,
                                    "bytes": [
                                        49
                                    ]
                                },
                                {
                                    "token": "  ",
                                    "logprob": -16.703128814697266,
                                    "bytes": [
                                        32,
                                        32
                                    ]
                                }
                            ]
                        },
                        {
                            "token": "1",
                            "logprob": -5.960462772236497e-07,
                            "bytes": [
                                49
                            ],
                            "top_logprobs": [
                                {
                                    "token": "1",
                                    "logprob": -5.960462772236497e-07,
                                    "bytes": [
                                        49
                                    ]
                                },
                                {
                                    "token": "5",
                                    "logprob": -14.500000953674316,
                                    "bytes": [
                                        53
                                    ]
                                },
                                {
                                    "token": "2",
                                    "logprob": -16.015625,
                                    "bytes": [
                                        50
                                    ]
                                }
                            ]
                        },
                        {
                            "token": "9",
                            "logprob": -0.0011200590524822474,
                            "bytes": [
                                57
                            ],
                            "top_logprobs": [
                                {
                                    "token": "9",
                                    "logprob": -0.0011200590524822474,
                                    "bytes": [
                                        57
                                    ]
                                },
                                {
                                    "token": "8",
                                    "logprob": -6.797995090484619,
                                    "bytes": [
                                        56
                                    ]
                                },
                                {
                                    "token": "1",
                                    "logprob": -13.204244613647461,
                                    "bytes": [
                                        49
                                    ]
                                }
                            ]
                        },
                        {
                            "token": "5",
                            "logprob": -3.4689302992774174e-05,
                            "bytes": [
                                53
                            ],
                            "top_logprobs": [
                                {
                                    "token": "5",
                                    "logprob": -3.4689302992774174e-05,
                                    "bytes": [
                                        53
                                    ]
                                },
                                {
                                    "token": "1",
                                    "logprob": -10.79690933227539,
                                    "bytes": [
                                        49
                                    ]
                                },
                                {
                                    "token": "9",
                                    "logprob": -11.50784683227539,
                                    "bytes": [
                                        57
                                    ]
                                }
                            ]
                        },
                        {
                            "token": "9",
                            "logprob": -1.4543427823809907e-05,
                            "bytes": [
                                57
                            ],
                            "top_logprobs": [
                                {
                                    "token": "9",
                                    "logprob": -1.4543427823809907e-05,
                                    "bytes": [
                                        57
                                    ]
                                },
                                {
                                    "token": "8",
                                    "logprob": -11.156264305114746,
                                    "bytes": [
                                        56
                                    ]
                                },
                                {
                                    "token": "0",
                                    "logprob": -15.484389305114746,
                                    "bytes": [
                                        48
                                    ]
                                }
                            ]
                        },
                        {
                            "token": ",",
                            "logprob": -0.3206804096698761,
                            "bytes": [
                                44
                            ],
                            "top_logprobs": [
                                {
                                    "token": ",",
                                    "logprob": -0.3206804096698761,
                                    "bytes": [
                                        44
                                    ]
                                },
                                {
                                    "token": ".",
                                    "logprob": -1.3050553798675537,
                                    "bytes": [
                                        46
                                    ]
                                },
                                {
                                    "token": " (",
                                    "logprob": -6.805055618286133,
                                    "bytes": [
                                        32,
                                        40
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " after",
                            "logprob": -1.4256243705749512,
                            "bytes": [
                                32,
                                97,
                                102,
                                116,
                                101,
                                114
                            ],
                            "top_logprobs": [
                                {
                                    "token": " after",
                                    "logprob": -1.4256243705749512,
                                    "bytes": [
                                        32,
                                        97,
                                        102,
                                        116,
                                        101,
                                        114
                                    ]
                                },
                                {
                                    "token": " when",
                                    "logprob": -1.4724993705749512,
                                    "bytes": [
                                        32,
                                        119,
                                        104,
                                        101,
                                        110
                                    ]
                                },
                                {
                                    "token": " following",
                                    "logprob": -1.5662493705749512,
                                    "bytes": [
                                        32,
                                        102,
                                        111,
                                        108,
                                        108,
                                        111,
                                        119,
                                        105,
                                        110,
                                        103
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " it",
                            "logprob": -1.6329573392868042,
                            "bytes": [
                                32,
                                105,
                                116
                            ],
                            "top_logprobs": [
                                {
                                    "token": " it",
                                    "logprob": -1.6329573392868042,
                                    "bytes": [
                                        32,
                                        105,
                                        116
                                    ]
                                },
                                {
                                    "token": " the",
                                    "logprob": -0.8126448392868042,
                                    "bytes": [
                                        32,
                                        116,
                                        104,
                                        101
                                    ]
                                },
                                {
                                    "token": " a",
                                    "logprob": -2.3829574584960938,
                                    "bytes": [
                                        32,
                                        97
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " was",
                            "logprob": -0.36921998858451843,
                            "bytes": [
                                32,
                                119,
                                97,
                                115
                            ],
                            "top_logprobs": [
                                {
                                    "token": " was",
                                    "logprob": -0.36921998858451843,
                                    "bytes": [
                                        32,
                                        119,
                                        97,
                                        115
                                    ]
                                },
                                {
                                    "token": " gained",
                                    "logprob": -1.9473450183868408,
                                    "bytes": [
                                        32,
                                        103,
                                        97,
                                        105,
                                        110,
                                        101,
                                        100
                                    ]
                                },
                                {
                                    "token": " had",
                                    "logprob": -3.228595018386841,
                                    "bytes": [
                                        32,
                                        104,
                                        97,
                                        100
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " admitted",
                            "logprob": -0.04310690239071846,
                            "bytes": [
                                32,
                                97,
                                100,
                                109,
                                105,
                                116,
                                116,
                                101,
                                100
                            ],
                            "top_logprobs": [
                                {
                                    "token": " admitted",
                                    "logprob": -0.04310690239071846,
                                    "bytes": [
                                        32,
                                        97,
                                        100,
                                        109,
                                        105,
                                        116,
                                        116,
                                        101,
                                        100
                                    ]
                                },
                                {
                                    "token": " approved",
                                    "logprob": -4.160294532775879,
                                    "bytes": [
                                        32,
                                        97,
                                        112,
                                        112,
                                        114,
                                        111,
                                        118,
                                        101,
                                        100
                                    ]
                                },
                                {
                                    "token": " officially",
                                    "logprob": -4.535294532775879,
                                    "bytes": [
                                        32,
                                        111,
                                        102,
                                        102,
                                        105,
                                        99,
                                        105,
                                        97,
                                        108,
                                        108,
                                        121
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " to",
                            "logprob": -0.3276839256286621,
                            "bytes": [
                                32,
                                116,
                                111
                            ],
                            "top_logprobs": [
                                {
                                    "token": " to",
                                    "logprob": -0.3276839256286621,
                                    "bytes": [
                                        32,
                                        116,
                                        111
                                    ]
                                },
                                {
                                    "token": " into",
                                    "logprob": -1.593308925628662,
                                    "bytes": [
                                        32,
                                        105,
                                        110,
                                        116,
                                        111
                                    ]
                                },
                                {
                                    "token": " as",
                                    "logprob": -2.593308925628662,
                                    "bytes": [
                                        32,
                                        97,
                                        115
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " the",
                            "logprob": -0.0007993363542482257,
                            "bytes": [
                                32,
                                116,
                                104,
                                101
                            ],
                            "top_logprobs": [
                                {
                                    "token": " the",
                                    "logprob": -0.0007993363542482257,
                                    "bytes": [
                                        32,
                                        116,
                                        104,
                                        101
                                    ]
                                },
                                {
                                    "token": " membership",
                                    "logprob": -7.407049179077148,
                                    "bytes": [
                                        32,
                                        109,
                                        101,
                                        109,
                                        98,
                                        101,
                                        114,
                                        115,
                                        104,
                                        105,
                                        112
                                    ]
                                },
                                {
                                    "token": " union",
                                    "logprob": -9.821111679077148,
                                    "bytes": [
                                        32,
                                        117,
                                        110,
                                        105,
                                        111,
                                        110
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " Union",
                            "logprob": -0.5531853437423706,
                            "bytes": [
                                32,
                                85,
                                110,
                                105,
                                111,
                                110
                            ],
                            "top_logprobs": [
                                {
                                    "token": " Union",
                                    "logprob": -0.5531853437423706,
                                    "bytes": [
                                        32,
                                        85,
                                        110,
                                        105,
                                        111,
                                        110
                                    ]
                                },
                                {
                                    "token": " United",
                                    "logprob": -1.1156853437423706,
                                    "bytes": [
                                        32,
                                        85,
                                        110,
                                        105,
                                        116,
                                        101,
                                        100
                                    ]
                                },
                                {
                                    "token": " union",
                                    "logprob": -2.35006046295166,
                                    "bytes": [
                                        32,
                                        117,
                                        110,
                                        105,
                                        111,
                                        110
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " as",
                            "logprob": -0.1123080775141716,
                            "bytes": [
                                32,
                                97,
                                115
                            ],
                            "top_logprobs": [
                                {
                                    "token": " as",
                                    "logprob": -0.1123080775141716,
                                    "bytes": [
                                        32,
                                        97,
                                        115
                                    ]
                                },
                                {
                                    "token": " on",
                                    "logprob": -2.9716830253601074,
                                    "bytes": [
                                        32,
                                        111,
                                        110
                                    ]
                                },
                                {
                                    "token": " under",
                                    "logprob": -3.6591830253601074,
                                    "bytes": [
                                        32,
                                        117,
                                        110,
                                        100,
                                        101,
                                        114
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " the",
                            "logprob": -0.022054528817534447,
                            "bytes": [
                                32,
                                116,
                                104,
                                101
                            ],
                            "top_logprobs": [
                                {
                                    "token": " the",
                                    "logprob": -0.022054528817534447,
                                    "bytes": [
                                        32,
                                        116,
                                        104,
                                        101
                                    ]
                                },
                                {
                                    "token": " a",
                                    "logprob": -4.193929672241211,
                                    "bytes": [
                                        32,
                                        97
                                    ]
                                },
                                {
                                    "token": " one",
                                    "logprob": -5.131429672241211,
                                    "bytes": [
                                        32,
                                        111,
                                        110,
                                        101
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " ",
                            "logprob": -0.012293754145503044,
                            "bytes": [
                                32
                            ],
                            "top_logprobs": [
                                {
                                    "token": " ",
                                    "logprob": -0.012293754145503044,
                                    "bytes": [
                                        32
                                    ]
                                },
                                {
                                    "token": " fifty",
                                    "logprob": -4.527918815612793,
                                    "bytes": [
                                        32,
                                        102,
                                        105,
                                        102,
                                        116,
                                        121
                                    ]
                                },
                                {
                                    "token": " forty",
                                    "logprob": -6.731043815612793,
                                    "bytes": [
                                        32,
                                        102,
                                        111,
                                        114,
                                        116,
                                        121
                                    ]
                                }
                            ]
                        },
                        {
                            "token": "5",
                            "logprob": -6.496695277746767e-05,
                            "bytes": [
                                53
                            ],
                            "top_logprobs": [
                                {
                                    "token": "5",
                                    "logprob": -6.496695277746767e-05,
                                    "bytes": [
                                        53
                                    ]
                                },
                                {
                                    "token": "4",
                                    "logprob": -9.656314849853516,
                                    "bytes": [
                                        52
                                    ]
                                },
                                {
                                    "token": "",
                                    "logprob": -14.437564849853516,
                                    "bytes": []
                                }
                            ]
                        },
                        {
                            "token": "0",
                            "logprob": 0.0,
                            "bytes": [
                                48
                            ],
                            "top_logprobs": [
                                {
                                    "token": "0",
                                    "logprob": 0.0,
                                    "bytes": [
                                        48
                                    ]
                                },
                                {
                                    "token": "4",
                                    "logprob": -16.953125,
                                    "bytes": [
                                        52
                                    ]
                                },
                                {
                                    "token": "1",
                                    "logprob": -18.375,
                                    "bytes": [
                                        49
                                    ]
                                }
                            ]
                        },
                        {
                            "token": "th",
                            "logprob": -2.50339189733495e-06,
                            "bytes": [
                                116,
                                104
                            ],
                            "top_logprobs": [
                                {
                                    "token": "th",
                                    "logprob": -2.50339189733495e-06,
                                    "bytes": [
                                        116,
                                        104
                                    ]
                                },
                                {
                                    "token": " states",
                                    "logprob": -13.65625286102295,
                                    "bytes": [
                                        32,
                                        115,
                                        116,
                                        97,
                                        116,
                                        101,
                                        115
                                    ]
                                },
                                {
                                    "token": " th",
                                    "logprob": -14.17187786102295,
                                    "bytes": [
                                        32,
                                        116,
                                        104
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " state",
                            "logprob": -0.0029809109400957823,
                            "bytes": [
                                32,
                                115,
                                116,
                                97,
                                116,
                                101
                            ],
                            "top_logprobs": [
                                {
                                    "token": " state",
                                    "logprob": -0.0029809109400957823,
                                    "bytes": [
                                        32,
                                        115,
                                        116,
                                        97,
                                        116,
                                        101
                                    ]
                                },
                                {
                                    "token": " U",
                                    "logprob": -6.549855709075928,
                                    "bytes": [
                                        32,
                                        85
                                    ]
                                },
                                {
                                    "token": " State",
                                    "logprob": -6.815480709075928,
                                    "bytes": [
                                        32,
                                        83,
                                        116,
                                        97,
                                        116,
                                        101
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " of",
                            "logprob": -1.254439115524292,
                            "bytes": [
                                32,
                                111,
                                102
                            ],
                            "top_logprobs": [
                                {
                                    "token": " of",
                                    "logprob": -1.254439115524292,
                                    "bytes": [
                                        32,
                                        111,
                                        102
                                    ]
                                },
                                {
                                    "token": ".",
                                    "logprob": -0.676314115524292,
                                    "bytes": [
                                        46
                                    ]
                                },
                                {
                                    "token": " in",
                                    "logprob": -1.816939115524292,
                                    "bytes": [
                                        32,
                                        105,
                                        110
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " the",
                            "logprob": -1.5258672647178173e-05,
                            "bytes": [
                                32,
                                116,
                                104,
                                101
                            ],
                            "top_logprobs": [
                                {
                                    "token": " the",
                                    "logprob": -1.5258672647178173e-05,
                                    "bytes": [
                                        32,
                                        116,
                                        104,
                                        101
                                    ]
                                },
                                {
                                    "token": " America",
                                    "logprob": -11.468765258789062,
                                    "bytes": [
                                        32,
                                        65,
                                        109,
                                        101,
                                        114,
                                        105,
                                        99,
                                        97
                                    ]
                                },
                                {
                                    "token": " United",
                                    "logprob": -12.781265258789062,
                                    "bytes": [
                                        32,
                                        85,
                                        110,
                                        105,
                                        116,
                                        101,
                                        100
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " United",
                            "logprob": -0.00560237281024456,
                            "bytes": [
                                32,
                                85,
                                110,
                                105,
                                116,
                                101,
                                100
                            ],
                            "top_logprobs": [
                                {
                                    "token": " United",
                                    "logprob": -0.00560237281024456,
                                    "bytes": [
                                        32,
                                        85,
                                        110,
                                        105,
                                        116,
                                        101,
                                        100
                                    ]
                                },
                                {
                                    "token": " U",
                                    "logprob": -5.8493523597717285,
                                    "bytes": [
                                        32,
                                        85
                                    ]
                                },
                                {
                                    "token": " Union",
                                    "logprob": -6.4118523597717285,
                                    "bytes": [
                                        32,
                                        85,
                                        110,
                                        105,
                                        111,
                                        110
                                    ]
                                }
                            ]
                        },
                        {
                            "token": " States",
                            "logprob": -2.145764938177308e-06,
                            "bytes": [
                                32,
                                83,
                                116,
                                97,
                                116,
                                101,
                                115
                            ],
                            "top_logprobs": [
                                {
                                    "token": " States",
                                    "logprob": -2.145764938177308e-06,
                                    "bytes": [
                                        32,
                                        83,
                                        116,
                                        97,
                                        116,
                                        101,
                                        115
                                    ]
                                },
                                {
                                    "token": " State",
                                    "logprob": -13.390626907348633,
                                    "bytes": [
                                        32,
                                        83,
                                        116,
                                        97,
                                        116,
                                        101
                                    ]
                                },
                                {
                                    "token": " states",
                                    "logprob": -14.453126907348633,
                                    "bytes": [
                                        32,
                                        115,
                                        116,
                                        97,
                                        116,
                                        101,
                                        115
                                    ]
                                }
                            ]
                        },
                        {
                            "token": ".",
                            "logprob": -0.12239378690719604,
                            "bytes": [
                                46
                            ],
                            "top_logprobs": [
                                {
                                    "token": ".",
                                    "logprob": -0.12239378690719604,
                                    "bytes": [
                                        46
                                    ]
                                },
                                {
                                    "token": " of",
                                    "logprob": -2.372393846511841,
                                    "bytes": [
                                        32,
                                        111,
                                        102
                                    ]
                                },
                                {
                                    "token": " on",
                                    "logprob": -4.669268608093262,
                                    "bytes": [
                                        32,
                                        111,
                                        110
                                    ]
                                }
                            ]
                        },
                        {
                            "token": "",
                            "logprob": -0.53877192735672,
                            "bytes": [],
                            "top_logprobs": [
                                {
                                    "token": "",
                                    "logprob": -0.53877192735672,
                                    "bytes": []
                                },
                                {
                                    "token": "\n",
                                    "logprob": -1.8825218677520752,
                                    "bytes": [
                                        10
                                    ]
                                },
                                {
                                    "token": " The",
                                    "logprob": -2.179396867752075,
                                    "bytes": [
                                        32,
                                        84,
                                        104,
                                        101
                                    ]
                                }
                            ]
                        }
                    ]
                },
                "finish_reason": "stop",
                "stop_reason": null
            }
        ],
        "usage": {
            "prompt_tokens": 46,
            "total_tokens": 85,
            "completion_tokens": 39
        }
    }
""".trimIndent()

val defaultInferenceResponseObject = cosmosJson.fromJson(defaultInferenceResponse, OpenAIResponse::class.java)

