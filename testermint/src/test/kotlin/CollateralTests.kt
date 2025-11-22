import com.productscience.*
import com.productscience.data.Collateral
import com.productscience.data.TxResponse
import com.productscience.data.spec
import com.productscience.data.AppState
import com.productscience.data.InferenceState
import com.productscience.data.InferenceParams
import com.productscience.data.ValidationParams
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class CollateralTests : TestermintTest() {

    @Test
    fun `a participant can deposit collateral and withdraw it`() {
        val (cluster, genesis) = initCluster(reboot = true)
        val participant = cluster.genesis
        val participantAddress = participant.node.getColdAddress()

        logSection("Despositing collateral")

        logHighlight("Query initial collateral for ${participant.name}")
        val initialCollateral = participant.queryCollateral(participantAddress)
        assertThat(initialCollateral.amount).isNull()

        val depositAmount = 1000L
        logHighlight("Depositing $depositAmount nicoin for ${participant.name}")

        val initialBalance = participant.getBalance(participantAddress)
        logHighlight("Initial balance is ${initialBalance}")
        val result = participant.depositCollateral(depositAmount)
        assertThat(result.code).isEqualTo(0)
        participant.node.waitForNextBlock()

        logHighlight("Verifying collateral and balance changes")
        val collateralAfterDeposit = participant.queryCollateral(participantAddress)
        assertThat(collateralAfterDeposit.amount?.amount).isEqualTo(depositAmount)
        assertThat(collateralAfterDeposit.amount?.denom).isEqualTo("ngonka")

        val balanceAfterDeposit = participant.getBalance(participantAddress)
        // In the local testnet, fees are zero, so the balance should be exactly the initial amount minus the deposit.
        assertThat(balanceAfterDeposit).isEqualTo(initialBalance - depositAmount)

        logSection("Withdrawing $depositAmount nicoin from ${participant.name}")
        val epochBeforeWithdraw = participant.api.getLatestEpoch().latestEpoch.index-1
        val startLastRewardedEpoch = getRewardCalculationEpochIndex(participant)
        val params = participant.node.queryCollateralParams()
        val unbondingPeriod = params.params.unbondingPeriodEpochs.toLong()
        val expectedCompletionEpoch = epochBeforeWithdraw + unbondingPeriod
        logHighlight("Expected completion epoch: $expectedCompletionEpoch (epoch $epochBeforeWithdraw + $unbondingPeriod)")
        Thread.sleep(10000)

        participant.withdrawCollateral(depositAmount)
        participant.node.waitForNextBlock()

        logSection("Verifying withdrawl")
        val activeCollateral = participant.queryCollateral(participantAddress)
        assertThat(activeCollateral.amount).isNull()
        val balanceAfterWithdraw = participant.getBalance(participantAddress)
        assertThat(balanceAfterWithdraw).isEqualTo(balanceAfterDeposit)

        logHighlight("Verifying withdrawal is in the unbonding queue for epoch $expectedCompletionEpoch")
        val unbondingQueue = participant.node.queryUnbondingCollateral(participantAddress)
        assertThat(unbondingQueue.unbondings).hasSize(1)
        val unbondingEntry = unbondingQueue.unbondings!!.first()
        assertThat(unbondingEntry.amount.amount).isEqualTo(depositAmount)
        assertThat(unbondingEntry.completionEpoch.toLong()).isEqualTo(expectedCompletionEpoch)

        logHighlight("Waiting for unbonding period to pass (${unbondingPeriod + 1} epochs)")
        repeat((unbondingPeriod + 1).toInt()) {
            genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        }

        logHighlight("Verifying balance is restored and queue is empty")
        val finalBalance = participant.getBalance(participantAddress)
        
        // Calculate expected balance including any epoch rewards accumulated during unbonding
        val endLastRewardedEpoch = getRewardCalculationEpochIndex(participant)
        val participantRewards = calculateExpectedChangeFromEpochRewards(
            participant,
            participantAddress,
            startLastRewardedEpoch,
            endLastRewardedEpoch,
            failureEpoch = null  // No excluded epochs for collateral test
        )
        val expectedFinalBalance = initialBalance + participantRewards

        logHighlight("Expected final balance: $initialBalance (initial) + $participantRewards (epoch rewards) = $expectedFinalBalance")
        assertThat(finalBalance).isEqualTo(expectedFinalBalance)

        val finalUnbondingQueue = participant.node.queryUnbondingCollateral(participantAddress)
        assertThat(finalUnbondingQueue.unbondings).isNullOrEmpty()
    }

    @Test
    fun `a participant is slashed for downtime with unbonding slashed`() {
        // Configure genesis with fast expiration for downtime testing
        val fastExpirationSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::params] = spec<InferenceParams> {
                    this[InferenceParams::validationParams] = spec<ValidationParams> {
                        this[ValidationParams::expirationBlocks] = 2L // Fast expiration for testing
                    }
                }
            }
        }

        val fastExpirationConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(fastExpirationSpec) ?: fastExpirationSpec
        )

        val (cluster, genesis) = initCluster(config = fastExpirationConfig, reboot = true)
        val genesisAddress = genesis.node.getColdAddress()
        val depositAmount = 1000L

        val timeoutsAtStart = genesis.node.getInferenceTimeouts()

        logSection("Depositing $depositAmount nicoin for ${genesis.name}")
        genesis.depositCollateral(depositAmount)
        genesis.node.waitForNextBlock()

        logHighlight("Verifying initial collateral")
        val initialCollateral = genesis.queryCollateral(genesisAddress)
        assertThat(initialCollateral.amount?.amount).isEqualTo(depositAmount)

        logSection("Making good inferences")
        repeat(3) {
            runCatching { genesis.makeInferenceRequest(inferenceRequest) }
        }
        genesis.node.waitForNextBlock(1)

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        genesis.node.waitForNextBlock(2)

        // NEW: Withdraw portion of collateral to create unbonding entry
        val withdrawAmount = 400L
        val activeAmount = depositAmount - withdrawAmount
        logSection("Withdrawing $withdrawAmount nicoin to create unbonding collateral")
        genesis.withdrawCollateral(withdrawAmount)
        genesis.node.waitForNextBlock()

        logSection("Verifying pre-slash state: $activeAmount active, $withdrawAmount unbonding")
        val activeCollateralBeforeSlash = genesis.queryCollateral(genesisAddress)
        assertThat(activeCollateralBeforeSlash.amount?.amount).isEqualTo(activeAmount)
        val unbondingQueueBeforeSlash = genesis.node.queryUnbondingCollateral(genesisAddress)
        assertThat(unbondingQueueBeforeSlash.unbondings).hasSize(1)
        assertThat(unbondingQueueBeforeSlash.unbondings!!.first().amount.amount).isEqualTo(withdrawAmount)


        logSection("Getting bad inferences")
        genesis.mock!!.setInferenceResponse("This is invalid json!!!")

        logSection("Running inferences until genesis is INVALID")
        repeat(15) {
            runCatching { genesis.makeInferenceRequest(inferenceRequest) }
        }
        genesis.node.waitForNextBlock(1)
        val timeoutsBefore = genesis.node.getInferenceTimeouts()
        logSection("Total timeouts right after inference requests: ${timeoutsBefore.inferenceTimeout?.count() ?: 0}")

        val expirationBlocks = genesis.node.getInferenceParams().params.validationParams.expirationBlocks + 1
        val expirationBlock = genesis.getCurrentBlockHeight() + expirationBlocks
        logSection("Waiting for expirationBlocks: $expirationBlocks")
        genesis.node.waitForMinimumBlock(expirationBlock, "inferenceExpiration")

        val timeoutsAfter = genesis.node.getInferenceTimeouts()
        logSection("Total timeouts after expiration wait: ${timeoutsAfter.inferenceTimeout?.count() ?: 0}")
        genesis.node.waitForNextBlock()

        logSection("Waiting for slashing on downtime")
        genesis.node.waitForNextBlock(2)
        logSection("Verifying inference was processed and status updated")

        logSection("Verifying collateral has been slashed proportionally")
        val inferenceParams = genesis.node.getInferenceParams().params
        val slashFraction = inferenceParams.collateralParams.slashFractionDowntime
        
        // Verify active collateral was slashed
        val expectedSlashedActive = (activeAmount * slashFraction.toDouble()).toLong()
        val expectedFinalActive = activeAmount - expectedSlashedActive
        val finalActiveCollateral = genesis.queryCollateral(genesisAddress)
        assertThat(finalActiveCollateral.amount?.amount).isEqualTo(expectedFinalActive)

        // Verify unbonding collateral was slashed proportionally
        val expectedSlashedUnbonding = (withdrawAmount * slashFraction.toDouble()).toLong()
        val expectedFinalUnbonding = withdrawAmount - expectedSlashedUnbonding
        val finalUnbondingQueue = genesis.node.queryUnbondingCollateral(genesisAddress)
        assertThat(finalUnbondingQueue.unbondings).hasSize(1)
        assertThat(finalUnbondingQueue.unbondings!!.first().amount.amount).isEqualTo(expectedFinalUnbonding)

        logSection("Proportional slashing verified: Active ($activeAmount -> $expectedFinalActive), Unbonding ($withdrawAmount -> $expectedFinalUnbonding)")
        
        // Mark for reboot to reset parameters for subsequent tests
        genesis.markNeedsReboot()
    }

}