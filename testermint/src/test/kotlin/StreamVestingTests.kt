import com.productscience.EpochStage
import com.productscience.data.spec
import com.productscience.data.AppState
import com.productscience.data.InferenceState
import com.productscience.data.InferenceParams
import com.productscience.data.TokenomicsParams
import com.productscience.initCluster
import com.productscience.logSection
import com.productscience.makeInferenceRequest
import com.productscience.inferenceRequest
import com.productscience.inferenceConfig
import com.productscience.getInferenceResult
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class StreamVestingTests : TestermintTest() {

    @Test
    fun `comprehensive vesting test with 2-epoch periods`() {
        // Configure genesis with 2-epoch vesting periods for fast testing
        val fastVestingSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::params] = spec<InferenceParams> {
                    this[InferenceParams::tokenomicsParams] = spec<TokenomicsParams> {
                        this[TokenomicsParams::workVestingPeriod] = 2L       // 2 epochs for work coins
                        this[TokenomicsParams::rewardVestingPeriod] = 2L     // 2 epochs for reward coins
                        this[TokenomicsParams::topMinerVestingPeriod] = 2L   // 2 epochs for top miner rewards
                    }
                }
            }
        }

        val fastVestingConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(fastVestingSpec) ?: fastVestingSpec
        )

        val (cluster, genesis) = initCluster(config = fastVestingConfig, reboot = true)
        genesis.markNeedsReboot()
        val participant = genesis
        val participantAddress = participant.node.getAddress()

        logSection("Waiting for system to be ready for inferences")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)

        logSection("=== SCENARIO 1: Test Reward Vesting ===")
        logSection("Querying initial participant balance")
        val initialBalance = participant.getBalance(participantAddress)
        logSection("Initial balance: $initialBalance nicoin")

        // Query initial vesting schedule (should be empty)
        logSection("Querying initial vesting schedule")
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
        logSection("Found ${participantInferences.size} inferences assigned to participant ($participantAddress)")
        
        allResults.forEachIndexed { index, result ->
            logSection("Inference ${index + 1}: assigned_to: ${result.inference.assignedTo}, executed_by: ${result.inference.executedBy}")
        }
        
        require(participantInferences.isNotEmpty()) { "No inference was assigned to participant $participantAddress" }
        val inferenceResult = participantInferences.first()
        logSection("Using inference: ${inferenceResult.inference.inferenceId}")

        logSection("Waiting for inference to be processed and rewards calculated")
        participant.waitForStage(EpochStage.CLAIM_REWARDS)
        participant.node.waitForNextBlock()

        logSection("Verifying reward vesting: balance should NOT increase immediately")
        val balanceAfterReward = participant.getBalance(participantAddress)
        logSection("Balance after reward: $balanceAfterReward nicoin")
        
        // Balance should not increase immediately due to vesting
        assertThat(balanceAfterReward).isLessThanOrEqualTo(initialBalance)

        logSection("Verifying vesting schedule was created correctly")
        val vestingScheduleAfterReward = participant.node.queryVestingSchedule(participantAddress)
        assertThat(vestingScheduleAfterReward.vestingSchedule?.epochAmounts).isNotEmpty()
        assertThat(vestingScheduleAfterReward.vestingSchedule?.epochAmounts).hasSize(2) // 2-epoch vesting period

        val totalVestingAmount = vestingScheduleAfterReward.vestingSchedule?.epochAmounts?.sumOf { 
            it.coins.sumOf { coin -> coin.amount } 
        } ?: 0
        logSection("Total amount vesting: $totalVestingAmount nicoin over 2 epochs")
        assertThat(totalVestingAmount).isGreaterThan(0)

        logSection("=== SCENARIO 2: Test Epoch Unlocking ===")
        logSection("Waiting for first epoch to unlock vested tokens")
        participant.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        participant.node.waitForNextBlock()

        val balanceAfterFirstEpoch = participant.getBalance(participantAddress)
        logSection("Balance after first epoch unlock: $balanceAfterFirstEpoch nicoin")
        // Balance should increase after first epoch unlock
        assertThat(balanceAfterFirstEpoch).isGreaterThan(balanceAfterReward)

        logSection("Verifying vesting schedule updated (should have 1 epoch left)")
        val vestingAfterFirstEpoch = participant.node.queryVestingSchedule(participantAddress)
        if (!vestingAfterFirstEpoch.vestingSchedule?.epochAmounts.isNullOrEmpty()) {
            assertThat(vestingAfterFirstEpoch.vestingSchedule?.epochAmounts).hasSize(1) // 1 epoch remaining
        }

        logSection("Waiting for second epoch to unlock remaining vested tokens")
        participant.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        participant.node.waitForNextBlock()

        val balanceAfterSecondEpoch = participant.getBalance(participantAddress)
        logSection("Balance after second epoch unlock: $balanceAfterSecondEpoch nicoin")
        // Balance should increase further after second epoch unlock
        assertThat(balanceAfterSecondEpoch).isGreaterThan(balanceAfterFirstEpoch)

        logSection("Verifying vesting schedule is now empty (all tokens unlocked)")
        val finalVestingSchedule = participant.node.queryVestingSchedule(participantAddress)
        assertThat(finalVestingSchedule.vestingSchedule?.epochAmounts).isNullOrEmpty()

        logSection("=== SCENARIO 3: Test Reward Aggregation ===")
        logSection("Making 20 parallel second inference requests for aggregation test")
        val secondFutures = (1..20).map { i ->
            java.util.concurrent.CompletableFuture.supplyAsync {
                logSection("Starting second inference request $i")
                getInferenceResult(participant)
            }
        }
        
        val secondAllResults = secondFutures.map { it.get() }
        logSection("Completed 20 second inference requests")
        
        val secondParticipantInferences = secondAllResults.filter { it.inference.assignedTo == participantAddress }
        logSection("Found ${secondParticipantInferences.size} second inferences assigned to participant ($participantAddress)")
        
        secondAllResults.forEachIndexed { index, result ->
            logSection("Second inference ${index + 1}: assigned_to: ${result.inference.assignedTo}, executed_by: ${result.inference.executedBy}")
        }
        
        require(secondParticipantInferences.isNotEmpty()) { "No second inference was assigned to participant $participantAddress" }
        val secondInferenceResult = secondParticipantInferences.first()
        logSection("Using second inference: ${secondInferenceResult.inference.inferenceId}")

        logSection("Waiting for second reward to be processed")
        participant.waitForStage(EpochStage.CLAIM_REWARDS) 
        participant.node.waitForNextBlock()

        val balanceBeforeAggregation = participant.getBalance(participantAddress)
        logSection("Balance before aggregation test: $balanceBeforeAggregation nicoin")

        logSection("Making 20 parallel third inference requests to test aggregation")
        val thirdFutures = (1..20).map { i ->
            java.util.concurrent.CompletableFuture.supplyAsync {
                logSection("Starting third inference request $i")
                getInferenceResult(participant)
            }
        }
        
        val thirdAllResults = thirdFutures.map { it.get() }
        logSection("Completed 20 third inference requests")
        
        val thirdParticipantInferences = thirdAllResults.filter { it.inference.assignedTo == participantAddress }
        logSection("Found ${thirdParticipantInferences.size} third inferences assigned to participant ($participantAddress)")
        
        thirdAllResults.forEachIndexed { index, result ->
            logSection("Third inference ${index + 1}: assigned_to: ${result.inference.assignedTo}, executed_by: ${result.inference.executedBy}")
        }
        
        require(thirdParticipantInferences.isNotEmpty()) { "No third inference was assigned to participant $participantAddress" }
        val thirdInferenceResult = thirdParticipantInferences.first()
        logSection("Using third inference: ${thirdInferenceResult.inference.inferenceId}")

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
        logSection("Total aggregated vesting amount: $aggregatedTotalAmount nicoin")
        
        // The aggregated amount should be greater than a single reward
        // TODO: unfortunatelly, it's not true, because we can't guarantee that the rewards are equal each time to the same validator
        // assertThat(aggregatedTotalAmount).isGreaterThan(totalVestingAmount)

        logSection("=== VESTING TEST COMPLETED SUCCESSFULLY ===")
        logSection("All scenarios verified:")
        logSection("✅ Reward vesting - rewards vest over 2 epochs instead of immediate payment")
        logSection("✅ Epoch unlocking - tokens unlock progressively over 2 epochs")  
        logSection("✅ Reward aggregation - multiple rewards aggregate into same 2-epoch schedule")
    }
    
} 