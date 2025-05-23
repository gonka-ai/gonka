import com.productscience.ApplicationAPI
import com.productscience.EpochStage
import com.productscience.LocalCluster
import com.productscience.SERVER_TYPE_ADMIN
import com.productscience.SERVER_TYPE_CHAIN
import com.productscience.SERVER_TYPE_ML
import com.productscience.SERVER_TYPE_PUBLIC
import com.productscience.SERVER_TYPE_VERIFIER
import com.productscience.data.GenesisValidator
import com.productscience.data.GovernanceMessage
import com.productscience.data.GovernanceProposal
import com.productscience.data.UpdateParams
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger

class ParticipantTests : TestermintTest() {

    @Test
    fun `verify active participants proof`() {
        logSection("Verify Active Participants Proof")

        val (_, genesis) = initCluster()

        // Create a new API with additional URLs for verifier
        val apiUrls = mutableMapOf<String, String>()
        apiUrls.putAll(genesis.api.urls)

        // Add URL for verifier
        val publicUrl = apiUrls[SERVER_TYPE_PUBLIC] ?: ""
        apiUrls[SERVER_TYPE_VERIFIER] = publicUrl

        val api = ApplicationAPI(apiUrls, genesis.config, genesis.node)

        // Get current active participants
        val currentActiveParticipants = api.getActiveParticipants()
        val currentEpoch = currentActiveParticipants.activeParticipants.epochGroupId

        Logger.info("Current epoch: $currentEpoch")

        var prevValidators: List<GenesisValidator>? = null

        for (i in 1L..currentEpoch) {
            if (i == 1L) {
                // For the first epoch, get validators from genesis
                prevValidators = api.getGenesisValidators()
                Logger.info("Genesis validators: $prevValidators")
            }

            // Get active participants for this epoch
            val activeParticipants = api.getActiveParticipantsByEpoch(i.toString())

            // Verify proof
            val proofValid = api.verifyProof(activeParticipants)
            assertThat(proofValid as Boolean).isTrue()
            Logger.info("Proof verification for epoch $i: $proofValid")

            // Verify block with previous validators
            val blockValid = activeParticipants.block?.get(2)?.let { block ->
                prevValidators?.let { validators ->
                    api.verifyBlock(block, validators)
                } ?: false
            } ?: false

            assertThat(blockValid as Boolean).isTrue()
            Logger.info("Block verification for epoch $i: $blockValid")

            // Update previous validators for next iteration
            prevValidators = api.extractValidatorsFromActiveParticipants(activeParticipants)
            Logger.info("Verified epoch $i. prevValidators: $prevValidators")
        }
    }

    @Test
    fun `get active participants`() {
        val (_, genesis) = initCluster()
        logSection("Getting current active participants")
        val participants = genesis.api.getActiveParticipants()
        Logger.info(participants.toString())
        assertThat(participants.activeParticipants.participants).hasSize(3)
    }
    @Test
    fun `reputation increases after epoch participation`() {
        val (_, genesis) = initCluster()
        val startStats = genesis.node.getParticipantCurrentStats()
        logSection("Running inferences")
        runParallelInferences(genesis, 10)
        logSection("verifying reputation increase")
        val endStats = genesis.node.getParticipantCurrentStats()

        val statsPairs = startStats.participantCurrentStats.zip(endStats.participantCurrentStats)
        statsPairs.forEach { (start, end) ->
            assertThat(end.reputation).isGreaterThan(start.reputation)
            assertThat(end.participantId).isEqualTo(start.participantId)
        }
    }

    @Test
    fun `traffic basis decreases minimum average validation`() {
        val (_, genesis) = initCluster()
        logSection("Making sure traffic basis is low")
        var startMin = genesis.node.getMinimumValidationAverage()
        if (startMin.trafficBasis >= 10) {
            // Wait for current and previous values to no longer apply
            genesis.node.waitForMinimumBlock(startMin.blockHeight + genesis.getEpochLength() * 2, "twoEpochsAhead")
            startMin = genesis.node.getMinimumValidationAverage()
        }
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Running inferences")
        runParallelInferences(genesis, 50, waitForBlocks = 1)
        genesis.waitForBlock(2) {
            it.node.getMinimumValidationAverage().minimumValidationAverage < startMin.minimumValidationAverage
        }
        logSection("verifying traffic basis decrease")
        val stopMin = genesis.node.getMinimumValidationAverage()
        assertThat(stopMin.minimumValidationAverage).isLessThan(startMin.minimumValidationAverage)
    }

