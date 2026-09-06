import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

/**
 * End-to-end guard for the provenance-preserving seating fix.
 *
 * Attack under test: a participant proves PoC weight under a cheap, honestly
 * served model (sourceModel), then rewrites its on-chain hardware inventory to
 * declare a different, higher-coefficient model (targetModel) before epoch
 * formation seats nodes. The self-reported hardware inventory must act only as a
 * filter: it can exclude the relabeled node but must never migrate its validated
 * weight into the target model bucket.
 *
 * The relabel is injected via a raw MsgSubmitHardwareDiff signed by the
 * participant itself (the same message the DAPI broker sends during node sync),
 * so this exercises the real chain handler + epoch-formation path rather than
 * the in-process seating function.
 *
 * The DAPI broker reconciles hardware every ~60s, so the local inventory is also
 * relabeled to the target model; otherwise a compensating diff could restore the
 * source model before seating and let even a vulnerable implementation pass. The
 * test asserts the on-chain claim is (and stays) the target model across the
 * seated epoch so a silent revert fails loudly instead of producing a false green.
 */
class HardwareRelabelTests : TestermintTest() {

    private val rawPocWeight = 100L
    private val coeffSource = 1.0    // model actually proven via PoC
    private val coeffTarget = 10.0   // high-value model the attacker relabels into

    private val sourceModel = defaultModel
    private val targetModel = secondModel

    // Two nodes on the cheating participant so it keeps validator weight even if
    // the relabeled node is dropped; single node elsewhere.
    private val keepNodeId = "keep"
    private val cheatNodeId = "cheat"

