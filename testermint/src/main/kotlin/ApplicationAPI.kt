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

const val SERVER_TYPE_PUBLIC = "public"
const val SERVER_TYPE_ML = "ml"
const val SERVER_TYPE_ADMIN = "admin"

data class ApplicationAPI(val urls: Map<String, String>, override val config: ApplicationConfig) : HasConfig {
    private fun urlFor(type: String): String =
        urls[type] ?: error("URL for type \"$type\" not found in ApplicationAPI")

    fun getParticipants(): List<Participant> = wrapLog("GetParticipants", false) {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        val resp = Fuel.get("$url/v1/participants")
            .timeoutRead(1000*60)
            .responseObject<ParticipantsResponse>(gsonDeserializer(cosmosJson))
        logResponse(resp)
        resp.third.get().participants
    }

    fun addInferenceParticipant(inferenceParticipant: InferenceParticipant) = wrapLog("AddInferenceParticipant", true) {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        val response = Fuel.post("$url/v1/participants")
            .jsonBody(inferenceParticipant, cosmosJson)
            .response()
        logResponse(response)
    }

    fun addUnfundedInferenceParticipant(inferenceParticipant: UnfundedInferenceParticipant) =
        wrapLog("AddUnfundedInferenceParticipant", true) {
            val url = urlFor(SERVER_TYPE_PUBLIC)
            val response = Fuel.post("$url/v1/participants")
                .jsonBody(inferenceParticipant, cosmosJson)
                .response()
            logResponse(response)
        }

    fun getInferenceOrNull(inferenceId: String): InferencePayload? = wrapLog("getInferenceOrNull", true) {
        try {
            getInference(inferenceId)
        } catch (_: Exception) {
            null
        }
    }

    fun getInference(inferenceId: String): InferencePayload = wrapLog("getInference", true) {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        val response = Fuel.get(url + "/v1/chat/completions/$inferenceId")
            .responseObject<InferencePayload>(gsonDeserializer(cosmosJson))
        logResponse(response)
        response.third.get()
    }

    fun makeInferenceRequest(
        request: String,
        address: String,
        signature: String,
    ): OpenAIResponse =
        wrapLog("MakeInferenceRequest", true) {
            val url = urlFor(SERVER_TYPE_PUBLIC)
            val response = Fuel.post((url + "/v1/chat/completions"))
                .jsonBody(request)
                .header("X-Requester-Address", address)
                .header("Authorization", signature)
                .timeout(1000*60)
                .timeoutRead(1000*60)
                .responseObject<OpenAIResponse>(gsonDeserializer(cosmosJson))
            logResponse(response)
            response.third.get()
        }

    fun makeStreamedInferenceRequest(
        request: String,
        address: String,
        signature: String,
    ): List<String> =
        wrapLog("MakeStreamedInferenceRequest", true) {
            val url = urlFor(SERVER_TYPE_PUBLIC)
            stream(url = "$url/v1/chat/completions", address = address, signature = signature, jsonBody = request)
        }

    fun setNodesTo(node: InferenceNode) {
        val nodes = getNodes()
        if (nodes.all { it.node.id == node.id }) return
        nodes.forEach { removeNode(it.node.id) }
        addNode(node)
    }

    fun getNodes(): List<NodeResponse> =
        wrapLog("GetNodes", false) {
            val url = urlFor(SERVER_TYPE_ADMIN)
            val response = Fuel.get("$url/admin/v1/nodes")
                .responseObject<List<NodeResponse>>(gsonDeserializer(cosmosJson))
            logResponse(response)
            response.third.get()
        }

    fun addNode(node: InferenceNode): InferenceNode = wrapLog("AddNode", true) {
        val url = urlFor(SERVER_TYPE_ADMIN)
        val response = Fuel.post("$url/admin/v1/nodes")
            .jsonBody(node, cosmosJson)
            .responseObject<InferenceNode>(gsonDeserializer(cosmosJson))
        logResponse(response)
        response.third.get()
    }

    fun addNodes(nodes: List<InferenceNode>): List<InferenceNode> = wrapLog("AddNodes", true) {
        val url = urlFor(SERVER_TYPE_ADMIN)
        val response = Fuel.post("$url/admin/v1/nodes/batch")
            .jsonBody(nodes, cosmosJson)
            .responseObject<List<InferenceNode>>(gsonDeserializer(cosmosJson))
        logResponse(response)
        response.third.get()
    }

    fun removeNode(nodeId: String) = wrapLog("RemoveNode", true) {
        val url = urlFor(SERVER_TYPE_ADMIN)
        val response = Fuel.delete("$url/admin/v1/nodes/$nodeId")
            .responseString()
        logResponse(response)
    }

    fun submitPriceProposal(proposal: UnitOfComputePriceProposalDto): String = wrapLog("SubmitPriceProposal", true) {
        val url = urlFor(SERVER_TYPE_ADMIN)
        val response = Fuel.post("$url/admin/v1/unit-of-compute-price-proposal")
            .jsonBody(proposal, cosmosJson)
            .responseString()
        logResponse(response)

        response.third.get()
    }

    fun getPriceProposal(): GetUnitOfComputePriceProposalDto = wrapLog("SubmitPriceProposal", true) {
        val url = urlFor(SERVER_TYPE_ADMIN)
        get<GetUnitOfComputePriceProposalDto>(url, "admin/v1/unit-of-compute-price-proposal")
    }

    fun getPricing(): GetPricingDto = wrapLog("GetPricing", true) {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        get<GetPricingDto>(url, "v1/pricing")
    }

    fun registerModel(model: RegisterModelDto): String = wrapLog("RegisterModel", true) {
        val url = urlFor(SERVER_TYPE_ADMIN)
        postWithStringResponse(url, "admin/v1/models", model)
    }

    fun submitTransaction(json: String): TxResponse {
        val url = urlFor(SERVER_TYPE_ADMIN)
        return postRawJson(url, "admin/v1/tx/send", json)
    }

    fun startTrainingTask(training: StartTrainingDto): String = wrapLog("StartTrainingTask", true) {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        postWithStringResponse(url, "v1/training/tasks", training)
    }

    fun getTrainingTask(taskId: ULong): String = wrapLog("GetTrainingTask", true) {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        get(url, "v1/training/tasks/$taskId")
    }

    inline fun <reified Out: Any> get(url: String, path: String): Out {
        val response = Fuel.get("$url/$path")
            .responseObject<Out>(gsonDeserializer(cosmosJson))
        logResponse(response)

        return response.third.get()
    }

    inline fun <reified In: Any, reified Out: Any> post(url: String, path: String, body: In): Out {
        val response = Fuel.post("$url/$path")
            .jsonBody(body, cosmosJson)
            .responseObject<Out>()
        logResponse(response)

        return response.third.get()
    }

    inline fun <reified Out : Any> postRawJson(url: String, path: String, json: String): Out {
        val response = Fuel.post("$url/$path")
            .jsonBody(json)
            .responseObject<Out>(gsonDeserializer(cosmosJson))
        logResponse(response)

        return response.third.get()
    }

    inline fun <reified In: Any> postWithStringResponse(url: String, path: String, body: In): String {
        val response = Fuel.post("$url/$path")
            .jsonBody(body, cosmosJson)
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

    if (!response.statusCode.toString().startsWith("2")) {
        Logger.error("Response data: {}", response.data.decodeToString())
    }
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
