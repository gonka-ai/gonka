import com.productscience.EpochStage
import com.productscience.LocalCluster
import com.productscience.data.GovernanceMessage
import com.productscience.data.GovernanceProposal
import com.productscience.data.UpdateParams
import com.productscience.inferenceConfig
import com.productscience.initCluster
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class ParticipantTests : TestermintTest() {
    @Test
    fun `reputation increases after epoch participation`() {
        val (_, genesis) = initCluster()
        val startStats = genesis.node.getParticipantCurrentStats()
        runParallelInferences(genesis, 10)
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
        var startMin = genesis.node.getMinimumValidationAverage()
        if (startMin.trafficBasis >= 10) {
            // Wait for current and previous values to no longer apply
            genesis.node.waitForMinimumBlock(startMin.blockHeight + genesis.getEpochLength() * 2, "twoEpochsAhead")
            startMin = genesis.node.getMinimumValidationAverage()
        }
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        runParallelInferences(genesis, 50, waitForBlocks = 1)
        genesis.waitForBlock(2) {
            it.node.getMinimumValidationAverage().minimumValidationAverage < startMin.minimumValidationAverage
        }
        val stopMin = genesis.node.getMinimumValidationAverage()
        assertThat(stopMin.minimumValidationAverage).isLessThan(startMin.minimumValidationAverage)
    }

    @Test
    fun `power to zero removes participant from validators`() {
        val (cluster, genesis) = initCluster()
        genesis.markNeedsReboot()
        val zeroParticipant = cluster.joinPairs.first()
        val zeroParticipantKey = zeroParticipant.node.getValidatorInfo()
        val participants = genesis.api.getParticipants()
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        zeroParticipant.changePoc(0)
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
        val zeroParticipantKey = zeroParticipant.node.getValidatorInfo()
        val participants = genesis.api.getParticipants()
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        genesis.markNeedsReboot()
        zeroParticipant.changePoc(0)
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

        zeroParticipant.changePoc(15)

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
        runProposal(cluster, UpdateParams(params = modifiedParams))
        genesis.markNeedsReboot()
        genesis.node.waitForNextBlock(5)
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
        runProposal(cluster, UpdateParams(params = modifiedParams), noVoters = cluster.joinPairs.map { it.name })
        genesis.node.waitForNextBlock(5)
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
            runProposal(cluster, UpdateParams(params = modifiedParams), noVoters = cluster.joinPairs.map { it.name })
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
        genesis.mock?.setPocResponse(11)
        join2.mock?.setPocResponse(12)
        join1.mock?.setPocResponse(0)
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.node.waitForNextBlock(2)
        // At the end of this, genesis has 11 votes, join2 has 12 and join1 should have 0
        // Thus, a vote proposed by genesis and voted NO by join2 should fail
        val params = genesis.getParams()
        val modifiedParams = params.copy(
            validationParams = params.validationParams.copy(
                expirationBlocks = params.validationParams.expirationBlocks + 1
            )
        )
        val proposalId = runProposal(cluster, UpdateParams(params = modifiedParams), noVoters = listOf(join2.name))
        genesis.node.waitForNextBlock(5)
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
        val (cluster, genesis) = initCluster(reboot = true)
        genesis.changePoc(11)
        val validators = genesis.node.getValidators()
        val genesisKey = genesis.node.getValidatorInfo().key
        val genesisValidator = validators.validators.first { it.consensusPubkey.value == genesisKey }
        val tokensAfterChange = genesisValidator.tokens

        genesis.changePoc(10)

        val updatedValidators = genesis.node.getValidators()
        val updatedGenesisValidator = updatedValidators.validators.first { it.consensusPubkey.value == genesisKey }
        assertThat(updatedGenesisValidator.tokens).isEqualTo(10)
        assertThat(tokensAfterChange).isEqualTo(11)
    }
}

fun runProposal(cluster: LocalCluster, proposal: GovernanceMessage, noVoters: List<String> = emptyList()): String {
    val genesis = cluster.genesis
    val proposalId = genesis.submitGovernanceProposal(
        GovernanceProposal(
            metadata = "http://www.yahoo.com",
            deposit = "${minDeposit}${inferenceConfig.denom}",
            title = "Extend the expiration blocks",
            summary = "some inferences are taking a very long time to respond to, we need a longer expiration",
            expedited = false,
            messages = listOf(
                proposal
            )
        )
    ).getProposalId()!!
    val depositResponse = genesis.makeGovernanceDeposit(proposalId, minDeposit)
    println("DEPOSIT:\n" + depositResponse)
    cluster.allPairs.forEach {
        val response2 = it.voteOnProposal(proposalId, if (noVoters.contains(it.name)) "no" else "yes")
        assertThat(response2).isNotNull()
        println("VOTE:\n" + response2)
    }
    return proposalId
}