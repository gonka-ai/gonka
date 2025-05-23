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
const val SERVER_TYPE_CHAIN = "chain"
const val SERVER_TYPE_VERIFIER = "verifier"

data class ApplicationAPI(val urls: Map<String, String>, override val config: ApplicationConfig, val cli: ApplicationCLI? = null) : HasConfig {
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

    fun getActiveParticipants(): ActiveParticipantsResponse = wrapLog("GetActiveParticipants", false) {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        val resp = Fuel.get("$url/v1/epochs/current/participants")
            .timeoutRead(1000*60)
            .responseObject<ActiveParticipantsResponse>(gsonDeserializer(cosmosJson))
        logResponse(resp)
        resp.third.get()
    }

    fun getActiveParticipantsByEpoch(epoch: String): ActiveParticipantsResponse = wrapLog("GetActiveParticipantsByEpoch", false) {
        val url = urlFor(SERVER_TYPE_PUBLIC)
        val resp = Fuel.get("$url/v1/epochs/$epoch/participants")
            .timeoutRead(1000*60)
            .responseObject<ActiveParticipantsResponse>(gsonDeserializer(cosmosJson))
        logResponse(resp)
        resp.third.get()
    }

    fun getGenesisValidators(): List<GenesisValidator> = wrapLog("GetGenesisValidators", false) {
        // Use the CLI to get the genesis data if available
        if (cli != null) {
            val genesisState = cli.getGenesisState()
            return@wrapLog extractValidatorsFromAppExport(genesisState)
        }

        // Fallback to dummy data if CLI is not available
        listOf(
            GenesisValidator(
                pubKey = "dummy_pub_key",
                votingPower = 10
            )
        )
    }

    fun extractValidatorsFromAppExport(appExport: AppExport): List<GenesisValidator> {
        val validators = mutableListOf<GenesisValidator>()

        val genutil = appExport.appState.genutil ?: return emptyList()
        val genTxs = genutil.genTxs ?: return emptyList()

        for (tx in genTxs) {
            val body = tx.body ?: continue
            val messages = body.messages ?: continue

            for (msg in messages) {
                if (msg.type != "/cosmos.staking.v1beta1.MsgCreateValidator") {
                    continue
                }

                val pubkey = msg.pubkey ?: continue
                val key = pubkey.key ?: continue

                val value = msg.value ?: continue
                val amount = value.amount ?: continue

                val validator = GenesisValidator(
                    pubKey = key,
                    votingPower = amount.toLongOrNull() ?: 0
                )
                validators.add(validator)
            }
        }

        return validators
    }

    private fun extractValidatorsFromGenesis(genesis: Map<String, Any>): List<GenesisValidator> {
        val validators = mutableListOf<GenesisValidator>()

        @Suppress("UNCHECKED_CAST")
        val result = genesis["result"] as? Map<String, Any> ?: return emptyList()

        @Suppress("UNCHECKED_CAST")
        val genesisData = result["genesis"] as? Map<String, Any> ?: return emptyList()

        @Suppress("UNCHECKED_CAST")
        val appState = genesisData["app_state"] as? Map<String, Any> ?: return emptyList()

        @Suppress("UNCHECKED_CAST")
        val genutil = appState["genutil"] as? Map<String, Any> ?: return emptyList()

        @Suppress("UNCHECKED_CAST")
        val genTxs = genutil["gen_txs"] as? List<Map<String, Any>> ?: return emptyList()

        for (tx in genTxs) {
            @Suppress("UNCHECKED_CAST")
            val body = tx["body"] as? Map<String, Any> ?: continue

            @Suppress("UNCHECKED_CAST")
            val messages = body["messages"] as? List<Map<String, Any>> ?: continue

            for (msg in messages) {
                val type = msg["@type"] as? String ?: continue

                if (type != "/cosmos.staking.v1beta1.MsgCreateValidator") {
                    continue
                }

                @Suppress("UNCHECKED_CAST")
                val pubkey = msg["pubkey"] as? Map<String, Any> ?: continue
                val key = pubkey["key"] as? String ?: continue

                @Suppress("UNCHECKED_CAST")
                val value = msg["value"] as? Map<String, Any> ?: continue
                val amount = value["amount"] as? String ?: continue

                val validator = GenesisValidator(
                    pubKey = key,
                    votingPower = amount.toLongOrNull() ?: 0
                )
                validators.add(validator)
            }
        }

        return validators
    }

