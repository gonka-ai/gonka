import com.productscience.*
import com.productscience.data.spec
import com.productscience.data.AppState
import com.productscience.data.InferenceState
import com.productscience.data.InferenceParams
import com.productscience.data.BitcoinRewardParams
import com.productscience.data.EpochParams
import com.productscience.data.TokenomicsParams
import java.time.Duration
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.junit.jupiter.api.Test

class StreamVestingTests : TestermintTest() {

    @Test
    fun `comprehensive vesting test with automatic reward system detection`() {
        // Configure genesis with 2-epoch vesting periods for fast testing
        val fastVestingSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::params] = spec<InferenceParams> {
                    this[InferenceParams::tokenomicsParams] = spec<TokenomicsParams> {
                        this[TokenomicsParams::workVestingPeriod] = 2L       // 2 epochs for work coins
                        this[TokenomicsParams::rewardVestingPeriod] = 2L     // 2 epochs for reward coins
                        this[TokenomicsParams::topMinerVestingPeriod] = 2L   // 2 epochs for top miner rewards
                    }
                    this[InferenceParams::epochParams] = spec<EpochParams> {
                        this[EpochParams::epochLength] = 25L                 // This test doesn't fit in 15 blocks

                    }
                }
            }
        }

        val fastVestingConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(fastVestingSpec) ?: fastVestingSpec
        )

        val (cluster, genesis) = initCluster(config = fastVestingConfig, reboot = true)

        val params = genesis.node.getInferenceParams().params
        val isBitcoinEnabled = isBitcoinRewardsEnabled(params.bitcoinRewardParams)
        
        logSection("=== VESTING TEST WITH AUTOMATIC REWARD SYSTEM DETECTION ===")
        if (isBitcoinEnabled) {
            logHighlight("✅ Bitcoin Rewards System DETECTED - Running Bitcoin vesting test")
            logHighlight("  • Fixed epoch rewards based on PoC weight")
            logHighlight("  • No immediate RewardCoins bonuses per inference")
            logHighlight("  • Epoch rewards distributed at CLAIM_REWARDS stage")
            testBitcoinRewardSystemVesting(cluster, genesis)
        } else {
            logHighlight("✅ Legacy Rewards System DETECTED - Running legacy vesting test")
            logHighlight("  • Variable subsidies based on total network work")
            logHighlight("  • Immediate RewardCoins bonuses per inference")
            logHighlight("  • All rewards subject to vesting periods")
            testLegacyRewardSystemVesting(cluster, genesis)
        }
    }

    private fun testLegacyRewardSystemVesting(cluster: LocalCluster, genesis: LocalInferencePair) {
        val participant = genesis
        val participantAddress = participant.node.getColdAddress()

        logSection("=== LEGACY REWARD SYSTEM VESTING TEST ===")
        logHighlight("Testing comprehensive vesting with legacy variable reward system")
        logHighlight("  • Variable subsidies based on total network work")
        logHighlight("  • Immediate RewardCoins bonuses per inference")
        logHighlight("  • All rewards subject to vesting periods")
        
        logSection("Waiting for system to be ready for inferences")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)

        logSection("=== SCENARIO 1: Test Reward Vesting ===")
        logHighlight("Querying initial participant balance")
        val initialBalance = participant.getBalance(participantAddress)
        logHighlight("Initial balance: $initialBalance ngonka")

        // Query initial vesting schedule (should be empty)
        logHighlight("Querying initial vesting schedule")
        val initialVestingSchedule = participant.node.queryVestingSchedule(participantAddress)
        assertThat(initialVestingSchedule.vestingSchedule?.epochAmounts).isNullOrEmpty()

        logSection("Making 20 parallel inference requests to earn rewards")
        val futures = (1..20).map { i ->
            java.util.concurrent.CompletableFuture.supplyAsync {
                logSection("Starting inference request $i")
                getInferenceResult(participant)
            }
        }
        
        val allResults = futures.map { it.get() }
        logSection("Completed 20 inference requests")
        
        val participantInferences = allResults.filter { it.inference.assignedTo == participantAddress }
        logHighlight("Found ${participantInferences.size} inferences assigned to participant ($participantAddress)")
        
        allResults.forEachIndexed { index, result ->
            logHighlight("Inference ${index + 1}: assigned_to: ${result.inference.assignedTo}, executed_by: ${result.inference.executedBy}")
        }
        
        require(participantInferences.isNotEmpty()) { "No inference was assigned to participant $participantAddress" }
        val inferenceResult = participantInferences.first()
        logHighlight("Using inference: ${inferenceResult.inference.inferenceId}")

        logSection("Waiting for inference to be processed and rewards calculated")
        participant.waitForStage(EpochStage.CLAIM_REWARDS)
        participant.node.waitForNextBlock()

        logSection("Verifying reward vesting: balance should NOT increase immediately")
        val balanceAfterReward = participant.getBalance(participantAddress)
        logHighlight("Balance after reward: $balanceAfterReward ngonka")
        
        // Balance should not increase immediately due to vesting
        assertThat(balanceAfterReward).isLessThanOrEqualTo(initialBalance)

        logSection("Verifying vesting schedule was created correctly")
        val vestingScheduleAfterReward = participant.node.queryVestingSchedule(participantAddress)
        assertThat(vestingScheduleAfterReward.vestingSchedule?.epochAmounts).isNotEmpty()
        assertThat(vestingScheduleAfterReward.vestingSchedule?.epochAmounts).hasSize(2) // 2-epoch vesting period

        val totalVestingAmount = vestingScheduleAfterReward.vestingSchedule?.epochAmounts?.sumOf { 
            it.coins.sumOf { coin -> coin.amount } 
        } ?: 0
        logHighlight("Total amount vesting: $totalVestingAmount nicoin over 2 epochs")
        assertThat(totalVestingAmount).isGreaterThan(0)

        logSection("=== SCENARIO 2: Test Epoch Unlocking ===")
        logHighlight("Waiting for first epoch to unlock vested tokens")
        participant.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        participant.node.waitForNextBlock()

        val balanceAfterFirstEpoch = participant.getBalance(participantAddress)
        logHighlight("Balance after first epoch unlock: $balanceAfterFirstEpoch ngonka")
        // Balance should increase after first epoch unlock
        assertThat(balanceAfterFirstEpoch).isGreaterThan(balanceAfterReward)

        logHighlight("Verifying vesting schedule updated (should have 1 epoch left)")
        val vestingAfterFirstEpoch = participant.node.queryVestingSchedule(participantAddress)
        if (!vestingAfterFirstEpoch.vestingSchedule?.epochAmounts.isNullOrEmpty()) {
            assertThat(vestingAfterFirstEpoch.vestingSchedule?.epochAmounts).hasSize(1) // 1 epoch remaining
        }

        logSection("Waiting for second epoch to unlock remaining vested tokens")
        participant.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        participant.node.waitForNextBlock()

        val balanceAfterSecondEpoch = participant.getBalance(participantAddress)
        logHighlight("Balance after second epoch unlock: $balanceAfterSecondEpoch ngonka")
        // Balance should increase further after second epoch unlock
        assertThat(balanceAfterSecondEpoch).isGreaterThan(balanceAfterFirstEpoch)

        logHighlight("Verifying vesting schedule is now empty (all tokens unlocked)")
        val finalVestingSchedule = participant.node.queryVestingSchedule(participantAddress)
        assertThat(finalVestingSchedule.vestingSchedule?.epochAmounts).isNullOrEmpty()

        logSection("=== SCENARIO 3: Test Reward Aggregation ===")
        logSection("Making 20 parallel second inference requests for aggregation test")
        val secondFutures = (1..20).map { i ->
            java.util.concurrent.CompletableFuture.supplyAsync {
                logHighlight("Starting second inference request $i")
                getInferenceResult(participant)
            }
        }
        
        val secondAllResults = secondFutures.map { it.get() }
        logSection("Completed 20 second inference requests")
        
        val secondParticipantInferences = secondAllResults.filter { it.inference.assignedTo == participantAddress }
        logHighlight("Found ${secondParticipantInferences.size} second inferences assigned to participant ($participantAddress)")
        
        secondAllResults.forEachIndexed { index, result ->
            logHighlight("Second inference ${index + 1}: assigned_to: ${result.inference.assignedTo}, executed_by: ${result.inference.executedBy}")
        }
        
        require(secondParticipantInferences.isNotEmpty()) { "No second inference was assigned to participant $participantAddress" }
        val secondInferenceResult = secondParticipantInferences.first()
        logHighlight("Using second inference: ${secondInferenceResult.inference.inferenceId}")

        logSection("Waiting for second reward to be processed")
        participant.waitForStage(EpochStage.CLAIM_REWARDS) 
        participant.node.waitForNextBlock()

        val balanceBeforeAggregation = participant.getBalance(participantAddress)
        logHighlight("Balance before aggregation test: $balanceBeforeAggregation ngonka")

        logSection("Making 20 parallel third inference requests to test aggregation")
        val thirdFutures = (1..20).map { i ->
            java.util.concurrent.CompletableFuture.supplyAsync {
                logSection("Starting third inference request $i")
                getInferenceResult(participant)
            }
        }
        
        val thirdAllResults = thirdFutures.map { it.get() }
        logHighlight("Completed 20 third inference requests")
        
        val thirdParticipantInferences = thirdAllResults.filter { it.inference.assignedTo == participantAddress }
        logHighlight("Found ${thirdParticipantInferences.size} third inferences assigned to participant ($participantAddress)")
        
        thirdAllResults.forEachIndexed { index, result ->
            logHighlight("Third inference ${index + 1}: assigned_to: ${result.inference.assignedTo}, executed_by: ${result.inference.executedBy}")
        }
        
        require(thirdParticipantInferences.isNotEmpty()) { "No third inference was assigned to participant $participantAddress" }
        val thirdInferenceResult = thirdParticipantInferences.first()
        logHighlight("Using third inference: ${thirdInferenceResult.inference.inferenceId}")

        logSection("Waiting for third reward to be processed and aggregated")
        participant.waitForStage(EpochStage.CLAIM_REWARDS)
        participant.node.waitForNextBlock()

        logSection("Verifying reward aggregation: should still be 2-epoch schedule")
        val aggregatedVestingSchedule = participant.node.queryVestingSchedule(participantAddress)
        assertThat(aggregatedVestingSchedule.vestingSchedule?.epochAmounts).isNotEmpty()
        assertThat(aggregatedVestingSchedule.vestingSchedule?.epochAmounts).hasSize(2) // Still 2 epochs, not extended

        val aggregatedTotalAmount = aggregatedVestingSchedule.vestingSchedule?.epochAmounts?.sumOf { 
            it.coins.sumOf { coin -> coin.amount } 
        } ?: 0
        logHighlight("Total aggregated vesting amount: $aggregatedTotalAmount ngonka")
        
        // The aggregated amount should be greater than a single reward
        // TODO: unfortunatelly, it's not true, because we can't guarantee that the rewards are equal each time to the same validator
        // assertThat(aggregatedTotalAmount).isGreaterThan(totalVestingAmount)

        logSection("=== LEGACY REWARD SYSTEM VESTING TEST COMPLETED ===")
        logHighlight("All scenarios verified for legacy variable reward system:")
        logHighlight("✅ Reward vesting - rewards vest over 2 epochs instead of immediate payment")
        logHighlight("✅ Epoch unlocking - tokens unlock progressively over 2 epochs")
        logHighlight("✅ Reward aggregation - multiple rewards aggregate into same 2-epoch schedule")
        logHighlight("✅ Legacy system compatibility - immediate RewardCoins bonuses work with vesting")
    }

    private fun testBitcoinRewardSystemVesting(cluster: LocalCluster, genesis: LocalInferencePair) {
        val participant = genesis
        val participantAddress = participant.node.getColdAddress()

        logSection("=== BITCOIN REWARD SYSTEM VESTING TEST ===")
        logSection("Testing vesting aggregation with Bitcoin-style fixed reward system")
        logSection("  • Fixed epoch rewards based on PoC weight")
        logSection("  • No immediate RewardCoins bonuses per inference")
        logSection("  • Epoch rewards distributed at CLAIM_REWARDS stage")
        logSection("  • All rewards subject to vesting periods")
        
        logSection("Waiting for system to be ready for inferences")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)

        logSection("=== SCENARIO 1: Test Reward Vesting Aggregation ===")
        logSection("Querying initial participant state")
        val initialBalance = participant.getBalance(participantAddress)
        val startLastRewardedEpoch = getRewardCalculationEpochIndex(participant)
        
        // Query initial vesting schedule (may have existing vesting from epoch rewards)
        val initialVestingSchedule = participant.node.queryVestingSchedule(participantAddress)
        val initialTotalVesting = initialVestingSchedule.vestingSchedule?.epochAmounts?.sumOf { 
            it.coins.sumOf { coin -> coin.amount } 
        } ?: 0
        // Only the first epoch should unlock during this claim rewards cycle
        val initialFirstEpochUnlock = initialVestingSchedule.vestingSchedule?.epochAmounts?.firstOrNull()?.coins?.sumOf { coin -> coin.amount } ?: 0
        val initialSecondEpoch = initialVestingSchedule.vestingSchedule?.epochAmounts?.getOrNull(1)?.coins?.sumOf { coin -> coin.amount } ?: 0
        logSection("Initial balance: $initialBalance nicoin, start epoch: $startLastRewardedEpoch")
        logSection("Initial vesting - Total: $initialTotalVesting, First unlock: $initialFirstEpochUnlock, Second remaining: $initialSecondEpoch")

        logSection("Making 20 parallel inference requests to earn rewards")
        val futures = (1..20).map { i ->
            java.util.concurrent.CompletableFuture.supplyAsync {
                logSection("Starting inference request $i")
                getInferenceResult(participant)
            }
        }
        
        val allResults = futures.map { it.get() }
        logSection("Completed 20 inference requests")
        
        val participantInferences = allResults.filter { it.inference.assignedTo == participantAddress }
        logSection("Found ${participantInferences.size} inferences assigned to participant ($participantAddress)")
        
        allResults.forEachIndexed { index, result ->
            logSection("Inference ${index + 1}: assigned_to: ${result.inference.assignedTo}, executed_by: ${result.inference.executedBy}")
        }
        
        require(participantInferences.isNotEmpty()) { "No inference was assigned to participant $participantAddress" }

        logSection("Waiting for next claim reward cycle to process rewards and vesting")
        participant.waitForStage(EpochStage.CLAIM_REWARDS)
        participant.node.waitForNextBlock()

        // Re-query all inferences to get their final settled status and accurate costs
        logSection("Re-querying all inferences to get final settled status")
        val settledInferences = allResults.map { participant.api.getInference(it.inference.inferenceId) }
        logSection("Settled ${settledInferences.size} inferences with updated status and costs")

        // Calculate expected vesting schedule from this reward cycle
        val inferencePayloads = settledInferences  // Use fresh settled data instead of original
        val participants = participant.api.getParticipants()
        val currentLastRewardedEpoch = getRewardCalculationEpochIndex(participant)
        
        val expectedVestingSchedule = calculateVestingScheduleChanges(
            inferencePayloads,
            participant.node.getInferenceParams().params,
            participants,
            startLastRewardedEpoch,
            currentLastRewardedEpoch,
            vestingPeriod = 2  // 2-epoch vesting period
        )
        val expectedSchedule = expectedVestingSchedule[participantAddress] ?: LongArray(3) { 0L }
        val expectedCosts = expectedSchedule[0]  // Immediate costs (negative)
        val expectedFirstVesting = expectedSchedule[1]  // First epoch from new rewards
        val expectedSecondVesting = expectedSchedule[2]  // Second epoch from new rewards
        val expectedNewVesting = expectedFirstVesting + expectedSecondVesting  // Total new vesting

        logSection("Verifying balance and vesting changes after claim rewards")
        val balanceAfterReward = participant.getBalance(participantAddress)
        logSection("Balance after reward: $balanceAfterReward ngonka")
        
        // Balance change = -inference_costs + unlocked_initial_vesting (first epoch only)
        val actualBalanceChange = balanceAfterReward - initialBalance
        logSection("Actual balance change: $actualBalanceChange ngonka")
        logSection("Expected components: ${expectedCosts} (costs) + $initialFirstEpochUnlock (first epoch unlock)")
        val expectedBalanceChange = expectedCosts + initialFirstEpochUnlock  // expectedCosts is negative
        
        // Balance should change by costs paid plus any initial vesting unlocked
        assertThat(actualBalanceChange).isCloseTo(expectedBalanceChange, Offset.offset(1L))

        logSection("Verifying new vesting schedule aggregation")
        val vestingScheduleAfterReward = participant.node.queryVestingSchedule(participantAddress)
        val newFirstEpoch = vestingScheduleAfterReward.vestingSchedule?.epochAmounts?.getOrNull(0)?.coins?.sumOf { coin -> coin.amount } ?: 0
        val newSecondEpoch = vestingScheduleAfterReward.vestingSchedule?.epochAmounts?.getOrNull(1)?.coins?.sumOf { coin -> coin.amount } ?: 0
        val newTotalVesting = newFirstEpoch + newSecondEpoch
        
        logSection("New vesting structure:")
        logSection("  First epoch: $newFirstEpoch ngonka")
        logSection("  Second epoch: $newSecondEpoch ngonka")
        logSection("  Total: $newTotalVesting ngonka")
        
        // Expected aggregation:
        // First epoch = initial second epoch + new first epoch
        // Second epoch = new second epoch
        val expectedNewFirstEpoch = initialSecondEpoch + expectedFirstVesting
        val expectedNewSecondEpoch = expectedSecondVesting
        
        logSection("Expected vesting structure:")
        logSection("  First epoch: $expectedNewFirstEpoch nicoin (initial second: $initialSecondEpoch + new first: $expectedFirstVesting)")
        logSection("  Second epoch: $expectedNewSecondEpoch nicoin (new second: $expectedSecondVesting)")
        
        // Verify epoch-by-epoch aggregation
        assertThat(newFirstEpoch).isCloseTo(expectedNewFirstEpoch, Offset.offset(1L))
        assertThat(newSecondEpoch).isCloseTo(expectedNewSecondEpoch, Offset.offset(1L))

        logSection("=== BITCOIN REWARD SYSTEM VESTING TEST COMPLETED ===")
        logSection("✅ Balance changed correctly: paid costs + unlocked first epoch of initial vesting")
        logSection("✅ Vesting structure aggregated correctly:")
        logSection("    • First epoch = initial second epoch + new first epoch")
        logSection("    • Second epoch = new second epoch")
        logSection("✅ Initial first epoch was properly unlocked during claim rewards cycle")
        logSection("✅ Bitcoin system compatibility: epoch rewards work correctly with vesting aggregation")
        logSection("✅ Fixed reward distribution: Bitcoin-style epoch rewards properly vest over 2 epochs")
    }
    
} 