    @Test
    fun `power to zero removes participant from validators`() {
        val (cluster, genesis) = initCluster()
        genesis.markNeedsReboot()
        val zeroParticipant = cluster.joinPairs.first()
        logSection("Setting ${zeroParticipant.name} to 0 power")
        val zeroParticipantKey = zeroParticipant.node.getValidatorInfo()
        val participants = genesis.api.getParticipants()
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        zeroParticipant.changePoc(0)
        logSection("Confirming ${zeroParticipant.name} is removed from validators")
        val validatorsAfter = genesis.node.getValidators()
        val zeroValidator = validatorsAfter.validators.first {
            it.consensusPubkey.value == zeroParticipantKey.key
        }
        assertThat(zeroValidator.tokens).isZero
        assertThat(zeroValidator.status).isEqualTo(2) // Unbonding
        val cometValidators = genesis.node.getCometValidators()
        assertThat(cometValidators.validators).noneMatch {
            it.pubKey.key == zeroParticipantKey.key
        }
        assertThat(cometValidators.validators).hasSize(2)
    }

    @Test
    fun `stage tests`() {
        val (cluster, genesis) = initCluster()
        EpochStage.entries.forEach { stage ->
            val nextStage = genesis.getNextStage(stage)
            println("Next Stage: $stage block: $nextStage")
        }
        genesis.waitForStage(EpochStage.START_OF_POC)
        EpochStage.entries.forEach { stage ->
            val nextStage = genesis.getNextStage(stage)
            println("Next Stage: $stage block: $nextStage")
        }
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        EpochStage.entries.forEach { stage ->
            val nextStage = genesis.getNextStage(stage)
            println("Next Stage: $stage block: $nextStage")
        }
    }

    @Test
    fun `power to zero and back again restores validator`() {
        val (cluster, genesis) = initCluster()
        val zeroParticipant = cluster.joinPairs.first()
        logSection("Setting ${zeroParticipant.name} to 0 power")
        val zeroParticipantKey = zeroParticipant.node.getValidatorInfo()
        val participants = genesis.api.getParticipants()
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        genesis.markNeedsReboot()
        zeroParticipant.changePoc(0)
        logSection("Confirming ${zeroParticipant.name} is removed from validators")
        val validatorsAfter = genesis.node.getValidators()
        val zeroValidator = validatorsAfter.validators.first {
            it.consensusPubkey.value == zeroParticipantKey.key
        }
        assertThat(zeroValidator.tokens).isZero
        assertThat(zeroValidator.status).isEqualTo(2) // Unbonding
        val cometValidators = genesis.node.getCometValidators()
        assertThat(cometValidators.validators).noneMatch {
            it.pubKey.key == zeroParticipantKey.key
        }
        assertThat(cometValidators.validators).hasSize(2)

        logSection("Setting ${zeroParticipant.name} back to 15 power")
        zeroParticipant.changePoc(15)

        logSection("Confirming ${zeroParticipant.name} is back in validators")
        val validatorsAfterRejoin = genesis.node.getValidators()
        val rejoinedValidator = validatorsAfterRejoin.validators.first {
            it.consensusPubkey.value == zeroParticipantKey.key
        }

        assertThat(rejoinedValidator.tokens).isEqualTo(15)
        assertThat(rejoinedValidator.status).isEqualTo(3) // Bonded
        val cometValidatorsAfterRejoin = genesis.node.getCometValidators()
        assertThat(cometValidatorsAfterRejoin.validators).anyMatch {
            it.pubKey.key == zeroParticipantKey.key
        }
        assertThat(cometValidatorsAfterRejoin.validators).hasSize(3)
    }

    @Test
    fun `pass a setParams proposal`() {
        val (cluster, genesis) = initCluster()
        val params = genesis.getParams()
        val modifiedParams = params.copy(
            validationParams = params.validationParams.copy(
                expirationBlocks = params.validationParams.expirationBlocks + 1
            )
        )
        logSection("Submitting Proposal")
        genesis.runProposal(cluster, UpdateParams(params = modifiedParams))
        genesis.markNeedsReboot()
        logSection("Waiting for Proposal to Pass")
        genesis.node.waitForNextBlock(5)
        logSection("Verifying Pass")
        val newParams = genesis.getParams()
        assertThat(newParams.validationParams).isEqualTo(modifiedParams.validationParams)
    }

