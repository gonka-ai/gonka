package com.productscience

import com.google.gson.Gson
import com.google.gson.GsonBuilder
import com.productscience.data.AppState
import com.productscience.data.DoubleSerializer
import com.productscience.data.DurationDeserializer
import com.productscience.data.EpochParams
import com.productscience.data.FloatSerializer
import com.productscience.data.GenesisOnlyParams
import com.productscience.data.InferenceNode
import com.productscience.data.InferenceParams
import com.productscience.data.InferencePayload
import com.productscience.data.InferenceState
import com.productscience.data.InstantDeserializer
import com.productscience.data.LongSerializer
import com.productscience.data.OpenAIResponse
import com.productscience.data.Participant
import com.productscience.data.PubKey
import com.productscience.data.Pubkey2
import com.productscience.data.Pubkey2Deserializer
import com.productscience.data.TxResponse
import com.productscience.data.UnfundedInferenceParticipant
import com.productscience.data.spec
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

fun getInferenceResult(highestFunded: LocalInferencePair): InferenceResult {
    val beforeInferenceParticipants = highestFunded.api.getParticipants()
    val inference = makeInferenceRequest(highestFunded, inferenceRequest)
    val afterInference = highestFunded.api.getParticipants()
    return createInferenceResult(inference, afterInference, beforeInferenceParticipants)
}

fun createInferenceResult(
    inference: InferencePayload,
    afterInference: List<Participant>,
    beforeInferenceParticipants: List<Participant>,
): InferenceResult {
    val requester = inference.requestedBy
    val executor = inference.executedBy
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

private fun makeInferenceRequest(highestFunded: LocalInferencePair, payload: String): InferencePayload {
    highestFunded.waitForFirstPoC()
    val response = highestFunded.makeInferenceRequest(payload)
    Logger.info("Inference response: ${response.choices.first().message.content}")
    val inferenceId = response.id

    val inference = generateSequence {
        highestFunded.node.waitForNextBlock()
        highestFunded.api.getInference(inferenceId)
    }.take(5).firstOrNull { it.status == 1 }
    check(inference != null) { "Inference never logged in chain" }
    return inference
}

fun initialize(pairs: List<LocalInferencePair>): LocalInferencePair {
    pairs.forEach {
        it.node.waitForMinimumBlock(10)
        it.api.setNodesTo(validNode.copy(host = "${it.name.trim('/')}-wiremock", pocPort = 8080, inferencePort = 8080))
        it.mock?.setInferenceResponse(defaultInferenceResponseObject)
        it.getParams()
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
        if (currentParticipants.none { it.id == pair.node.getAddress() }) {
            pair.addSelfAsParticipant(listOf("unsloth/llama-3-8b-Instruct"))
        }
    }
    addUnfundedDirectly(unfunded, currentParticipants, highestFunded)
//    fundUnfunded(unfunded, highestFunded)

    highestFunded.node.waitForNextBlock()
    return highestFunded
}

private fun fundUnfunded(
    unfunded: List<LocalInferencePair>,
    highestFunded: LocalInferencePair,
) {
    for (pair in unfunded) {
        highestFunded.node.transferMoneyTo(pair.node, defaultFunding).assertSuccess()
        highestFunded.node.waitForNextBlock()
    }
    val fundingHeight = highestFunded.node.getStatus().syncInfo.latestBlockHeight

    unfunded.forEach {
        it.node.waitForMinimumBlock(fundingHeight + 1L)
        it.addSelfAsParticipant(listOf("unsloth/llama-3-8b-Instruct"))
    }
}

private fun addUnfundedDirectly(
    unfunded: List<LocalInferencePair>,
    currentParticipants: List<Participant>,
    highestFunded: LocalInferencePair,
) {
    for (pair in unfunded) {
        if (currentParticipants.none { it.id == pair.node.getAddress() }) {
            val selfKey = pair.node.getKeys()[0]
            val status = pair.node.getStatus()
            val validatorInfo = status.validatorInfo
            val valPubKey: PubKey = validatorInfo.pubKey
            Logger.debug("PubKey extracted pubkey={}", selfKey.pubkey)
            highestFunded.api.addUnfundedInferenceParticipant(
                UnfundedInferenceParticipant(
                    url = "http://${pair.name}-api:8080",
                    models = listOf("unsloth/llama-3-8b-Instruct"),
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
    .registerTypeAdapter(Pubkey2::class.java, Pubkey2Deserializer())
    .registerTypeAdapter(java.lang.Long::class.java, LongSerializer())
    .registerTypeAdapter(java.lang.Double::class.java, DoubleSerializer())
    .registerTypeAdapter(java.lang.Float::class.java, FloatSerializer())
    .create()

val openAiJson: Gson = GsonBuilder()
    .setFieldNamingPolicy(com.google.gson.FieldNamingPolicy.LOWER_CASE_WITH_UNDERSCORES)
    .registerTypeAdapter(Instant::class.java, InstantDeserializer())
    .registerTypeAdapter(Duration::class.java, DurationDeserializer())
    .create()

val gsonCamelCase = GsonBuilder()
    .setFieldNamingPolicy(com.google.gson.FieldNamingPolicy.IDENTITY)
    .registerTypeAdapter(Instant::class.java, InstantDeserializer())
    .registerTypeAdapter(Duration::class.java, DurationDeserializer())
    .create()

val inferenceConfig = ApplicationConfig(
    appName = "inferenced",
    chainId = "prod-sim",
    nodeImageName = "gcr.io/decentralized-ai/inferenced",
    genesisNodeImage = "gcr.io/decentralized-ai/inferenced",
    wireMockImageName = "wiremock/wiremock:latest",
    apiImageName = "gcr.io/decentralized-ai/api",
    denom = "nicoin",
    stateDirName = ".inference",
    // TODO: probably need to add more to the spec here, so if tests change them we change back
    genesisSpec = spec {
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::params] = spec<InferenceParams> {
                this[InferenceParams::epochParams] = spec<EpochParams> {
                    this[EpochParams::epochLength] = 20L
                }
            }
            this[InferenceState::genesisOnlyParams] = spec<GenesisOnlyParams> {
                this[GenesisOnlyParams::topRewardPeriod] = Duration.ofDays(365).toSeconds()
            }
        }
    }
)

val inferenceRequest = """
    {
      "model" : "unsloth/llama-3-8b-Instruct",
      "temperature" : 0.8,
      "messages": [{
          "role": "system",
          "content": "Regardless of the language of the question, answer in english"
        },
        {
            "role": "user",
            "content": "When did Hawaii become a state"
        }
      ],
      "seed" : -25
    }
""".trimIndent()

val inferenceRequestStream = """
    {
      "model" : "unsloth/llama-3-8b-Instruct",
      "temperature" : 0.8,
      "messages": [{
          "role": "system",
          "content": "Regardless of the language of the question, answer in english"
        },
        {
            "role": "user",
            "content": "When did Hawaii become a state"
        }
      ],
      "seed" : -25,
      "stream" : true
    }
""".trimIndent()

const val defaultModel = "unsloth/llama-3-8b-Instruct"

val validNode = InferenceNode(
    host = "36.189.234.237:19009/",
    pocPort = 19009,
    inferencePort = 19009,
    models = listOf(defaultModel),
    id = "wiremock2",
    maxConcurrent = 10
)

val defaultInferenceResponse = """
    {
        "id": "cmpl-1c7b769de9b0494694eeee854da0a014",
        "object": "chat.completion",
        "created": 1736220740,
        "model": "unsloth/llama-3-8b-Instruct",
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
