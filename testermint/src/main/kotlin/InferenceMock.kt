package com.productscience

import com.github.tomakehurst.wiremock.client.MappingBuilder
import com.github.tomakehurst.wiremock.client.WireMock
import com.github.tomakehurst.wiremock.client.WireMock.aResponse
import com.github.tomakehurst.wiremock.client.WireMock.post
import com.github.tomakehurst.wiremock.client.WireMock.urlEqualTo
import com.productscience.data.OpenAIResponse

class InferenceMock(port: Int, val name: String) {
    private val mockClient = WireMock(port)
    fun givenThat(builder: MappingBuilder) =
        mockClient.register(builder)

    fun setInferenceResponse(response: String) =
        this.givenThat(
            post(urlEqualTo("/v1/chat/completions"))
                .willReturn(aResponse().withStatus(200).withBody(response))
        )

    fun setInferenceResponse(openAIResponse: OpenAIResponse) =
        this.setInferenceResponse(
            gsonSnakeCase.toJson(openAIResponse))

    fun setPocResponse(weight: Long) {
        val nonces = (1..weight).toList()
        val dist = nonces.map { it.toDouble() / weight }
        val body = """
            {
              "public_key": "{{jsonPath originalRequest.body '$.public_key'}}",
              "block_hash": "{{jsonPath originalRequest.body '$.block_hash'}}",
              "block_height": {{jsonPath originalRequest.body '$.block_height'}},
              "nonces": $nonces,
              "dist": $dist
            }
        """.trimIndent()
        this.givenThat(
            post(urlEqualTo("/api/v1/pow/init/generate"))
                .willReturn(
                    aResponse()
                        .withStatus(200)
                        .withHeader("Content-Type", "application/json")
                        .withBody("")
                )
                .withPostServeAction(
                    "webhook",
                    mapOf(
                        "method" to "POST",
                        "url" to "{{jsonPath originalRequest.body '$.url'}}/generated",
                        "headers" to mapOf("Content-Type" to "application/json"),
                        "delay" to mapOf("type" to "fixed", "milliseconds" to 1000),
                        "body" to body
                    )
                )
        )


    }
}
