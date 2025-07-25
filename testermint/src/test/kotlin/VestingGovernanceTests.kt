package com.productscience

import com.productscience.EpochStage
import com.productscience.data.AppState
import com.productscience.data.InferenceState
import com.productscience.data.InferenceParams
import com.productscience.data.TokenomicsParams
import com.productscience.data.UpdateParams
import com.productscience.data.spec
import com.productscience.initCluster
import com.productscience.logSection
import com.productscience.getInferenceResult
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.condition.EnabledIfSystemProperty

class VestingGovernanceTests : TestermintTest() {

    @Test
    @Tag("exclude")
    fun `vesting parameters can be changed through governance`() {
        // Test configuration with initial fast vesting periods
        val initialVestingSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::params] = spec<InferenceParams> {
                    this[InferenceParams::tokenomicsParams] = spec<TokenomicsParams> {
                        this[TokenomicsParams::workVestingPeriod] = 2L     // Start with 2 epochs
                        this[TokenomicsParams::rewardVestingPeriod] = 2L   // Start with 2 epochs  
                        this[TokenomicsParams::topMinerVestingPeriod] = 2L // Start with 2 epochs
                    }
                }
            }
        }

        val initialVestingConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(initialVestingSpec) ?: initialVestingSpec
        )

        logSection("=== Testing Vesting Parameter Governance ===")

        val (cluster, genesis) = initCluster(config = initialVestingConfig, reboot = true)

        logSection("1. Verify Initial Vesting Parameters")
        
        // Query initial parameters to confirm they're set correctly
        val initialParams = genesis.getParams()
        assertThat(initialParams.tokenomicsParams.workVestingPeriod).isEqualTo(2L)
        assertThat(initialParams.tokenomicsParams.rewardVestingPeriod).isEqualTo(2L)  
        assertThat(initialParams.tokenomicsParams.topMinerVestingPeriod).isEqualTo(2L)
        
        logSection("2. Submit Governance Proposal to Change Vesting Periods")
        
        // Create modified parameters
        // Change WorkVestingPeriod from 2 to 5 epochs
        // Change RewardVestingPeriod from 2 to 10 epochs  
        // Change TopMinerVestingPeriod from 2 to 15 epochs
        val modifiedParams = initialParams.copy(
            tokenomicsParams = initialParams.tokenomicsParams.copy(
                workVestingPeriod = 5L,
                rewardVestingPeriod = 10L,
                topMinerVestingPeriod = 15L
            )
        )

        logSection("3. Submit and Vote on Proposal")
        genesis.runProposal(cluster, UpdateParams(params = modifiedParams))
        genesis.markNeedsReboot()
        
        logSection("4. Wait for Proposal to Pass")
        genesis.node.waitForNextBlock(5)

        logSection("5. Verify Parameters Have Been Updated")
        
        // Query updated parameters to confirm the governance change took effect
        val updatedParams = genesis.getParams()
        
        assertThat(updatedParams.tokenomicsParams.workVestingPeriod).isEqualTo(5L)
        assertThat(updatedParams.tokenomicsParams.rewardVestingPeriod).isEqualTo(10L)
        assertThat(updatedParams.tokenomicsParams.topMinerVestingPeriod).isEqualTo(15L)
        
        logSection("6. Test New Vesting Behavior")
        
        // Wait for system to be ready for inferences after governance change
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        
        // Claim some rewards to verify they use the new vesting periods  
        val participantAddress = genesis.node.getAddress()
        
        // Make an inference request and claim rewards
        val inferenceResult = getInferenceResult(genesis)
        
        // Check that new rewards are vested over the updated periods
        val vestingSchedule = genesis.node.queryVestingSchedule(participantAddress)
        
        // The vesting schedule should reflect the new vesting periods for different reward types
        logSection("New Vesting Schedule: ${vestingSchedule}")
        
        // Verify new rewards use updated vesting periods
        if (vestingSchedule.vestingSchedule?.epochAmounts?.isNotEmpty() == true) {
            val epochCount = vestingSchedule.vestingSchedule.epochAmounts.size
            logSection("Verified: New rewards vest over $epochCount epochs (updated from 2 epochs)")
            // Note: The exact epoch count depends on which reward type was earned
            // Could be 5 (work), 10 (reward), or 15 (top miner) epochs
            assertThat(epochCount).isIn(5, 10, 15)
        }
        
        logSection("=== Vesting Parameter Governance Test Completed Successfully ===")
    }
} 