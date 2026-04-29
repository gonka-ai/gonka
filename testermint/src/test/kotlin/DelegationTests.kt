import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class DelegationTests : TestermintTest() {

    // Two models with different raw weights and inverse coefficients.
    // Model A (defaultModel): base weight 10, coefficient 5.0 -> consensus contribution 50
    // Model B (secondModel):  base weight 100, coefficient 0.1 -> consensus contribution 10
    // Serving both models: consensusWeight = 10*5 + 100*0.1 = 60
    // Serving only model A: consensusWeight = 10*5 = 50
    private val pocWeightA = 10L
    private val pocWeightB = 100L
    private val coeffA = 5.0
    private val coeffB = 0.1

    // --- Spec builders ---

    private fun baseMultiModelSpec(
        delegationParams: Spec<DelegationParams>,
        secondModelPenaltyStartEpoch: Long = 0L,
    ) = spec {
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
                            penaltyStartEpoch = secondModelPenaltyStartEpoch,
                        ),
                    )
                    this[PocParams::pocV2Enabled] = true
                    this[PocParams::validationSlots] = 32L
                    this[PocParams::pocNormalizationEnabled] = false
                }
                this[InferenceParams::delegationParams] = delegationParams.merge(spec<DelegationParams> {
                    this[DelegationParams::initialModelId] = defaultModel
                })
            }
            this[InferenceState::genesisOnlyParams] = spec<GenesisOnlyParams> {
                this[GenesisOnlyParams::maxIndividualPowerPercentage] = Decimal.fromDouble(0.0)
            }
        }
    }

    // --- Node setup helpers ---

    /** Configure a pair with two MLNodes: one per model. Both serve model A and B. */
    private fun setupBothModels(pair: LocalInferencePair) {
        val pairName = pair.name.trim('/')
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
        pair.api.setNodesTo(listOf(nodeA, nodeB))
        pair.mock?.setPocResponse(pocWeightA, nodeA.pocHost)
        pair.mock?.setPocResponse(pocWeightB, nodeB.pocHost)
    }

    /** Configure a pair with one MLNode: serves only model A. */
    private fun setupModelAOnly(pair: LocalInferencePair) {
        val pairName = pair.name.trim('/')
        val nodeA = validNode.copy(
            id = "node-a",
            host = "ml-0001.$pairName.test",
            models = mapOf(defaultModel to ModelConfig(args = emptyList())),
        )
        pair.api.setNodesTo(nodeA)
        pair.mock?.setPocResponse(pocWeightA, nodeA.pocHost)
    }

    // --- Delegation tx helpers ---

    private fun LocalInferencePair.setPoCDelegation(modelId: String, delegateTo: String): TxResponse =
        submitTransaction(listOf("inference", "set-poc-delegation", modelId, delegateTo))

    private fun LocalInferencePair.refusePoCDelegation(modelId: String): TxResponse =
        submitTransaction(listOf("inference", "refuse-poc-delegation", modelId))

    private fun LocalInferencePair.declarePoCIntent(modelId: String): TxResponse =
        submitTransaction(listOf("inference", "declare-poc-intent", modelId))

    private fun LocalInferencePair.queryPoCDelegation(): PoCDelegationResponse =
        node.execAndParse(listOf("query", "inference", "poc-delegation", node.getColdAddress()))

    private fun LocalInferencePair.waitForActiveEpoch(targetEpoch: Long, maxBlocks: Int = 300): ActiveParticipantsResponse {
        var latest: ActiveParticipantsResponse? = null
        waitForBlock(maxBlocks) {
            latest = it.api.getActiveParticipants()
            latest!!.activeParticipants.epochId == targetEpoch
        }
        return latest!!
    }

    // --- Tests ---

    @Test
    fun `all direct with non-zero delegation params produces unchanged weights and voting powers`() {
        val delegationSpec = spec<DelegationParams> {
            this[DelegationParams::deployWindow] = 1L
            this[DelegationParams::refusalPenalty] = Decimal.fromDouble(0.25)
            this[DelegationParams::noParticipationPenalty] = Decimal.fromDouble(0.5)
            this[DelegationParams::delegationShare] = Decimal.fromDouble(0.2)
            this[DelegationParams::vMin] = 1L
        }
        val (cluster, genesis) = initCluster(1, reboot = true, mergeSpec = baseMultiModelSpec(delegationSpec))

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        logSection("Setting up two MLNodes per participant (one per model)")
        val allPairs = cluster.allPairs
        allPairs.forEach { setupBothModels(it) }

        logSection("Waiting for PoC cycles to stabilize (genesis PoC lags ~1 epoch behind joins)")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, 3)

        logSection("Verifying weights and voting powers")
        val participants = genesis.api.getActiveParticipants().activeParticipants.participants
        assertThat(participants).isNotEmpty

        val expectedWeight = (pocWeightA * coeffA + pocWeightB * coeffB).toLong() // 60
        for (p in participants) {
            logSection("Participant ${p.index}: weight=${p.weight}, votingPowers=${p.votingPowers}")

            // All participants serve both models -> all DIRECT -> no penalty regardless of params
            assertThat(p.weight).isEqualTo(expectedWeight)

            // Voting powers: each participant is DIRECT for both models
            assertThat(p.votingPowers).isNotNull
            assertThat(p.votingPowers!!).hasSize(2)

            val vpByModel = p.votingPowers!!.associateBy { it.modelId }
            assertThat(vpByModel).containsKey(defaultModel)
            assertThat(vpByModel).containsKey(secondModel)
            // VP = own consensusWeight (no delegators)
            assertThat(vpByModel[defaultModel]!!.votingPower).isEqualTo(expectedWeight)
            assertThat(vpByModel[secondModel]!!.votingPower).isEqualTo(expectedWeight)
        }
    }

    @Test
    fun `non pre-eligible bootstrap model contributes no weight and penalizes missing choice`() {
        val delegationSpec = spec<DelegationParams> {
            this[DelegationParams::deployWindow] = 1L
            this[DelegationParams::refusalPenalty] = Decimal.fromDouble(0.25)
            this[DelegationParams::noParticipationPenalty] = Decimal.fromDouble(0.5)
            this[DelegationParams::delegationShare] = Decimal.fromDouble(0.0)
            this[DelegationParams::vMin] = 1L
        }
        val (cluster, genesis) = initCluster(2, reboot = true, mergeSpec = baseMultiModelSpec(delegationSpec))

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        logSection("Configuring nodes: A=both models, B=model A only (REFUSE), C=model A only (NONE)")
        val nodeA = genesis
        val nodeB = cluster.joinPairs[0]
        val nodeC = cluster.joinPairs[1]

        setupBothModels(nodeA)
        setupModelAOnly(nodeB)
        setupModelAOnly(nodeC)

        logSection("Node A declares intent and Node B refuses delegation for secondModel")
        assertThat(nodeA.declarePoCIntent(secondModel).code).isEqualTo(0)
        nodeB.refusePoCDelegation(secondModel)

        logSection("Waiting for PoC cycles to stabilize (genesis PoC lags ~1 epoch behind joins)")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, 3)

        logSection("Verifying weights and voting powers")
        val participants = genesis.api.getActiveParticipants().activeParticipants.participants

        // Map by address for targeted assertions
        val pA = participants.first { it.index == nodeA.node.getColdAddress() }
        val pB = participants.first { it.index == nodeB.node.getColdAddress() }
        val pC = participants.first { it.index == nodeC.node.getColdAddress() }

        logSection("Node A: weight=${pA.weight}, votingPowers=${pA.votingPowers}")
        logSection("Node B: weight=${pB.weight}, votingPowers=${pB.votingPowers}")
        logSection("Node C: weight=${pC.weight}, votingPowers=${pC.votingPowers}")

        // secondModel is bootstrap-only and not pre-eligible here.
        // It contributes zero consensus weight.
        // A declared bootstrap intent, which is acceptable while preEligible=false.
        // B refused and C made no choice, so both are punished as NONE.
        // A keeps model A only -> 50
        // B serves A only -> floor(50 * 0.5) penalty -> 25
        // C serves A only -> floor(50 * 0.5) penalty -> 25
        assertThat(pA.weight).isEqualTo(50)
        assertThat(pB.weight).isEqualTo(25)
        assertThat(pC.weight).isEqualTo(25)

        // Voting powers only exist for model A because secondModel never entered validation.
        assertThat(pA.votingPowers).isNotNull
        val vpA = pA.votingPowers!!.associateBy { it.modelId }
        assertThat(vpA).containsKey(defaultModel)
        assertThat(vpA).doesNotContainKey(secondModel)
        assertThat(vpA[defaultModel]!!.votingPower).isEqualTo(50)

        assertThat(pB.votingPowers).isNotNull
        val vpB = pB.votingPowers!!.associateBy { it.modelId }
        assertThat(vpB).containsKey(defaultModel)
        assertThat(vpB).doesNotContainKey(secondModel)
        assertThat(vpB[defaultModel]!!.votingPower).isEqualTo(25)

        assertThat(pC.votingPowers).isNotNull
        val vpC = pC.votingPowers!!.associateBy { it.modelId }
        assertThat(vpC).containsKey(defaultModel)
        assertThat(vpC).doesNotContainKey(secondModel)
        assertThat(vpC[defaultModel]!!.votingPower).isEqualTo(25)
    }

    @Test
    fun `pre eligible bootstrap intent without commit is punished like refusal`() {
        val delegationSpec = spec<DelegationParams> {
            this[DelegationParams::deployWindow] = 1L
            this[DelegationParams::refusalPenalty] = Decimal.fromDouble(0.25)
            this[DelegationParams::noParticipationPenalty] = Decimal.fromDouble(0.5)
            this[DelegationParams::delegationShare] = Decimal.fromDouble(0.0)
            this[DelegationParams::vMin] = 1L
        }
        val (cluster, genesis) = initCluster(3, reboot = true, mergeSpec = baseMultiModelSpec(delegationSpec))

        val nodeA = genesis
        val nodeB = cluster.joinPairs[0]
        val nodeC = cluster.joinPairs[1]
        val nodeD = cluster.joinPairs[2]

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        setupBothModels(nodeA)
        setupModelAOnly(nodeB)
        setupModelAOnly(nodeC)
        setupBothModels(nodeD)

        val addrA = nodeA.node.getColdAddress()

        logSection("Bootstrap pre-eligibility inputs: A intent, B intent, C delegation for secondModel")
        assertThat(nodeA.declarePoCIntent(secondModel).code).isEqualTo(0)
        assertThat(nodeB.declarePoCIntent(secondModel).code).isEqualTo(0)
        assertThat(nodeC.setPoCDelegation(secondModel, addrA).code).isEqualTo(0)

        logSection("Waiting for PoC cycles to stabilize with bootstrap pre-eligibility")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, 3)

        val participants = genesis.api.getActiveParticipants().activeParticipants.participants
        val pA = participants.first { it.index == nodeA.node.getColdAddress() }
        val pB = participants.first { it.index == nodeB.node.getColdAddress() }
        val pC = participants.first { it.index == nodeC.node.getColdAddress() }
        val pD = participants.first { it.index == nodeD.node.getColdAddress() }

        for (p in listOf("A" to pA, "B" to pB, "C" to pC, "D" to pD)) {
            logSection("Node ${p.first}: weight=${p.second.weight}, votingPowers=${p.second.votingPowers}")
        }

        // A, D serve both models -> consensusWeight = 50 + 10 = 60, no penalty.
        // B declared intent for secondModel but doesn't serve it -> no_participation_penalty on consensusWeight.
        // C delegated for secondModel -> no penalty.
        assertThat(pA.weight).isEqualTo(60)
        assertThat(pB.weight).isEqualTo(25) // 50 - floor(50*0.5) = 25
        assertThat(pC.weight).isEqualTo(50)
        assertThat(pD.weight).isEqualTo(60)

        val vpA = pA.votingPowers!!.associateBy { it.modelId }
        val vpB = pB.votingPowers!!.associateBy { it.modelId }
        val vpC = pC.votingPowers!!.associateBy { it.modelId }
        val vpD = pD.votingPowers!!.associateBy { it.modelId }

        assertThat(vpA[defaultModel]!!.votingPower).isEqualTo(60)
        assertThat(vpB[defaultModel]!!.votingPower).isEqualTo(25)
        assertThat(vpC[defaultModel]!!.votingPower).isEqualTo(50)
        assertThat(vpD[defaultModel]!!.votingPower).isEqualTo(60)

        // For secondModel: A and D are direct. C delegated to A. B's missed intent contributes nothing.
        assertThat(vpA[secondModel]!!.votingPower).isEqualTo(110) // own(60) + C's delegation(50)
        assertThat(vpB).doesNotContainKey(secondModel)
        assertThat(vpC).doesNotContainKey(secondModel)
        assertThat(vpD[secondModel]!!.votingPower).isEqualTo(60)
    }

    @Test
    fun `delegation transfers weight and voting power to delegate target`() {
        val delegationSpec = spec<DelegationParams> {
            this[DelegationParams::deployWindow] = 1L
            this[DelegationParams::refusalPenalty] = Decimal.fromDouble(0.0)
            this[DelegationParams::noParticipationPenalty] = Decimal.fromDouble(0.0)
            this[DelegationParams::delegationShare] = Decimal.fromDouble(0.2)
            this[DelegationParams::vMin] = 1L
        }
        val (cluster, genesis) = initCluster(2, reboot = true, mergeSpec = baseMultiModelSpec(delegationSpec))

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        logSection("Configuring nodes: A=both, B=both, C=model A only")
        val nodeA = genesis
        val nodeB = cluster.joinPairs[0]
        val nodeC = cluster.joinPairs[1]

        setupBothModels(nodeA)
        setupBothModels(nodeB)
        setupModelAOnly(nodeC)

        val addrA = nodeA.node.getColdAddress()
        val addrC = nodeC.node.getColdAddress()

        // --- Transaction semantics sub-test ---
        logSection("Transaction semantics: last-write-wins and self-delegation")

        // Step 1: C delegates to A for secondModel
        logSection("C delegates to A for secondModel")
        val delegTx = nodeC.setPoCDelegation(secondModel, addrA)
        assertThat(delegTx.code).isEqualTo(0)

        // Step 2: Query -- delegation present
        val state1 = nodeC.queryPoCDelegation()
        logSection("After delegation: $state1")
        assertThat(state1.delegations).hasSize(1)
        assertThat(state1.delegations[0].modelId).isEqualTo(secondModel)
        assertThat(state1.delegations[0].delegateTo).isEqualTo(addrA)
        assertThat(state1.refusals).isEmpty()
        assertThat(state1.intents).isEmpty()

        // Step 3: C refuses for secondModel -- should clear delegation (last-write-wins)
        logSection("C refuses secondModel (overwrites delegation)")
        val refuseTx = nodeC.refusePoCDelegation(secondModel)
        assertThat(refuseTx.code).isEqualTo(0)

        // Step 4: Query -- refusal present, delegation cleared
        val state2 = nodeC.queryPoCDelegation()
        logSection("After refusal: $state2")
        assertThat(state2.delegations).isEmpty()
        assertThat(state2.refusals).hasSize(1)
        assertThat(state2.refusals[0].modelId).isEqualTo(secondModel)

        // Step 5: C delegates again -- should clear refusal
        logSection("C delegates again (overwrites refusal)")
        nodeC.setPoCDelegation(secondModel, addrA)

        // Step 6: Query -- delegation restored
        val state3 = nodeC.queryPoCDelegation()
        logSection("After re-delegation: $state3")
        assertThat(state3.delegations).hasSize(1)
        assertThat(state3.delegations[0].delegateTo).isEqualTo(addrA)
        assertThat(state3.refusals).isEmpty()

        // Step 7: Self-delegation should fail at CheckTx (don't wait for block inclusion)
        logSection("C attempts self-delegation (should fail)")
        val selfDelegTx = nodeC.submitTransaction(
            listOf("inference", "set-poc-delegation", secondModel, addrC),
            waitForProcessed = false,
        )
        assertThat(selfDelegTx.code).isNotEqualTo(0)
        logSection("Self-delegation tx code: ${selfDelegTx.code}")

        // Step 8: Delegation to A still intact after failed tx
        val state4 = nodeC.queryPoCDelegation()
        logSection("After failed self-delegation: $state4")
        assertThat(state4.delegations).hasSize(1)
        assertThat(state4.delegations[0].delegateTo).isEqualTo(addrA)

        // --- Weight verification ---
        logSection("Waiting for PoC cycles to stabilize with delegation active")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, 3)

        logSection("Verifying weights and voting powers")
        val activeResp = genesis.api.getActiveParticipants()
        val participants = activeResp.activeParticipants.participants

        val pA = participants.first { it.index == nodeA.node.getColdAddress() }
        val pB = participants.first { it.index == nodeB.node.getColdAddress() }
        val pC = participants.first { it.index == nodeC.node.getColdAddress() }

        // Diagnostic: verify model assignment and per-model PoC worked
        for (p in listOf("A" to pA, "B" to pB, "C" to pC)) {
            logSection("Node ${p.first}: models=${p.second.models}, mlNodes=${p.second.mlNodes.map { mn -> mn.mlNodes.map { "${it.nodeId}:w=${it.pocWeight}" } }}, weight=${p.second.weight}, votingPowers=${p.second.votingPowers}")
        }

        // A and B must have both models assigned with non-zero pocWeights
        assertThat(pA.models).containsExactlyInAnyOrder(defaultModel, secondModel)
        assertThat(pA.mlNodes).hasSize(2)
        assertThat(pB.models).containsExactlyInAnyOrder(defaultModel, secondModel)
        assertThat(pB.mlNodes).hasSize(2)

        // C must have only model A
        assertThat(pC.models).containsExactly(defaultModel)
        assertThat(pC.mlNodes).hasSize(1)

        // Expected weights:
        // Consensus before adjustment: A=60, B=60, C=50
        // C is DELEGATE for model B -> delta=floor(50*0.2)=10
        //   C: 50-10=40, A: 60+10=70
        assertThat(pA.weight).isEqualTo(70)
        assertThat(pB.weight).isEqualTo(60)
        assertThat(pC.weight).isEqualTo(40)

        // Voting powers for model A (all DIRECT, VP = own final weight)
        val vpA = pA.votingPowers!!.associateBy { it.modelId }
        val vpB = pB.votingPowers!!.associateBy { it.modelId }
        val vpC = pC.votingPowers!!.associateBy { it.modelId }

        assertThat(vpA[defaultModel]!!.votingPower).isEqualTo(70)
        assertThat(vpB[defaultModel]!!.votingPower).isEqualTo(60)
        assertThat(vpC[defaultModel]!!.votingPower).isEqualTo(40)

        // Voting powers for model B:
        // A (DIRECT): VP = own(70) + delegated(C's final weight 40) = 110
        // B (DIRECT): VP = own(60)
        // C (DELEGATE): no VP entry for model B
        assertThat(vpA[secondModel]!!.votingPower).isEqualTo(110)
        assertThat(vpB[secondModel]!!.votingPower).isEqualTo(60)
        assertThat(vpC).doesNotContainKey(secondModel)
    }

    @Test
    fun `v_min prevents ineligible group from contributing weight`() {
        val delegationSpec = spec<DelegationParams> {
            this[DelegationParams::deployWindow] = 1L
            this[DelegationParams::refusalPenalty] = Decimal.fromDouble(0.25)
            this[DelegationParams::noParticipationPenalty] = Decimal.fromDouble(0.5)
            this[DelegationParams::delegationShare] = Decimal.fromDouble(0.2)
            this[DelegationParams::vMin] = 2L  // Requires 2 members with pocWeight > 0
        }
        val (cluster, genesis) = initCluster(1, reboot = true, mergeSpec = baseMultiModelSpec(delegationSpec))

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        logSection("Configuring nodes: A=both models, B=model A only")
        val nodeA = genesis
        val nodeB = cluster.joinPairs[0]

        setupBothModels(nodeA)
        setupModelAOnly(nodeB)

        logSection("Waiting for PoC cycles to stabilize (genesis PoC lags ~1 epoch behind joins)")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, 3)

        logSection("Verifying weights -- model B should be ineligible (1 member < v_min=2)")
        val participants = genesis.api.getActiveParticipants().activeParticipants.participants

        val pA = participants.first { it.index == nodeA.node.getColdAddress() }
        val pB = participants.first { it.index == nodeB.node.getColdAddress() }

        logSection("Node A: weight=${pA.weight}, votingPowers=${pA.votingPowers}")
        logSection("Node B: weight=${pB.weight}, votingPowers=${pB.votingPowers}")

        // Model B ineligible -> no consensus contribution from model B.
        // Both get base weight from model A only: pocWeightA * coeffA = 50.
        // Bootstrap penalty for non-pre-eligible model B: direct committers are
        // exempt (BootstrapPenaltyDirect), only non-committers get penalized.
        // Node A committed to model B -> no penalty -> 50.
        // Node B did not commit to model B -> BootstrapPenaltyNone -> 50*0.5 = 25.
        assertThat(pA.weight).isEqualTo(50)
        assertThat(pB.weight).isEqualTo(25)

        // Voting powers: only model A entries (model B ineligible -> no VP computed)
        val vpA = pA.votingPowers!!.associateBy { it.modelId }
        val vpB = pB.votingPowers!!.associateBy { it.modelId }

        assertThat(vpA).containsKey(defaultModel)
        assertThat(vpA).doesNotContainKey(secondModel)
        assertThat(vpB).containsKey(defaultModel)
        assertThat(vpB).doesNotContainKey(secondModel)

        assertThat(vpA[defaultModel]!!.votingPower).isEqualTo(50)
        assertThat(vpB[defaultModel]!!.votingPower).isEqualTo(25)
    }

    @Test
    fun `bootstrap no participation penalty starts at configured epoch`() {
        val penaltyStartEpoch = 8L
        val delegationSpec = spec<DelegationParams> {
            this[DelegationParams::deployWindow] = 1L
            this[DelegationParams::refusalPenalty] = Decimal.fromDouble(0.25)
            this[DelegationParams::noParticipationPenalty] = Decimal.fromDouble(0.5)
            this[DelegationParams::delegationShare] = Decimal.fromDouble(0.2)
            this[DelegationParams::vMin] = 2L  // secondModel stays ineligible with one direct member
        }
        val (cluster, genesis) = initCluster(
            1,
            reboot = true,
            mergeSpec = baseMultiModelSpec(delegationSpec, secondModelPenaltyStartEpoch = penaltyStartEpoch),
        )

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        val nodeA = genesis
        val nodeB = cluster.joinPairs[0]

        setupBothModels(nodeA)
        setupModelAOnly(nodeB)

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, 3)

        val beforeGate = genesis.waitForActiveEpoch(penaltyStartEpoch - 1)
        val beforeParticipants = beforeGate.activeParticipants.participants
        val beforeA = beforeParticipants.first { it.index == nodeA.node.getColdAddress() }
        val beforeB = beforeParticipants.first { it.index == nodeB.node.getColdAddress() }

        logSection("Before penalty_start_epoch: epoch=${beforeGate.activeParticipants.epochId}, A=${beforeA.weight}, B=${beforeB.weight}")

        // secondModel is known to the chain but its bootstrap missing-choice penalty
        // must stay disabled until penaltyStartEpoch.
        assertThat(beforeGate.activeParticipants.epochId).isEqualTo(penaltyStartEpoch - 1)
        assertThat(beforeA.weight).isEqualTo(50)
        assertThat(beforeB.weight).isEqualTo(50)

        val atGate = genesis.waitForActiveEpoch(penaltyStartEpoch)
        val atGateParticipants = atGate.activeParticipants.participants
        val atGateA = atGateParticipants.first { it.index == nodeA.node.getColdAddress() }
        val atGateB = atGateParticipants.first { it.index == nodeB.node.getColdAddress() }

        logSection("At penalty_start_epoch: epoch=${atGate.activeParticipants.epochId}, A=${atGateA.weight}, B=${atGateB.weight}")

        // The runtime compares against the upcoming epoch being formed, so the
        // active set for penaltyStartEpoch is the first one where the penalty applies.
        // Node A committed to model B -> exempt from no-participation penalty (Direct).
        // Node B did not commit to model B -> penalized: 50 * 0.5 = 25.
        assertThat(atGate.activeParticipants.epochId).isEqualTo(penaltyStartEpoch)
        assertThat(atGateA.weight).isEqualTo(50)
        assertThat(atGateB.weight).isEqualTo(25)
    }

    @Test
    fun `delegation share starts at configured epoch for eligible model`() {
        val penaltyStartEpoch = 7L
        val delegationSpec = spec<DelegationParams> {
            this[DelegationParams::deployWindow] = 1L
            this[DelegationParams::refusalPenalty] = Decimal.fromDouble(0.0)
            this[DelegationParams::noParticipationPenalty] = Decimal.fromDouble(0.0)
            this[DelegationParams::delegationShare] = Decimal.fromDouble(0.2)
            this[DelegationParams::vMin] = 1L
        }
        val (cluster, genesis) = initCluster(
            2,
            reboot = true,
            mergeSpec = baseMultiModelSpec(delegationSpec, secondModelPenaltyStartEpoch = penaltyStartEpoch),
        )

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        val nodeA = genesis
        val nodeB = cluster.joinPairs[0]
        val nodeC = cluster.joinPairs[1]

        setupBothModels(nodeA)
        setupBothModels(nodeB)
        setupModelAOnly(nodeC)

        val addrA = nodeA.node.getColdAddress()
        assertThat(nodeC.setPoCDelegation(secondModel, addrA).code).isEqualTo(0)

        // Model B participates in PoC on the very next cycle after node registration.
        // Wait 3 cycles as a safety margin for the penalty gate assertions below.
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, 3)

        val beforeGate = genesis.waitForActiveEpoch(penaltyStartEpoch - 1)
        val beforeParticipants = beforeGate.activeParticipants.participants
        val beforeA = beforeParticipants.first { it.index == nodeA.node.getColdAddress() }
        val beforeB = beforeParticipants.first { it.index == nodeB.node.getColdAddress() }
        val beforeC = beforeParticipants.first { it.index == nodeC.node.getColdAddress() }

        logSection("Before delegation_share gate: epoch=${beforeGate.activeParticipants.epochId}, A=${beforeA.weight}, B=${beforeB.weight}, C=${beforeC.weight}")

        // secondModel is eligible, but its delegation_share must still be gated off.
        assertThat(beforeGate.activeParticipants.epochId).isEqualTo(penaltyStartEpoch - 1)
        assertThat(beforeA.weight).isEqualTo(60)
        assertThat(beforeB.weight).isEqualTo(60)
        assertThat(beforeC.weight).isEqualTo(50)

        val atGate = genesis.waitForActiveEpoch(penaltyStartEpoch)
        val atGateParticipants = atGate.activeParticipants.participants
        val atGateA = atGateParticipants.first { it.index == nodeA.node.getColdAddress() }
        val atGateB = atGateParticipants.first { it.index == nodeB.node.getColdAddress() }
        val atGateC = atGateParticipants.first { it.index == nodeC.node.getColdAddress() }

        logSection("At delegation_share gate: epoch=${atGate.activeParticipants.epochId}, A=${atGateA.weight}, B=${atGateB.weight}, C=${atGateC.weight}")

        assertThat(atGate.activeParticipants.epochId).isEqualTo(penaltyStartEpoch)
        assertThat(atGateA.weight).isEqualTo(70)
        assertThat(atGateB.weight).isEqualTo(60)
        assertThat(atGateC.weight).isEqualTo(40)
    }
}
