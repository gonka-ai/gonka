package com.productscience

import com.google.gson.GsonBuilder
import com.productscience.data.InstantDeserializer
import com.productscience.data.TxResponse
import java.time.Instant

fun main() {
    val pairs = getLocalInferencePairs(inferenceConfig)
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
        return
    }
//    println("Funded nodes: ${funded.size}")
//    funded.forEach {
//        println("funded: ${it.name} - ${it.node.getKeys()[0].address}")
//    }
//    unfunded.forEach {
//        println("unfunded:${it.name} - ${it.node.getKeys()[0].address}")
//    }
//    for (pair in funded) {
//        pair.addSelfAsParticipant(listOf("unsloth/llama-3-8b-Instruct"))
//    }
//    for (pair in unfunded) {
//        highestFunded.node.transferMoneyTo(pair.node, defaultFunding).assertSuccess()
//        highestFunded.node.waitForNextBlock()
//    }
//    val fundingHeight = highestFunded.node.getStatus().syncInfo.latestBlockHeight
//
//    unfunded.forEach {
//        it.node.waitForMinimumBlock(fundingHeight + 1L)
//        it.addSelfAsParticipant(listOf("unsloth/llama-3-8b-Instruct"))
//    }

    val participants = highestFunded.api.getParticipants()
    val response = highestFunded.makeInferenceRequest(inferenceRequest)

    val inferenceId = response.id

    highestFunded.node.waitForNextBlock()
    val inference = highestFunded.api.getInference(inferenceId)
    println(inference)


}

private fun TxResponse.assertSuccess() {
    if (code != 0) {
        throw IllegalStateException("Transaction failed: $rawLog")
    }
}

val defaultFunding = 20_000_000L
val gson = GsonBuilder()
    .setFieldNamingPolicy(com.google.gson.FieldNamingPolicy.LOWER_CASE_WITH_UNDERSCORES)
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