    @Test
    fun `fail a setParams proposal`() {
        val (cluster, genesis) = initCluster()
        val params = genesis.getParams()
        val modifiedParams = params.copy(
            validationParams = params.validationParams.copy(
                expirationBlocks = params.validationParams.expirationBlocks + 1
            )
        )
        logSection("Submitting Proposal")
        genesis.runProposal(cluster, UpdateParams(params = modifiedParams), noVoters = cluster.joinPairs.map { it.name })
        genesis.node.waitForNextBlock(5)
        logSection("Verifying Fail")
        val newParams = genesis.getParams()
        assertThat(newParams.validationParams).isEqualTo(params.validationParams)
    }

    @Test
    fun `pass a setParams proposal with a powerful voter`() {
        val (cluster, genesis) = initCluster()
        // genesis node is now powerful enough to pass on its own
        genesis.changePoc(100)
        genesis.markNeedsReboot()
        val params = genesis.getParams()
        val modifiedParams = params.copy(
            validationParams = params.validationParams.copy(
                expirationBlocks = params.validationParams.expirationBlocks + 1
            )
        )
        val proposalId =
            genesis.runProposal(cluster, UpdateParams(params = modifiedParams), noVoters = cluster.joinPairs.map { it.name })
        genesis.node.waitForNextBlock(5)
        val proposals = genesis.node.getGovernanceProposals()
        println(proposals)
        val newParams = genesis.getParams()
        assertThat(newParams.validationParams).isEqualTo(modifiedParams.validationParams)
        val finalTallyResult = proposals.proposals.first { it.id == proposalId }.finalTallyResult
        assertThat(finalTallyResult.noCount).isEqualTo(20)
        assertThat(finalTallyResult.yesCount).isEqualTo(100)
    }

    @Test
    fun `fail a setParams with a zero voter`() {
        val (cluster, genesis) = initCluster()
        val join1 = cluster.joinPairs.first()
        val join2 = cluster.joinPairs.last()
        logSection("Setting ${join1.name} to 0 power")
        genesis.mock?.setPocResponse(11)
        join2.mock?.setPocResponse(12)
        join1.mock?.setPocResponse(0)
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.node.waitForNextBlock(2)
        // At the end of this, genesis has 11 votes, join2 has 12 and join1 should have 0
        // Thus, a vote proposed by genesis and voted NO by join2 should fail
        logSection("Submitting Proposal")
        val params = genesis.getParams()
        val modifiedParams = params.copy(
            validationParams = params.validationParams.copy(
                expirationBlocks = params.validationParams.expirationBlocks + 1
            )
        )
        val proposalId = genesis.runProposal(cluster, UpdateParams(params = modifiedParams), noVoters = listOf(join2.name))
        genesis.node.waitForNextBlock(5)
        logSection("Verifying Fail")
        val newParams = genesis.getParams()
        assertThat(newParams.validationParams).isEqualTo(params.validationParams)
        val paramsProposal = genesis.node.getGovernanceProposals().proposals.first {
            it.id == proposalId
        }
        assertThat(paramsProposal.finalTallyResult.noCount).isEqualTo(12)
        assertThat(paramsProposal.finalTallyResult.yesCount).isEqualTo(11)
        assertThat(paramsProposal.status).isEqualTo(4)
    }

    @Test
    fun `change a participants power`() {
        val (_, genesis) = initCluster(reboot = true)
        logSection("Changing ${genesis.name} power to 11")
        genesis.changePoc(11)
        logSection("Verifying change")
        val validators = genesis.node.getValidators()
        val genesisKey = genesis.node.getValidatorInfo().key
        val genesisValidator = validators.validators.first { it.consensusPubkey.value == genesisKey }
        val tokensAfterChange = genesisValidator.tokens

        logSection("Changing ${genesis.name} power back to 10")
        genesis.changePoc(10)

        logSection("Verifying change back")
        val updatedValidators = genesis.node.getValidators()
        val updatedGenesisValidator = updatedValidators.validators.first { it.consensusPubkey.value == genesisKey }
        assertThat(updatedGenesisValidator.tokens).isEqualTo(10)
        assertThat(tokensAfterChange).isEqualTo(11)
    }
}
