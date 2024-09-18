package com.productscience

import com.github.kittinunf.fuel.Fuel
import com.github.kittinunf.fuel.core.FuelError
import com.github.kittinunf.fuel.core.Request
import com.github.kittinunf.fuel.core.Response
import com.github.kittinunf.fuel.core.extensions.jsonBody
import com.github.kittinunf.fuel.gson.gsonDeserializer
import com.github.kittinunf.fuel.gson.jsonBody
import com.github.kittinunf.result.Result
import com.productscience.data.InferenceParticipant
import com.productscience.data.InferencePayload
import com.productscience.data.OpenAIResponse
import com.productscience.data.Participant
import com.productscience.data.ParticipantsResponse
import org.tinylog.kotlin.Logger

data class ApplicationAPI(val url: String, override val config: ApplicationConfig) : HasConfig {
    fun getParticipants(): List<Participant> = wrapLog("GetParticipants", false) {
        val resp = Fuel.get("$url/v1/participants")
            .responseObject<ParticipantsResponse>(gsonDeserializer(gsonSnakeCase))
        logResponse(resp)
        resp.third.get().participants
    }

    fun addInferenceParticipant(inferenceParticipant: InferenceParticipant) = wrapLog("AddInferenceParticipant", true) {
        val response = Fuel.post("$url/v1/participants")
            .jsonBody(inferenceParticipant, gsonSnakeCase)
            .response()
        logResponse(response)
    }
    
    fun getInference(inferenceId: String):InferencePayload = wrapLog("getInference", true){
        val response = Fuel.get(url + "/v1/chat/completions/$inferenceId")
            .responseObject<InferencePayload>(gsonDeserializer(gsonCamelCase))
        logResponse(response)
        response.third.get()
    }

    fun makeInferenceRequest(
        request: String,
        address: String,
        signature: String,
    ): OpenAIResponse =
        wrapLog("MakeInferenceRequest", true) {
            val response = Fuel.post((url + "/v1/chat/completions"))
                .jsonBody(request)
                .header("X-Requester-Address", address)
                .header("Authorization", signature)
                .responseObject<OpenAIResponse>(gsonDeserializer(gsonSnakeCase))
            logResponse(response)
            response.third.get()
        }
}


fun logResponse(reqData: Triple<Request, Response, Result<*, FuelError>>) {
    val (request, response, result) = reqData
    Logger.debug("Request: ${request.method} ${request.url}")
    Logger.trace("Request headers: ${request.headers}")
    Logger.trace("Request data: ${request.body.asString("application/json")}")
    Logger.debug("Response: ${response.statusCode} ${response.responseMessage}")
    Logger.trace("Response headers: ${response.headers}")
    if (result is Result.Failure) {
        Logger.error(result.getException(), "Error making request to ${request.url}")
        Logger.error("Response Data: ${response.data.decodeToString()}")
        return
    }

    Logger.trace("Response Data: ${result.get()}")
}
