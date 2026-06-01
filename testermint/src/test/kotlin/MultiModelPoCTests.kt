import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class MultiModelPoCTests : TestermintTest() {

    // Two models with very different raw weights and inverse coefficients.
    // Model A (defaultModel): base weight 10, coefficient 5.0 -> consensus 50
    // Model B (secondModel):  base weight 100, coefficient 0.1 -> consensus 10
    // Expected total per participant: 60
    private val pocWeightA = 10L
    private val pocWeightB = 100L
    private val coeffA = 5.0
    private val coeffB = 0.1

    private val multiModelSpec = spec {
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::params] = spec<InferenceParams> {
                this[InferenceParams::pocParams] = spec<PocParams> {
                    this[PocParams::models] = listOf(
                        PoCModelConfig(
                            modelId = defaultModel,
                            seqLen = 256L,
                            weightScaleFactor = Decimal.fromDouble(coeffA),
                        ),
                        PoCModelConfig(
                            modelId = secondModel,
                            seqLen = 256L,
                            weightScaleFactor = Decimal.fromDouble(coeffB),
                        ),
                    )
                    this[PocParams::pocV2Enabled] = true
                    this[PocParams::validationSlots] = 2L
                    this[PocParams::pocNormalizationEnabled] = false
                }
            }
            this[InferenceState::genesisOnlyParams] = spec<GenesisOnlyParams> {
                this[GenesisOnlyParams::maxIndividualPowerPercentage] = Decimal.fromDouble(0.0)
            }
        }
    }

    @Test
    fun `multi model poc aggregates weight with coefficients`() {
        val (cluster, genesis) = initCluster(2, reboot = true, mergeSpec = multiModelSpec)

        // Wait for the initial epoch to finish before changing node config.
        // Otherwise genesis may already be mid-PoC with its default single-model node.
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        logSection("Setting up two MLNodes per participant (one per model)")
        val allPairs = listOf(genesis) + cluster.joinPairs

        allPairs.forEach { pair ->
            val pairName = pair.name.trim('/')
            // 1 MLNode = 1 model. Two models require two distinct MLNodes.
            // CoreDNS rewrites ml-XXXX.<pair>.test -> <pair>-mock-server,
            // so both hostnames resolve to the same mock container on port 8080.
            val nodeA = validNode.copy(
                id = "node-a",
                host = "ml-0001.$pairName.test",
                models = mapOf(defaultModel to ModelConfig(args = emptyList())),
            )
            val nodeB = validNode.copy(
                id = "node-b",
                host = "ml-0002.$pairName.test",
                models = mapOf(secondModel to ModelConfig(args = emptyList())),
            )
            pair.api.setNodesTo(nodeA)
            pair.api.addNode(nodeB)
            pair.mock?.setPocResponse(pocWeightA, nodeA.pocHost)
            pair.mock?.setPocResponse(pocWeightB, nodeB.pocHost)
        }

        logSection("Waiting for next PoC cycle with new node config")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, 3)

        logSection("Verifying active participants")
        val activeResp = genesis.api.getActiveParticipants()
        val participants = activeResp.activeParticipants.participants
        assertThat(participants).isNotEmpty

        for (p in participants) {
            logSection("Participant ${p.index}: models=${p.models}, weight=${p.weight}")

            // Each participant should have both models
            assertThat(p.models).containsExactlyInAnyOrder(defaultModel, secondModel)
            assertThat(p.mlNodes).hasSize(2)

            // Each model bucket should have exactly one MLNode
            for ((i, model) in p.models.withIndex()) {
                val nodes = p.mlNodes[i].mlNodes
                logSection("  model=$model nodes=${nodes.map { "${it.nodeId}:w=${it.pocWeight}" }}")
                assertThat(nodes).hasSize(1)
                assertThat(nodes[0].pocWeight).isGreaterThan(0)
            }

            // Weight = coefficient-adjusted aggregate:
            // pocWeightA * coeffA + pocWeightB * coeffB = 10*5 + 100*0.1 = 60
            // Collateral is in grace period, power capping disabled.
            val expectedWeight = (pocWeightA * coeffA + pocWeightB * coeffB).toLong()
            logSection("  expected weight=$expectedWeight, actual weight=${p.weight}")
            assertThat(p.weight).isEqualTo(expectedWeight)
        }

        logSection("Verifying per-model PoC v2 store commits")
        val pocStartHeight = activeResp.activeParticipants.pocStartBlockHeight
        val allCommits = genesis.node.getAllPoCV2StoreCommitsForStage(pocStartHeight)
        logSection("All commits for stage $pocStartHeight: ${allCommits.commits.map { "${it.participantAddress}/${it.modelId}:count=${it.count}" }}")

        // Each participant should have commits for both models
        val commitsByParticipant = allCommits.commits.groupBy { it.participantAddress }
        for ((addr, commits) in commitsByParticipant) {
            val models = commits.map { it.modelId }.toSet()
            logSection("Participant $addr commit models: $models")
            assertThat(models).containsExactlyInAnyOrder(defaultModel, secondModel)
            commits.forEach { assertThat(it.count).isGreaterThan(0) }
        }

        logSection("Verifying per-model weight distributions")
        for (p in participants) {
            for (model in p.models) {
                val dist = genesis.node.getMLNodeWeightDistribution(pocStartHeight, p.index, model)
                logSection("Participant ${p.index} model=$model dist=${dist.weights.map { "${it.nodeId}:w=${it.weight}" }}")
                assertThat(dist.found).isTrue()
                assertThat(dist.weights).isNotEmpty()
            }
        }

        logSection("Verifying slot allocation across models")
        for (p in participants) {
            for ((i, _) in p.models.withIndex()) {
                val nodes = p.mlNodes[i].mlNodes
                for (node in nodes) {
                    assertThat(node.timeslotAllocation).hasSize(2)
                    // PRE_POC_SLOT=true means the node participates
                    assertThat(node.timeslotAllocation[0]).isTrue()
                }
            }
        }

        logSection("Setting up inference responses per model")
        allPairs.forEach { pair ->
            pair.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse("response-model-a"),
                model = defaultModel,
            )
            pair.mock?.setInferenceResponse(
                defaultInferenceResponseObject.withResponse("response-model-b"),
                model = secondModel,
            )
        }

        logSection("Making inference request for model A")
        genesis.waitForNextInferenceWindow()
        val responseA = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(responseA.choices.first().message.content).isEqualTo("response-model-a")

        logSection("Making inference request for model B")
        val requestB = cosmosJson.toJson(inferenceRequestObject.copy(model = secondModel))
        val responseB = genesis.makeInferenceRequest(requestB)
        assertThat(responseB.choices.first().message.content).isEqualTo("response-model-b")
    }
}
