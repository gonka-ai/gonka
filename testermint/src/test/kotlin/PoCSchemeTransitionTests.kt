import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit

@Timeout(value = 45, unit = TimeUnit.MINUTES)
class PoCSchemeTransitionTests : TestermintTest() {
    private val prefill = "POC_SCHEME_PREFILL"
    private val decode = "POC_SCHEME_DECODE"
    // One batch per fixed generation window: DECODE throughput is 5x lower.
    // Accidentally preserving PREFILL nodes or evaluating its DECODE trial is visible.
    private val prefillNonces = 10L
    private val decodeNonces = 2L

    @Test
    fun `mixed prefill confirmations then governance switch to decode`() {
        val coefficient = DynamicCoefficientModelConfig(
            coeffMin = Decimal.fromDouble(1.0), coeffMax = Decimal.fromDouble(1.0),
            relativeDifficulty = Decimal.fromDouble(1.0), targetShareBps = 10000,
        )
        val transitionSpec = createConfirmationPoCSpec(1000, pocSlotAllocation = 0.33).merge(spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::params] = spec<InferenceParams> {
                    this[InferenceParams::epochParams] = spec<EpochParams> {
                        // Room for multiple confirmations and a governance vote in one epoch.
                        this[EpochParams::epochLength] = 80L
                    }
                    this[InferenceParams::pocParams] = spec<PocParams> {
                        this[PocParams::pocScheme] = prefill
                        this[PocParams::pocNormalizationEnabled] = false
                        this[PocParams::models] = listOf(PoCModelConfig(
                            modelId = defaultModel,
                            schemes = listOf(
                                PocSchemeParams(scheme = prefill, seqLen = 1024, dynamicCoefficient = coefficient),
                                PocSchemeParams(scheme = decode, seqLen = 256, maxTokens = 256, dynamicCoefficient = coefficient),
                            ),
                        ))
                    }
                }
                this[InferenceState::genesisOnlyParams] = spec<GenesisOnlyParams> {
                    this[GenesisOnlyParams::maxIndividualPowerPercentage] = Decimal.fromDouble(0.0)
                }
            }
        })
        val (cluster, genesis) = initCluster(2, mergeSpec = transitionSpec, reboot = true, resetMlNodes = false)
        cluster.allPairs.forEach { pair ->
            pair.addNodes(2)
            pair.setPocWeight(prefillNonces)
            // The mock selects this override from params.decode, not from test phase.
            requireNotNull(pair.mock).setPocResponse(decodeNonces, scenarioName = "DECODE")
        }

        // Newly added nodes need one measured round before they can be preserved.
        logSection("Measure all nodes in PREFILL before testing preservation")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        val warmupEpoch = genesis.getEpochData().latestEpoch.index
        assertNodeWeights(genesis, warmupEpoch, prefillNonces)
        assertRecipe(awaitConfirmation(genesis, warmupEpoch, 0), decode, tracking = true)
        assertRecipe(awaitConfirmation(genesis, warmupEpoch, 1), prefill, tracking = false)
        assertThat(confirmationWeights(genesis, warmupEpoch).values).containsOnly(prefillNonces * 3)

        logSection("PREFILL regular PoC with preservation")
        val mixedStart = genesis.waitForStage(EpochStage.START_OF_POC).stageBlock
        assertPreservation(genesis, mixedStart, expected = true)
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        val mixedEpoch = genesis.getEpochData().latestEpoch.index
        assertNodeWeights(genesis, mixedEpoch, prefillNonces)
        val initialConfirmationWeights = confirmationWeights(genesis, mixedEpoch)
        assertThat(initialConfirmationWeights).hasSize(3)
        assertThat(initialConfirmationWeights.values).containsOnly(prefillNonces * 3)

        val first = awaitConfirmation(genesis, mixedEpoch, 0)
        assertRecipe(first, decode, tracking = true)
        assertThat(confirmationWeights(genesis, mixedEpoch))
            .describedAs("the slower DECODE trial must not reduce PREFILL confirmation weights")
            .isEqualTo(initialConfirmationWeights)
        assertRecipe(awaitConfirmation(genesis, mixedEpoch, 1), prefill, tracking = false)
        assertThat(confirmationWeights(genesis, mixedEpoch)).isEqualTo(initialConfirmationWeights)

        logSection("Governance switches live params while the PREFILL epoch stays frozen")
        val params = genesis.getParams()
        genesis.runProposal(cluster, UpdateParams(params = params.copy(pocParams = params.pocParams.copy(pocScheme = decode))))
        assertThat(genesis.getParams().pocParams.pocScheme).isEqualTo(decode)
        assertThat(genesis.getEpochData().latestEpoch.index).isEqualTo(mixedEpoch)
        // Check an event created after enactment, not a previously completed event.
        val afterProposal = genesis.node.listConfirmationPoCEvents(mixedEpoch).events
            .maxOf { it.eventSequence } + 1
        assertRecipe(awaitConfirmation(genesis, mixedEpoch, afterProposal), prefill, tracking = false)

        logSection("First regular DECODE PoC must mine every node afresh")
        val decodeStart = genesis.waitForStage(EpochStage.START_OF_POC).stageBlock
        assertPreservation(genesis, decodeStart, expected = false)
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        val decodeEpoch = genesis.getEpochData().latestEpoch.index
        assertThat(decodeEpoch).isEqualTo(mixedEpoch + 1)
        assertNodeWeights(genesis, decodeEpoch, decodeNonces)
        val decodeConfirmationWeights = confirmationWeights(genesis, decodeEpoch)
        assertThat(decodeConfirmationWeights).hasSize(3)
        assertThat(decodeConfirmationWeights.values).containsOnly(decodeNonces * 3)
        for (sequence in 0L..1L) {
            assertRecipe(awaitConfirmation(genesis, decodeEpoch, sequence), decode, tracking = false)
            assertThat(confirmationWeights(genesis, decodeEpoch)).isEqualTo(decodeConfirmationWeights)
        }

        logSection("Later DECODE PoC resumes preservation using only DECODE weights")
        val nextStart = genesis.waitForStage(EpochStage.START_OF_POC).stageBlock
        assertPreservation(genesis, nextStart, expected = true)
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        assertNodeWeights(genesis, genesis.getEpochData().latestEpoch.index, decodeNonces)
    }

    private fun awaitConfirmation(pair: LocalInferencePair, epoch: Long, sequence: Long): ConfirmationPoCEvent {
        var completed: ConfirmationPoCEvent? = null
        pair.waitForBlock(100) {
            assertThat(it.getEpochData().latestEpoch.index)
                .describedAs("confirmation $sequence must complete in epoch $epoch").isEqualTo(epoch)
            completed = it.node.listConfirmationPoCEvents(epoch).events.firstOrNull { event ->
                event.eventSequence == sequence && event.phase == ConfirmationPoCPhase.CONFIRMATION_POC_COMPLETED
            }
            completed != null
        }
        val event = requireNotNull(completed)
        val expectedNonces = if (requireNotNull(event.recipe).scheme == decode) decodeNonces else prefillNonces
        val commits = pair.node.getAllPoCV2StoreCommitsForStage(event.triggerHeight).commits
        assertThat(commits).describedAs("confirmation must actually generate and commit work").isNotEmpty()
        commits.forEach { commit ->
            val distribution = pair.node.getMLNodeWeightDistribution(event.triggerHeight, commit.participantAddress, defaultModel)
            assertThat(distribution.found).isTrue()
            assertThat(distribution.weights).isNotEmpty()
            assertThat(distribution.weights.map { it.weight }).containsOnly(expectedNonces)
        }
        return event
    }

    private fun assertRecipe(event: ConfirmationPoCEvent, scheme: String, tracking: Boolean) {
        val recipe = requireNotNull(event.recipe)
        assertThat(recipe.scheme).isEqualTo(scheme)
        assertThat(recipe.tracking).isEqualTo(tracking)
        val block = recipe.models.single { it.modelId == defaultModel }.schemes!!.single()
        assertThat(block.scheme).isEqualTo(scheme)
        assertThat(block.seqLen).isEqualTo(if (scheme == decode) 256L else 1024L)
        assertThat(block.maxTokens).isEqualTo(if (scheme == decode) 256L else 0L)
    }

    private fun assertPreservation(pair: LocalInferencePair, height: Long, expected: Boolean) {
        val response = pair.node.queryPreservedNodesSnapshot()
        assertThat(response.found).isTrue()
        val snapshot = requireNotNull(response.snapshot)
        assertThat(snapshot.episodeAnchorHeight).isEqualTo(height)
        val nodes = snapshot.modelPreservedNodes.orEmpty()
            .flatMap { it.participants.orEmpty() }.flatMap { it.nodeIds.orEmpty() }
        if (expected) assertThat(nodes).isNotEmpty() else assertThat(nodes).isEmpty()
    }

    private fun assertNodeWeights(pair: LocalInferencePair, epoch: Long, nonces: Long) {
        val participants = pair.node.queryEpochGroupData(epoch, defaultModel).epochGroupData.validationWeights
        assertThat(participants).hasSize(3)
        participants.forEach {
            assertThat(it.mlNodes).hasSize(3)
            assertThat(it.mlNodes.map { node -> node.pocWeight })
                .describedAs("epoch $epoch must contain only $nonces-nonce node measurements")
                .containsOnly(nonces)
            assertThat(it.weight).isEqualTo(nonces * 3)
        }
    }

    private fun confirmationWeights(pair: LocalInferencePair, epoch: Long) =
        pair.node.queryEpochGroupData(epoch).epochGroupData.validationWeights
            .associate { it.memberAddress to it.confirmationWeight }
}