    fun extractValidatorsFromActiveParticipants(activeParticipants: ActiveParticipantsResponse): List<GenesisValidator> {
        val validators = mutableListOf<GenesisValidator>()

        for (participant in activeParticipants.activeParticipants.participants) {
            val validator = GenesisValidator(
                pubKey = participant.validatorKey,
                votingPower = participant.weight
            )
            validators.add(validator)
        }

        return validators
    }

    fun verifyProof(activeParticipants: ActiveParticipantsResponse): Boolean = wrapLog("VerifyProof", false) {
        val url = urlFor(SERVER_TYPE_VERIFIER)
        val payload = mapOf(
            "value" to activeParticipants.activeParticipantsBytes,
            "app_hash" to (activeParticipants.block?.get(2)?.header?.appHash ?: ""),
            "proof_ops" to activeParticipants.proofOps,
            "epoch" to activeParticipants.activeParticipants.epochGroupId.toInt()
        )
        val resp = Fuel.post("$url/v1/verify-proof")
            .jsonBody(payload, cosmosJson)
            .response()
        logResponse(resp)
        resp.second.statusCode == 200
    }

    // Define a data class for the block version with explicit Int type
    private data class BlockVersionInt(
        val block: Int
    )

    // Define a data class for the block header with explicit Int type for height
    private data class BlockHeaderInt(
        val version: BlockVersionInt,
        val chainId: String?,
        val height: Int,
        val time: String?,
        val lastBlockId: BlockId?,
        val lastCommitHash: String?,
        val dataHash: String?,
        val validatorsHash: String?,
        val nextValidatorsHash: String?,
        val consensusHash: String?,
        val appHash: String?,
        val lastResultsHash: String?,
        val evidenceHash: String?,
        val proposerAddress: String?
    )

    // Define a data class for the last commit with explicit Int type for height
    private data class LastCommitInt(
        val height: Int,
        val round: Int?,
        val blockId: BlockId?,
        val signatures: List<Signature>?
    )

    // Define a data class for the block with Int types
    private data class BlockInt(
        val header: BlockHeaderInt,
        val data: BlockData?,
        val evidence: BlockEvidence?,
        val lastCommit: LastCommitInt?
    )

    // Define a data class for the validator with explicit Int type for voting power
    private data class ValidatorInt(
        val pubKey: String,
        val votingPower: Int
    )

    // Define a data class for the verify block payload
    private data class VerifyBlockPayload(
        val block: BlockInt,
        val validators: List<ValidatorInt>
    )

    fun verifyBlock(block: Block, validators: List<GenesisValidator>): Boolean = wrapLog("VerifyBlock", false) {
        val url = urlFor(SERVER_TYPE_VERIFIER)

        // Convert the block to a BlockInt with explicit Int types
        val blockInt = BlockInt(
            header = BlockHeaderInt(
                version = BlockVersionInt(
                    block = block.header?.version?.block?.toInt() ?: 0
                ),
                chainId = block.header?.chainId,
                height = block.header?.height?.toInt() ?: 0,
                time = block.header?.time,
                lastBlockId = block.header?.lastBlockId,
                lastCommitHash = block.header?.lastCommitHash,
                dataHash = block.header?.dataHash,
                validatorsHash = block.header?.validatorsHash,
                nextValidatorsHash = block.header?.nextValidatorsHash,
                consensusHash = block.header?.consensusHash,
                appHash = block.header?.appHash,
                lastResultsHash = block.header?.lastResultsHash,
                evidenceHash = block.header?.evidenceHash,
                proposerAddress = block.header?.proposerAddress
            ),
            data = block.data,
            evidence = block.evidence,
            lastCommit = block.lastCommit?.let { lastCommit ->
                LastCommitInt(
                    height = lastCommit.height?.toInt() ?: 0,
                    round = lastCommit.round,
                    blockId = lastCommit.blockId,
                    signatures = lastCommit.signatures
                )
            }
        )

        // Convert the validators to ValidatorInt with explicit Int types
        val validatorsInt = validators.map { validator ->
            ValidatorInt(
                pubKey = validator.pubKey,
                votingPower = validator.votingPower.toInt()
            )
        }

        // Create the payload
        val payload = VerifyBlockPayload(
            block = blockInt,
            validators = validatorsInt
        )

        val resp = Fuel.post("$url/v1/verify-block")
            .jsonBody(payload, cosmosJson)
            .response()
        logResponse(resp)
        resp.second.statusCode == 200
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
