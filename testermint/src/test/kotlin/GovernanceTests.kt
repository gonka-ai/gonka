import com.productscience.EpochStage
import com.productscience.data.UpdateParams
import com.productscience.data.ProposalStatus
import com.productscience.data.spec
import com.productscience.data.AppState
import com.productscience.data.Coin
import com.productscience.data.InferenceState
import com.productscience.data.GenesisOnlyParams
import com.productscience.data.Decimal
import com.productscience.data.MsgTransferWithVesting
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger

class GovernanceTests : TestermintTest() {
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
        logSection("Verifying Fail")
        val newParams = genesis.getParams()
        assertThat(newParams.validationParams).isEqualTo(params.validationParams)
    }

    @Test
    fun `pass a setParams proposal with a powerful voter`() {
        // Disable power capping for this test to preserve original voting power behavior
        val noCappingSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::genesisOnlyParams] = spec<GenesisOnlyParams> {
                    this[GenesisOnlyParams::maxIndividualPowerPercentage] = Decimal.fromDouble(0.0) // Disable power capping
                }
            }
        }

        val noCappingConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(noCappingSpec) ?: noCappingSpec
        )

        val (cluster, genesis) = initCluster(config = noCappingConfig, reboot = true)
        // genesis node is now powerful enough to pass on its own
        genesis.setPocWeight(100)
        genesis.waitForNextEpoch()
        genesis.markNeedsReboot()
        val params = genesis.getParams()
        val modifiedParams = params.copy(
            validationParams = params.validationParams.copy(
                expirationBlocks = params.validationParams.expirationBlocks + 1
            )
        )
        val proposalId =
            genesis.runProposal(cluster, UpdateParams(params = modifiedParams), noVoters = cluster.joinPairs.map { it.name })
        val proposals = genesis.node.getGovernanceProposals()
        println(proposals)
        val newParams = genesis.getParams()
        assertThat(newParams.validationParams).isEqualTo(modifiedParams.validationParams)
        val finalTallyResult = proposals.proposals.first { it.id == proposalId }.finalTallyResult
        assertThat(finalTallyResult.noCount).isEqualTo(20)
        assertThat(finalTallyResult.yesCount).isEqualTo(100)

        // Mark for reboot to reset parameters for subsequent tests
        genesis.markNeedsReboot()
    }

    @Test
    fun `fail a setParams with a zero voter`() {
        // Disable power capping for this test to preserve original voting power behavior
        val noCappingSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::genesisOnlyParams] = spec<GenesisOnlyParams> {
                    this[GenesisOnlyParams::maxIndividualPowerPercentage] = Decimal.fromDouble(0.0) // Disable power capping
                }
            }
        }

        val noCappingConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(noCappingSpec) ?: noCappingSpec
        )

        val (cluster, genesis) = initCluster(config = noCappingConfig, reboot = true)
        val join1 = cluster.joinPairs.first()
        val join2 = cluster.joinPairs.last()
        logSection("Setting ${join1.name} to 0 power")
        genesis.setPocWeight(11)
        join2.setPocWeight(12)
        join1.setPocWeight(0)
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
        logSection("Verifying Fail")
        val newParams = genesis.getParams()
        assertThat(newParams.validationParams).isEqualTo(params.validationParams)
        val paramsProposal = genesis.node.getGovernanceProposals().proposals.first {
            it.id == proposalId
        }
        assertThat(paramsProposal.finalTallyResult.noCount).isEqualTo(12)
        assertThat(paramsProposal.finalTallyResult.yesCount).isEqualTo(11)
        assertThat(paramsProposal.status).isEqualTo(ProposalStatus.REJECTED)

        // Mark for reboot to reset parameters for subsequent tests
        genesis.markNeedsReboot()
    }

    @Test
    fun `send gov funds to an account`() {
        val (cluster, genesis) = initCluster(reboot = true)
        // The gov module account is intentionally not in the app's blocked-address
        // list, so it can be funded with a plain bank send. (Previously this test
        // funded it indirectly through classic-inference reward defaults, which
        // were removed along with the classic inference flow.)
        logSection("Funding governance module account")
        val governanceAddress = genesis.node.getModuleAccount("gov").account.value.address
        val genesisAddress = genesis.node.getColdAddress()
        // MsgTransferWithVesting enforces a 10-gonka minimum per transfer
        // (streamvesting/types/msg_transfer_with_vesting.go), so the gov account
        // must hold at least 10 gonka for the proposal below to execute.
        val fundAmount = 10_000_000_000L
        // Genesis cold wallet starts lean; wait for CLAIM_REWARDS income if needed.
        genesis.ensureGenesisSpendableForDevshard(fundAmount)
        val fundResp = genesis.submitTransaction(
            listOf("bank", "send", genesisAddress, governanceAddress, "$fundAmount${genesis.config.denom}")
        )
        assertThat(fundResp.code).isEqualTo(0)
        logSection("Submit Proposal to send funds")
        val governanceBalance = genesis.node.getBalance(governanceAddress, "ngonka")
        assertThat(governanceBalance.balance.amount).isGreaterThanOrEqualTo(fundAmount)
        val genesisBalance = genesis.node.getBalance(genesisAddress, "ngonka")
        val sendFunds = MsgTransferWithVesting(
            sender = governanceAddress,
            recipient = genesisAddress,
            amount = listOf(Coin("ngonka", governanceBalance.balance.amount)),
            vestingEpochs = 100
        )
        val proposalId = genesis.runProposal(cluster, sendFunds)
        logSection("Verifying Proposal")
        val newGovBalance = genesis.node.getBalance(governanceAddress, "ngonka")
        val newGenesisBalance = genesis.node.getBalance(genesisAddress, "ngonka")
        assertThat(newGovBalance.balance.amount).isEqualTo(0)
        // amount should be unaffected immediately
        assertThat(newGenesisBalance.balance.amount).isEqualTo(genesisBalance.balance.amount)
        val newVestingSchedule = genesis.node.queryVestingSchedule(genesisAddress)
        assertThat(newVestingSchedule).withFailMessage { "No vesting schedule added" }.isNotNull
        val totalAmount = newVestingSchedule.vestingSchedule?.epochAmounts?.sumOf { it.coins.sumOf { it.amount } } ?: 0
        assertThat(totalAmount).isEqualTo(governanceBalance.balance.amount)
        assertThat(newVestingSchedule.vestingSchedule?.epochAmounts).hasSize(100)
    }


}
