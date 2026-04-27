import com.productscience.*
import com.productscience.data.Collateral
import com.productscience.data.TxResponse
import com.productscience.data.spec
import com.productscience.data.AppState
import com.productscience.data.Decimal
import com.productscience.data.InferenceState
import com.productscience.data.InferenceParams
import com.productscience.data.ValidationParams
import com.productscience.data.getParticipant
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.junit.jupiter.api.Test
import java.time.Duration

class CollateralTests : TestermintTest() {

    private data class CollateralSlashExpectation(
        val slashCount: Int,
        val activeAmount: Long,
        val unbondingAmount: Long,
    )

    private fun expectedCollateralAfterDowntimeSlashes(
        activeAmount: Long,
        unbondingAmount: Long,
        slashFraction: Double,
        maxSlashCount: Int,
    ): List<CollateralSlashExpectation> {
        var currentActive = activeAmount
        var currentUnbonding = unbondingAmount

        return (1..maxSlashCount).map { slashCount ->
            currentActive -= (currentActive * slashFraction).toLong()
            currentUnbonding -= (currentUnbonding * slashFraction).toLong()

            CollateralSlashExpectation(
                slashCount = slashCount,
                activeAmount = currentActive,
                unbondingAmount = currentUnbonding,
            )
        }
    }

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
        participant.node.waitForNextBlock(2)

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
        participant.node.waitForNextBlock(2)

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
        assertThat(finalBalance).isCloseTo(expectedFinalBalance, Offset.offset(3L))

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
                            this[ValidationParams::downtimeHThreshold] = Decimal.fromDouble(1.0)
                        }
                    }
                }
            }

        val fastExpirationConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(fastExpirationSpec) ?: fastExpirationSpec
        )

        val (cluster, genesis) = initCluster(joinCount = 0, config = fastExpirationConfig, reboot = true)
        val genesisAddress = genesis.node.getColdAddress()
        val depositAmount = 1000L

        logSection("Depositing $depositAmount nicoin for ${genesis.name}")
        genesis.depositCollateral(depositAmount)
        genesis.node.waitForNextBlock(2)

        logHighlight("Verifying initial collateral")
        val initialCollateral = genesis.queryCollateral(genesisAddress)
        assertThat(initialCollateral.amount?.amount).isEqualTo(depositAmount)

        logSection("Making good inferences")
        genesis.waitForNextInferenceWindow(windowSizeInBlocks = 15)
        var successfulGoodInferences = 0
        repeat(3) {
            val result = runCatching { genesis.makeInferenceRequest(inferenceRequest) }
            if (result.isSuccess) {
                successfulGoodInferences++
            }
        }
        logSection("Warm-up good inferences succeeded: $successfulGoodInferences/3")
        genesis.node.waitForNextBlock(1)

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        genesis.node.waitForNextBlock(2)

        // NEW: Withdraw portion of collateral to create unbonding entry
        val withdrawAmount = 400L
        val activeAmount = depositAmount - withdrawAmount
        logSection("Withdrawing $withdrawAmount nicoin to create unbonding collateral")
        genesis.withdrawCollateral(withdrawAmount)
        genesis.node.waitForNextBlock(2)

        logSection("Verifying pre-slash state: $activeAmount active, $withdrawAmount unbonding")
        val activeCollateralBeforeSlash = genesis.queryCollateral(genesisAddress)
        assertThat(activeCollateralBeforeSlash.amount?.amount).isEqualTo(activeAmount)
        val unbondingQueueBeforeSlash = genesis.node.queryUnbondingCollateral(genesisAddress)
        assertThat(unbondingQueueBeforeSlash.unbondings).hasSize(1)
        assertThat(unbondingQueueBeforeSlash.unbondings!!.first().amount.amount).isEqualTo(withdrawAmount)

        logSection("Getting bad inferences")
        genesis.mock!!.setInferenceResponse("This is invalid json!!!")

        logSection("Running inferences until genesis is INACTIVE and downtime slash applies")
        val expirationBlocks = genesis.node.getInferenceParams().params.validationParams.expirationBlocks + 1
        var genesisStatus = genesis.node.getRawParticipants().getParticipant(genesis)?.status
        val maxBadInferenceBatches = 12
        repeat(maxBadInferenceBatches) { batch ->
            if (genesisStatus == "INACTIVE") return@repeat

            // Expiry during PoC uses preserve-node eligibility and skips downtime penalties.
            // Wait until we're safely in the inference window so expiries are counted as missed work.
            genesis.waitForNextInferenceWindow(windowSizeInBlocks = expirationBlocks.toInt() + 5)

            logHighlight("Submitting bad inference batch ${batch + 1}")
            val timeoutIdsBeforeBatch = genesis.node.getInferenceTimeouts()
                .inferenceTimeout
                .map { it.inferenceId }
                .toSet()
            repeat(6) {
                runCatching { genesis.makeInferenceRequest(inferenceRequest) }
            }

            genesis.node.waitForNextBlock(1)
            genesis.waitForBlock(maxBlocks = expirationBlocks.toInt() + 10) { pair ->
                pair.node.getInferenceTimeouts()
                    .inferenceTimeout
                    .any { it.inferenceId !in timeoutIdsBeforeBatch }
            }
            val newTimeouts = genesis.node.getInferenceTimeouts()
                .inferenceTimeout
                .filterNot { it.inferenceId in timeoutIdsBeforeBatch }
            assertThat(newTimeouts)
                .describedAs("Batch ${batch + 1} should create at least one inference timeout")
                .isNotEmpty()
            val expirationBlock = newTimeouts.maxOf { it.expirationHeight.toLong() } + 1
            logSection(
                "Batch ${batch + 1} created ${newTimeouts.size} new timeout(s); " +
                    "waiting for reported expiration height $expirationBlock"
            )
            genesis.node.waitForState(
                description = "inferenceExpiration:block height $expirationBlock",
                staleTimeout = Duration.ofMinutes(2),
            ) { it.syncInfo.latestBlockHeight >= expirationBlock }
            genesis.node.waitForNextBlock(3)

            val timeoutsAfter = genesis.node.getInferenceTimeouts()
            genesisStatus = genesis.node.getRawParticipants().getParticipant(genesis)?.status
            logSection("After batch ${batch + 1}: status=$genesisStatus, total timeouts=${timeoutsAfter.inferenceTimeout?.count() ?: 0}")
        }

        assertThat(genesisStatus).isEqualTo("INACTIVE")

        logSection("Verifying collateral has been slashed proportionally")
        val inferenceParams = genesis.node.getInferenceParams().params
        val slashFraction = inferenceParams.collateralParams.slashFractionDowntime
        val expectedOutcomes = expectedCollateralAfterDowntimeSlashes(
            activeAmount = activeAmount,
            unbondingAmount = withdrawAmount,
            slashFraction = slashFraction.toDouble(),
            maxSlashCount = maxBadInferenceBatches,
        )

        genesis.waitForBlock(maxBlocks = 10) { pair ->
            val currentActive = pair.queryCollateral(genesisAddress).amount?.amount
            val currentUnbonding = pair.node.queryUnbondingCollateral(genesisAddress)
                .unbondings
                ?.firstOrNull()
                ?.amount
                ?.amount

            expectedOutcomes.any {
                currentActive == it.activeAmount && currentUnbonding == it.unbondingAmount
            }
        }

        val finalActiveCollateral = genesis.queryCollateral(genesisAddress)
        val finalUnbondingQueue = genesis.node.queryUnbondingCollateral(genesisAddress)
        assertThat(finalUnbondingQueue.unbondings).hasSize(1)
        val finalActiveAmount = finalActiveCollateral.amount?.amount
        val finalUnbondingAmount = finalUnbondingQueue.unbondings!!.first().amount.amount

        val matchedExpectation = expectedOutcomes.firstOrNull {
            finalActiveAmount == it.activeAmount && finalUnbondingAmount == it.unbondingAmount
        }

        assertThat(matchedExpectation)
            .describedAs("Collateral should be slashed proportionally for the same number of downtime slashes")
            .isNotNull()

        logSection(
            "Proportional slashing verified after ${matchedExpectation!!.slashCount} downtime slashes: " +
                "Active ($activeAmount -> ${matchedExpectation.activeAmount}), " +
                "Unbonding ($withdrawAmount -> ${matchedExpectation.unbondingAmount})"
        )
        
        // Mark for reboot to reset parameters for subsequent tests
        genesis.markNeedsReboot()
    }

}
