package com.productscience

import com.github.kittinunf.fuel.Fuel
import com.github.kittinunf.fuel.core.FuelError
import com.github.kittinunf.fuel.core.HttpException
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
import java.net.URLEncoder

const val SERVER_TYPE_PUBLIC = "public"
const val SERVER_TYPE_ML = "ml"
const val SERVER_TYPE_ADMIN = "admin"

data class ApplicationAPI(
    val urls: Map<String, String>,
    override val config: ApplicationConfig,
    val logOutput: LogOutput,
    val executor: CliExecutor,
) : HasConfig {
    private fun urlFor(type: String): String =
        urls[type] ?: error("URL for type \"$type\" not found in ApplicationAPI")

    fun getPublicUrl() = urlFor(SERVER_TYPE_PUBLIC)


    fun getParticipants(): List<Participant> = wrapLog("GetParticipants", false) {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        val resp = Fuel.get("$url/v1/participants")
            .timeoutRead(1000 * 60)
            .responseObject<ParticipantsResponse>(gsonDeserializer(cosmosJson))
        logResponse(resp)
        resp.third.get().participants
    }

    fun getActiveParticipants(): ActiveParticipantsResponse = wrapLog("GetActiveParticipants", false) {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        val resp = Fuel.get("$url/v1/epochs/current/participants")
            .timeoutRead(1000 * 60)
            .responseObject<ActiveParticipantsResponse>(gsonDeserializer(cosmosJson))
        logResponse(resp)
        resp.third.get()
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
        val encodedInferenceId = URLEncoder.encode(inferenceId, "UTF-8")
        val response = Fuel.get("$url/v1/chat/completions/$encodedInferenceId")
            .responseObject<InferencePayload>(gsonDeserializer(cosmosJson))
        logResponse(response)
        response.third.get()
    }

    fun makeExecutorInferenceRequest(
        request: String,
        requesterAddress: String,
        devSignature: String,
        transferAddress: String,
        taSignature: String,
        timestamp: Long,
        seed: Long = 0
    ): OpenAIResponse = wrapLog("MakeExecutorInferenceRequest", true) {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        val response = Fuel.post((url + "/v1/chat/completions"))
            .jsonBody(request)
            .header("X-Requester-Address", requesterAddress)
            .header("Authorization", devSignature)
            .header("X-Timestamp", timestamp)
            .header("X-Transfer-Address", transferAddress)
            .header("X-Inference-Id", devSignature)
            .header("X-Seed", seed)
            .header("X-TA-Signature", taSignature)
            .timeout(1000 * 60)
            .timeoutRead(1000 * 60)
            .responseObject<OpenAIResponse>(gsonDeserializer(cosmosJson))
        logResponse(response)
        response.third.get()
    }

    fun makeInferenceRequest(
        request: String,
        address: String,
        signature: String,
        timestamp: Long,
    ): OpenAIResponse =
        wrapLog("MakeInferenceRequest", true) {
            val url = urlFor(SERVER_TYPE_PUBLIC)
            val response = Fuel.post((url + "/v1/chat/completions"))
                .jsonBody(request)
                .header("X-Requester-Address", address)
                .header("Authorization", signature)
                .header("X-Timestamp", timestamp)
                .timeout(1000 * 60)
                .timeoutRead(1000 * 60)
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

    /**
     * Creates a stream connection for inference requests that can be interrupted.
     *
     * @param request The request body as a string
     * @param address The requester address
     * @param signature The authorization signature
     * @return A StreamConnection object that can be used to read from the stream and interrupt it
     */
    fun createInferenceStreamConnection(
        request: String,
        address: String,
        signature: String,
        timestamp: Long,
    ): StreamConnection =
        wrapLog("CreateInferenceStreamConnection", true) {
            val url = urlFor(SERVER_TYPE_PUBLIC)
            createStreamConnection(
                url = "$url/v1/chat/completions",
                address = address,
                signature = signature,
                jsonBody = request,
                timestamp = timestamp
            )
        }

    fun setNodesTo(node: InferenceNode) {
        val nodes = getNodes()
        nodes.forEach { removeNode(it.node.id) }
        addNode(node)
    }

    fun setNodesTo(nodes: List<InferenceNode>) {
        val existingNodes = getNodes()
        existingNodes.forEach { removeNode(it.node.id) }
        addNodes(nodes)
    }

    fun getNodes(): List<NodeResponse> =
        wrapLog("getNodes", false) {
            val url = urlFor(SERVER_TYPE_ADMIN)
            var lastException: Exception? = null
            for (attempt in 1..3) {
                try {
                    val response = Fuel.get("$url/admin/v1/nodes")
                        .timeout(1000 * 10)  // 10 seconds connection timeout
                        .timeoutRead(1000 * 10)  // 10 seconds read timeout
                        .responseObject<List<NodeResponse>>(gsonDeserializer(cosmosJson))
                    logResponse(response)
                    return@wrapLog response.third.get()
                } catch (e: Exception) {
                    lastException = e
                    Logger.warn(e, "Exception during getNodes, retrying")
                    if (attempt < 3) {
                        Thread.sleep(5000) // 5 seconds delay
                        continue
                    }
                }
            }
            throw lastException ?: Exception("Failed to get nodes after 3 attempts")
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

    fun enableNode(nodeId: String): NodeAdminStateResponse = wrapLog("EnableNode", true) {
        val url = urlFor(SERVER_TYPE_ADMIN)
        val response = Fuel.post("$url/admin/v1/nodes/$nodeId/enable")
            .responseObject<NodeAdminStateResponse>(gsonDeserializer(cosmosJson))
        logResponse(response)
        response.third.get()
    }

    fun disableNode(nodeId: String): NodeAdminStateResponse = wrapLog("DisableNode", true) {
        val url = urlFor(SERVER_TYPE_ADMIN)
        val response = Fuel.post("$url/admin/v1/nodes/$nodeId/disable")
            .responseObject<NodeAdminStateResponse>(gsonDeserializer(cosmosJson))
        logResponse(response)
        response.third.get()
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

    fun getLatestEpoch(): EpochResponse {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        return get(url, "v1/epochs/latest")
    }

    // -----------------------
    // Restrictions via Decentralized API
    // -----------------------

    fun getRestrictionsStatus(): TransferRestrictionStatusDto {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        return get(url, "v1/restrictions/status")
    }

    fun getRestrictionsExemptions(): TransferExemptionsDto {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        return get(url, "v1/restrictions/exemptions")
    }

    fun getRestrictionsExemptionUsage(exemptionId: String, account: String): ExemptionUsageDto {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        val path = "v1/restrictions/exemptions/$exemptionId/usage/$account"
        return get(url, path)
    }

    fun updateRestrictionsParams(body: UpdateRestrictionsParamsDto): String {
        val url = urlFor(SERVER_TYPE_ADMIN)
        return postWithStringResponse(url, "admin/v1/restrictions/params", body)
    }

    fun executeEmergencyTransfer(body: EmergencyTransferDto): String {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        return postWithStringResponse(url, "v1/restrictions/emergency-transfer", body)
    }

    // -----------------------
    // BLS via Decentralized API
    // -----------------------

    fun requestThresholdSignature(request: RequestThresholdSignatureDto): String =
        wrapLog("RequestThresholdSignature", true) {
            val url = urlFor(SERVER_TYPE_ADMIN)
            postWithStringResponse(url, "admin/v1/bls/request", request)
        }

    fun queryBLSSigningStatus(requestId: String): SigningStatusWrapper = wrapLog("QueryBLSSigningStatus", true) {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        get(url, "v1/bls/signatures/${requestId}")
    }

    fun getBLSEpochWithUncompressed(epochId: Long): Map<String, Any> = wrapLog("GetBLSEpochWithUncompressed", false) {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        get(url, "v1/bls/epochs/$epochId")
    }

    inline fun <reified Out : Any> get(url: String, path: String): Out {
        val response = Fuel.get("$url/$path")
            .responseString()
        logResponse(response)

        val body = response.third.get() // throws on HTTP error
        Logger.trace("Response body: {}", body) // Always log successful response bodies

        return try {
            cosmosJson.fromJson(body, Out::class.java)
        } catch (e: Exception) {
            Logger.error(e, "JSON parse error for url={} body={}", "$url/$path", body)
            throw e
        }
    }

    inline fun <reified In : Any, reified Out : Any> post(url: String, path: String, body: In): Out {
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

    inline fun <reified In : Any> postWithStringResponse(url: String, path: String, body: In): String {
        val response = Fuel.post("$url/$path")
            .jsonBody(body, cosmosJson)
            .responseString()
        logResponse(response)

        return response.third.get()
    }

    /**
     * Retrieves the API configuration from /root/.dapi/api-config.yaml using the CliExecutor.
     * Parses the YAML content directly into an ApiConfig data class using Jackson.
     *
     * @return ApiConfig containing the parsed configuration
     */
    fun getConfig(): ApiConfig = wrapLog("GetConfig", false) {
        val url = urlFor(SERVER_TYPE_ADMIN)
        get<ApiConfig>(url, "admin/v1/config")
    }

}


fun logResponse(reqData: Triple<Request, Response, Result<*, FuelError>>, throwError: Boolean = false) {
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
        if (throwError) {
            throw HttpException(response.statusCode, response.data.decodeToString())
        }
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

/**
 * Creates a stream connection for inference requests that can be interrupted.
 *
 * @param url The URL to connect to
 * @param address The requester address
 * @param signature The authorization signature
 * @param jsonBody The JSON request body
 * @return A StreamConnection object that can be used to read from the stream and interrupt it
 */
fun createStreamConnection(
    url: String,
    address: String,
    signature: String,
    jsonBody: String,
    timestamp: Long
): StreamConnection {
    // Set up the URL and connection
    val url = URL(url)
    val connection = url.openConnection() as HttpURLConnection
    connection.requestMethod = "POST"
    connection.setRequestProperty("X-Requester-Address", address)
    connection.setRequestProperty("Authorization", signature)
    connection.setRequestProperty("Content-Type", "application/json")
    connection.setRequestProperty("X-Timestamp", timestamp.toString())
    connection.doOutput = true

    // Send the request body
    connection.outputStream.use { outputStream ->
        BufferedWriter(OutputStreamWriter(outputStream, "UTF-8")).use { writer ->
            writer.write(jsonBody)
            writer.flush()
        }
    }

    // Check response code
    val responseCode = connection.responseCode
    if (responseCode == HttpURLConnection.HTTP_OK) {
        // Create a reader for the input stream
        val reader = BufferedReader(InputStreamReader(connection.inputStream))
        return StreamConnection(connection, reader)
    } else {
        Logger.error("Failed to connect to API: ResponseCode={}", responseCode)
        connection.disconnect()
        throw RuntimeException("Failed to connect to API: ResponseCode=$responseCode")
    }
}

/**
 * A class representing a connection to a stream that can be interrupted.
 */
class StreamConnection(
    private val connection: HttpURLConnection,
    private val reader: BufferedReader
) : AutoCloseable {
    private var closed = false

    /**
     * Reads the next line from the stream.
     *
     * @return The next line, or null if the stream is closed or has reached the end
     */
    fun readLine(): String? {
        if (closed) return null
        return try {
            reader.readLine()
        } catch (e: Exception) {
            Logger.error(e, "Error reading from stream")
            close()
            null
        }
    }

    /**
     * Closes the connection and the reader.
     */
    override fun close() {
        if (!closed) {
            try {
                reader.close()
            } catch (e: Exception) {
                Logger.error(e, "Error closing reader")
            }
            connection.disconnect()
            closed = true
        }
    }
}