    private val relabelSpec = spec {
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::params] = spec<InferenceParams> {
                this[InferenceParams::pocParams] = spec<PocParams> {
                    this[PocParams::models] = listOf(
                        PoCModelConfig(
                            modelId = sourceModel,
                            seqLen = 256L,
                            weightScaleFactor = Decimal.fromDouble(coeffSource),
                        ),
                        PoCModelConfig(
                            modelId = targetModel,
                            seqLen = 256L,
                            weightScaleFactor = Decimal.fromDouble(coeffTarget),
                        ),
                    )
                    this[PocParams::pocV2Enabled] = true
                    this[PocParams::validationSlots] = 2L
                    this[PocParams::pocNormalizationEnabled] = false
                }
            }
            // Disable per-participant power capping so weight math is exact and an
            // inflation would be plainly visible.
            this[InferenceState::genesisOnlyParams] = spec<GenesisOnlyParams> {
                this[GenesisOnlyParams::maxIndividualPowerPercentage] = Decimal.fromDouble(0.0)
            }
        }
    }

    @Test
    fun `hardware relabel cannot migrate validated weight into another model`() {
        val (cluster, genesis) = initCluster(2, reboot = true, mergeSpec = relabelSpec)

        // Finish the genesis epoch before reconfiguring nodes.
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        logSection("Configuring nodes: genesis has two source-model nodes, joins have one")
        val allPairs = listOf(genesis) + cluster.joinPairs
        allPairs.forEach { pair ->
            val pairName = pair.name.trim('/')
            val keepNode = validNode.copy(
                id = keepNodeId,
                host = "ml-0001.$pairName.test",
                models = mapOf(sourceModel to ModelConfig(args = emptyList())),
            )
            pair.api.setNodesTo(keepNode)
            pair.mock?.setPocResponse(rawPocWeight, keepNode.pocHost)

            if (pair == genesis) {
                val cheatNode = validNode.copy(
                    id = cheatNodeId,
                    host = "ml-0002.$pairName.test",
                    models = mapOf(sourceModel to ModelConfig(args = emptyList())),
                )
                pair.api.addNode(cheatNode)
                pair.mock?.setPocResponse(rawPocWeight, cheatNode.pocHost)
            }
        }

        logSection("Waiting for a full PoC cycle so the new node config is seated")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, 3)

        logSection("Baseline: genesis proved both nodes under the source model")
        val genesisAddr = genesis.node.getColdAddress()
        val baseline = genesis.api.getActiveParticipants().activeParticipants.participants
            .single { it.index == genesisAddr }
        logSection("Baseline genesis: models=${baseline.models}, weight=${baseline.weight}")
        val baselineWeight = (2 * rawPocWeight * coeffSource).toLong()
        assertThat(baseline.models).containsExactly(sourceModel)
        assertThat(baseline.weight).isEqualTo(baselineWeight)

        logSection("Relabeling the cheat node to the target model before seating")
        // Relabel during PoC validation: the PoC batches (and thus the validated
        // node->model provenance) are already fixed to the source model, and this
        // lands well before onEndOfPoCValidationStage reads hardware for seating.
        genesis.waitForStage(EpochStage.START_OF_POC_VALIDATION)

        val pairName = genesis.name.trim('/')
        val cheatHost = "ml-0002.$pairName.test"

        // Relabel the LOCAL inventory too, so the broker's periodic node-sync does
        // not submit a compensating diff that restores the source model before
        // seating. Without this the on-chain claim could silently revert and even a
        // vulnerable implementation would pass.
        genesis.api.removeNode(cheatNodeId)
        genesis.api.addNode(
            validNode.copy(
                id = cheatNodeId,
                host = "ml-0002.$pairName.test",
                models = mapOf(targetModel to ModelConfig(args = emptyList())),
            )
        )

        // Immediate on-chain relabel (the broker's own sync is on a ~60s timer).
        val relabelJson = """
            {"body":{"messages":[{"@type":"/inference.inference.MsgSubmitHardwareDiff","creator":"$genesisAddr","newOrModified":[{"local_id":"$cheatNodeId","status":"POC","models":["$targetModel"],"host":"$cheatHost","port":"8080"}],"removed":[]}],"memo":"","timeout_height":0}}
        """.trimIndent()
        val relabelResp = genesis.submitTransaction(relabelJson)
        assertThat(relabelResp.code).isEqualTo(0)

        // Precondition: the on-chain inventory the seating logic will read now
        // declares the target model for the cheat node.
        assertThat(cheatNodeModelsOnChain(genesis, genesisAddr))
            .describedAs("relabel must land on chain before seating")
            .containsExactly(targetModel)

        logSection("Waiting for the epoch that seats using the relabeled inventory")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        // The relabel must have survived through the seated epoch; if the broker
        // compensated it back to the source model, this test would not have
        // actually exercised the relabeled inventory, so fail loudly.
        assertThat(cheatNodeModelsOnChain(genesis, genesisAddr))
            .describedAs("on-chain relabel must persist across seating (no broker revert)")
            .containsExactly(targetModel)

        val activeResp = genesis.api.getActiveParticipants()
        val participants = activeResp.activeParticipants.participants
        val cheated = participants.single { it.index == genesisAddr }
        logSection("After relabel genesis: models=${cheated.models}, weight=${cheated.weight}")

        // The core security property, asserted exactly: the relabeled cheat node is
        // dropped (its hardware no longer declares its validated model), the keep
        // node retains exactly one source-model contribution, and nothing appears
        // under the target model or its coefficient. Exact values rather than
        // ranges, so an unexpected extra contribution cannot hide.
        assertThat(cheated.models)
            .describedAs("only the source model may remain seated")
            .containsExactly(sourceModel)
        assertThat(cheated.weight)
            .describedAs("exactly the keep node's source-model weight, nothing more")
            .isEqualTo((rawPocWeight * coeffSource).toLong())

        logSection("No participant may be seated under the never-proven target model")
        participants.forEach { p ->
            assertThat(p.models)
                .describedAs("participant ${p.index} must not hold the target model")
                .doesNotContain(targetModel)
        }

        // The target-model subgroup must not carry the relabeled participant.
        val epochIndex = activeResp.activeParticipants.epochId
        runCatching { genesis.node.queryEpochGroupData(epochIndex, targetModel).epochGroupData }
            .onSuccess { targetGroup ->
                val members = targetGroup.validationWeights.map { it.memberAddress }
                logSection("Target subgroup members: $members")
                assertThat(members)
                    .describedAs("relabeled weight must not reach the target subgroup")
                    .doesNotContain(genesisAddr)
            }
    }

    private fun cheatNodeModelsOnChain(pair: LocalInferencePair, participant: String): List<String> =
        pair.node.queryHardwareNodesAll().nodes
            .firstOrNull { it.participant == participant }
            ?.hardwareNodes
            ?.firstOrNull { it.localId == cheatNodeId }
            ?.models
            ?: emptyList()
}
