import com.productscience.EpochStage
import com.productscience.data.UpdateParams
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class GovernanceTests : TestermintTest() {
    @Test
    fun `init cluster`() {
        val (cluster, genesis) = initCluster(reboot = true)
        logSection("Cluster initialized with genesis node: ${genesis.name}")
        assertThat(cluster.joinPairs).isNotEmpty
        assertThat(genesis.node).isNotNull
        assertThat(genesis.node.getStatus().syncInfo.latestBlockHeight).isGreaterThan(0)
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


}