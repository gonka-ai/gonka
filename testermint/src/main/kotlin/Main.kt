package com.productscience

import com.google.gson.GsonBuilder
import com.productscience.data.InferencePayload
import com.productscience.data.InstantDeserializer
import com.productscience.data.Participant
import com.productscience.data.PubKey
import com.productscience.data.Pubkey2
import com.productscience.data.Pubkey2Deserializer
import com.productscience.data.TxResponse
import com.productscience.data.UnfundedInferenceParticipant
import org.tinylog.kotlin.Logger
import java.time.Instant

fun main() {
    val pairs = getLocalInferencePairs(inferenceConfig)
    val highestFunded = initialize(pairs)
    val inference = generateSequence {
        getInferenceResult(highestFunded)
    }.first { it.inference.executedBy != it.inference.receivedBy }

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
    val requester = inference.receivedBy
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
    highestFunded.node.waitForMinimumBlock((EpochLength + setNewValidatorsStage + 1))

    val response = highestFunded.makeInferenceRequest(payload)

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
        it.node.waitForMinimumBlock(1)
    }
    val balances = pairs.zip(pairs.map { it.node.getSelfBalance("icoin") })

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
            Logger.debug("PubKey value: ${selfKey.pubkey}")
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
val gsonSnakeCase = GsonBuilder()
    .setFieldNamingPolicy(com.google.gson.FieldNamingPolicy.LOWER_CASE_WITH_UNDERSCORES)
    .registerTypeAdapter(Instant::class.java, InstantDeserializer())
    .registerTypeAdapter(Pubkey2::class.java, Pubkey2Deserializer())
    .create()

val gsonCamelCase = GsonBuilder()
    .setFieldNamingPolicy(com.google.gson.FieldNamingPolicy.IDENTITY)
    .registerTypeAdapter(Instant::class.java, InstantDeserializer())
    .create()

val inferenceConfig = ApplicationConfig(
    appName = "inferenced",
    chainId = "prod-sim",
    nodeImageName = "inferenced",
    apiImageName = "decentralized-api",
    denom = "icoin",
    stateDirName = ".inference",
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
