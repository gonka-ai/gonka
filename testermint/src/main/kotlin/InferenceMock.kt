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
        this.setInferenceResponse(com.productscience.gsonSnakeCase.toJson(openAIResponse))

}
