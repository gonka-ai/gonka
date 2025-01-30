package com.productscience

import com.github.kittinunf.fuel.Fuel
import com.github.kittinunf.fuel.core.FuelError
import com.github.kittinunf.fuel.core.Request
import com.github.kittinunf.fuel.core.Response
import com.github.kittinunf.fuel.core.extensions.jsonBody
import com.github.kittinunf.fuel.gson.gsonDeserializer
import com.github.kittinunf.fuel.gson.jsonBody
import com.github.kittinunf.fuel.gson.responseObject
import com.github.kittinunf.result.Result
import com.productscience.data.*
import org.tinylog.kotlin.Logger
import java.io.BufferedReader
import java.io.BufferedWriter
import java.io.InputStreamReader
import java.io.OutputStreamWriter
import java.net.HttpURLConnection
import java.net.URL

data class ApplicationAPI(val url: String, override val config: ApplicationConfig) : HasConfig {
    fun getParticipants(): List<Participant> = wrapLog("GetParticipants", false) {
        val resp = Fuel.get("$url/v1/participants")
            .timeoutRead(1000*60)
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

    fun addUnfundedInferenceParticipant(inferenceParticipant: UnfundedInferenceParticipant) =
        wrapLog("AddUnfundedInferenceParticipant", true) {
            val response = Fuel.post("$url/v1/participants")
                .jsonBody(inferenceParticipant, gsonSnakeCase)
                .response()
            logResponse(response)
        }

    fun getInference(inferenceId: String): InferencePayload = wrapLog("getInference", true) {
        val response = Fuel.get(url + "/v1/chat/completions/$inferenceId")
            .responseObject<InferencePayload>(gsonDeserializer(gsonSnakeCase))
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
                .timeout(1000*60)
                .timeoutRead(1000*60)
                .responseObject<OpenAIResponse>(gsonDeserializer(gsonSnakeCase))
            logResponse(response)
            response.third.get()
        }

    fun makeStreamedInferenceRequest(
        request: String,
        address: String,
        signature: String,
    ): List<String> =
        wrapLog("MakeStreamedInferenceRequest", true) {
            stream(url = "$url/v1/chat/completions", address = address, signature = signature, jsonBody = request)
        }

    fun runValidation(inferenceId: String): Unit = wrapLog("RunValidation", true) {
        val response = Fuel.get("$url/v1/validation")
            .jsonBody("{\"inference_id\": \"$inferenceId\"}")
            .response()
        logResponse(response)
    }

    fun setNodesTo(node: InferenceNode) {
        val nodes = getNodes()
        if (nodes.all { it.node.id == node.id }) return
        nodes.forEach { removeNode(it.node.id) }
        addNode(node)
    }

    fun getNodes(): List<NodeResponse> =
        wrapLog("GetNodes", false) {
            val response = Fuel.get("$url/v1/nodes")
                .responseObject<List<NodeResponse>>(gsonDeserializer(gsonSnakeCase))
            logResponse(response)
            response.third.get()
        }

    fun addNode(node: InferenceNode): InferenceNode = wrapLog("AddNode", true) {
        val response = Fuel.post("$url/v1/nodes")
            .jsonBody(node, gsonSnakeCase)
            .responseObject<InferenceNode>(gsonDeserializer(gsonSnakeCase))
        logResponse(response)
        response.third.get()
    }

    fun addNodes(nodes: List<InferenceNode>): List<InferenceNode> = wrapLog("AddNodes", true) {
        val response = Fuel.post("$url/v1/nodes/batch")
            .jsonBody(nodes, gsonSnakeCase)
            .responseObject<List<InferenceNode>>(gsonDeserializer(gsonSnakeCase))
        logResponse(response)
        response.third.get()
    }

    fun removeNode(nodeId: String) = wrapLog("RemoveNode", true) {
        val response = Fuel.delete("$url/v1/nodes/$nodeId")
            .responseString()
        logResponse(response)
    }

    fun submitPriceProposal(proposal: UnitOfComputePriceProposalDto): String = wrapLog("SubmitPriceProposal", true) {
        val response = Fuel.post("$url/v1/admin/unit-of-compute-price-proposal")
            .jsonBody(proposal, gsonSnakeCase)
            .responseString()
        logResponse(response)

        response.third.get()
    }

    fun getPriceProposal(): GetUnitOfComputePriceProposalDto = wrapLog("SubmitPriceProposal", true) {
        get<GetUnitOfComputePriceProposalDto>("v1/admin/unit-of-compute-price-proposal")
    }

    fun getPricing(): GetPricingDto = wrapLog("GetPricing", true) {
        get<GetPricingDto>("v1/pricing")
    }

    fun registerModel(model: RegisterModelDto): String = wrapLog("RegisterModel", true) {
        postWithStringResponse("v1/admin/models", model)
    }

    inline fun <reified Out: Any> get(path: String): Out {
        val response = Fuel.get("$url/$path")
            .responseObject<Out>(gsonDeserializer(gsonSnakeCase))
        logResponse(response)

        return response.third.get()
    }

    inline fun <reified In: Any, reified Out: Any> post(path: String, body: In): Out {
        val response = Fuel.post("$url/$path")
            .jsonBody(body, gsonSnakeCase)
            .responseObject<Out>()
        logResponse(response)

        return response.third.get()
    }

    inline fun <reified In: Any> postWithStringResponse(path: String, body: In): String {
        val response = Fuel.post("$url/$path")
            .jsonBody(body, gsonSnakeCase)
            .responseString()
        logResponse(response)

        return response.third.get()
    }
}


fun logResponse(reqData: Triple<Request, Response, Result<*, FuelError>>) {
    val (request, response, result) = reqData
    Logger.debug("Request: {} {}", request.method, request.url)
    Logger.trace("Request headers: {}", request.headers)
    Logger.trace("Request data: {}", request.body.asString("application/json"))
    Logger.debug("Response: {} {}", response.statusCode, response.responseMessage)
    Logger.trace("Response headers: {}", response.headers)
    if (result is Result.Failure) {
        Logger.error(result.getException(), "Error making request: url={}", request.url)
        Logger.error("Response Data: {}", response.data.decodeToString())
        return
    }

    Logger.trace("Response Data: {}", result.get())
}

fun stream(url: String, address: String, signature: String, jsonBody: String): List<String> {
    // Set up the URL and connection
    val url = URL(url)
    val connection = url.openConnection() as HttpURLConnection
    connection.requestMethod = "POST"
    connection.setRequestProperty("X-Requester-Address", address)
    connection.setRequestProperty("Authorization", signature)
    connection.setRequestProperty("Content-Type", "application/json")
    connection.doOutput = true

    // Send the request body
    connection.outputStream.use { outputStream ->
        BufferedWriter(OutputStreamWriter(outputStream, "UTF-8")).use { writer ->
            writer.write(jsonBody)
            writer.flush()
        }
    }

    val lines = mutableListOf<String>()
    // Check response code
    val responseCode = connection.responseCode
    if (responseCode == HttpURLConnection.HTTP_OK) {
        // Read the event stream line by line
        val reader = BufferedReader(InputStreamReader(connection.inputStream))
        var line: String?

        // Continuously read from the stream
        while (reader.readLine().also { line = it } != null) {
            Logger.debug(line)
            lines.add(line!!)
        }

        reader.close()
    } else {
        Logger.error("Failed to connect to API: ResponseCode={}", responseCode)
    }

    connection.disconnect()

    return lines
}
