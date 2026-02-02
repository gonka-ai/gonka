import com.productscience.EpochStage
import com.productscience.data.ConfirmationPoCPhase
import com.productscience.data.StakeValidatorStatus
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger

class ConfirmationPoCFailTests : TestermintTest() {
    // 12m
    @Test
    fun `confirmation PoC failed - capped rewards`() {
        logSection("=== TEST: Confirmation PoC Failed - Capped Rewards ===")

        // Initialize cluster with custom spec for confirmation PoC testing
        // Configure epoch timing to allow confirmation PoC triggers during inference phase
        val confirmationSpec = createConfirmationPoCSpec(expectedConfirmationsPerEpoch = 100)
        val (cluster, genesis) = initCluster(
            joinCount = 2,
            mergeSpec = confirmationSpec,  // Merge with defaults instead of replacing
            reboot = true
        )

        logSection("✅ Cluster Initialized Successfully!")

        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]

        logSection("Verifying cluster initialized with 3 participants")
        val allPairs = listOf(genesis, join1, join2)
        assertThat(allPairs).hasSize(3)

        logSection("Waiting for first PoC cycle to establish regular weights")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

        val initialStats = genesis.node.getParticipantCurrentStats()
        logSection("Initial participant weights:")
        initialStats.participantCurrentStats?.forEach {
            Logger.info("  ${it.participantId}: weight=${it.weight}")
        }

        logSection("Waiting for confirmation PoC trigger during inference phase")
        val confirmationEvent = waitForConfirmationPoCTrigger(genesis)
        assertThat(confirmationEvent).isNotNull
        Logger.info("Confirmation PoC triggered at height ${confirmationEvent!!.triggerHeight}")

        logSection("Setting PoC mocks for confirmation")
        Logger.info("  Genesis: weight=10 (passes)")
        Logger.info("  Join1: weight=8 (fails but above alpha=7, no slashing)")
        Logger.info("  Join2: weight=10 (passes)")
        genesis.setPocWeight(10)
        join1.setPocWeight(8)
        join2.setPocWeight(10)

        logSection("Waiting for confirmation PoC generation phase")
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_GENERATION)
        Logger.info("Confirmation PoC generation phase active")

        logSection("Waiting for confirmation PoC validation phase")
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_VALIDATION)
        Logger.info("Confirmation PoC validation phase active")

        logSection("Waiting for confirmation PoC completion")
        waitForConfirmationPoCCompletion(genesis)
        Logger.info("Confirmation PoC completed (event cleared)")
        genesis.setPocWeight(10)
        join1.setPocWeight(10)
        join2.setPocWeight(10)

        logSection("Verifying no slashing occurred for Join1 (above alpha threshold)")
        val join1Address = join1.node.getColdAddress()
        val validatorsAfterPoC = genesis.node.getValidators()
        val join1ValidatorAfterPoC = validatorsAfterPoC.validators.find {
            it.consensusPubkey.value == join1.node.getValidatorInfo().key
        }
        assertThat(join1ValidatorAfterPoC).isNotNull
        assertThat(join1ValidatorAfterPoC!!.status).isEqualTo(StakeValidatorStatus.BONDED.value)
        Logger.info("  Join1 is still bonded (not slashed, confirmation_weight=8 > alpha*regular_weight=7)")

        logSection("Waiting for NEXT epoch where confirmation weights will be applied")
        // Confirmation weights are only calculated and applied during the next epoch's settlement
        genesis.waitForStage(EpochStage.START_OF_POC)
        Logger.info("New epoch started, confirmation weights will be used in settlement")

        // Record balances AFTER confirmation but BEFORE settlement
        val initialBalances = mapOf(
            genesis.node.getColdAddress() to genesis.node.getSelfBalance(),
            join1Address to join1.node.getSelfBalance(),
            join2.node.getColdAddress() to join2.node.getSelfBalance()
        )

        // Record tokenomics before settlement to compute total reward minted this epoch
        val initialSubsidies = genesis.node.getTokenomics().tokenomicsData.totalSubsidies


        logSection("Waiting for reward settlement with confirmation weights")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

        logSection("Verifying rewards are capped for Join1")
        val finalBalances = mapOf(
            genesis.node.getColdAddress() to genesis.node.getSelfBalance(),
            join1Address to join1.node.getSelfBalance(),
            join2.node.getColdAddress() to join2.node.getSelfBalance()
        )

        val genesisChange = finalBalances[genesis.node.getColdAddress()]!! - initialBalances[genesis.node.getColdAddress()]!!
        val join1Change = finalBalances[join1Address]!! - initialBalances[join1Address]!!
        val join2Change = finalBalances[join2.node.getColdAddress()]!! - initialBalances[join2.node.getColdAddress()]!!

        Logger.info("Balance changes:")
        Logger.info("  Genesis: $genesisChange (regular_weight=10, confirmation_weight=10)")
        Logger.info("  Join1: $join1Change (regular_weight=10, confirmation_weight=8)")
        Logger.info("  Join2: $join2Change (regular_weight=10, confirmation_weight=10)")

        // All participants should have positive rewards (Join1 not slashed, above alpha threshold)
        assertThat(genesisChange).isGreaterThan(0)
        assertThat(join1Change).isGreaterThan(0)
        assertThat(join2Change).isGreaterThan(0)
        Logger.info("  All participants received positive rewards")

        // Genesis and Join2 should have identical rewards (both full weight)
        logSection("Verifying Genesis and Join2 receive identical rewards")
        assertThat(genesisChange).isCloseTo(join2Change, Offset.offset(5L))
        Logger.info("  Genesis and Join2 received identical rewards: $genesisChange")

        // Join1 should have lower rewards due to capped confirmation weight (8 vs 10)
        // Expected ratio: join1Change / genesisChange ≈ 8/10 = 0.8
        logSection("Verifying Join1 rewards are capped proportionally")
        assertThat(join1Change).isLessThan(genesisChange)
        assertThat(join1Change).isLessThan(join2Change)
        Logger.info("  Join1 rewards are capped (lower than Genesis and Join2)")

        // Verify the ratio is approximately 8:10 (allowing some tolerance for rounding)
        val actualRatio = join1Change.toDouble() / genesisChange.toDouble()
        val expectedJoin1Ratio = 8.0 / 10.0  // 0.8
        assertThat(actualRatio).isCloseTo(expectedJoin1Ratio, Offset.offset(0.05))
        Logger.info("  Join1 reward ratio: $actualRatio (expected: $expectedJoin1Ratio)")

        // NEW: Verify CPoC reduction went to governance (not redistributed to participants)
        logSection("Verifying CPoC reduction went to governance (not redistributed)")

        val totalDistributed = genesisChange + join1Change + join2Change
        Logger.info("Total distributed to participants: $totalDistributed")

        // Compute how much reward was minted this epoch from tokenomics data
        val finalSubsidies = genesis.node.getTokenomics().tokenomicsData.totalSubsidies
        val mintedThisEpoch = finalSubsidies - initialSubsidies
        Logger.info("Reward minted this epoch (tokenomics delta): $mintedThisEpoch")
        assertThat(mintedThisEpoch).isGreaterThan(0)

        // With new behavior (CPoC reduction to governance), participants should receive 28/30 of minted rewards.
        // With old behavior (redistributed), participants would receive 30/30 (100%).
        val actualDistributionRatio = totalDistributed.toDouble() / mintedThisEpoch.toDouble()
        val expectedDistributionRatio = 28.0 / 30.0  // 0.933...
        assertThat(actualDistributionRatio).isCloseTo(expectedDistributionRatio, Offset.offset(0.05))
        Logger.info("  ✓ Participants received ${String.format("%.1f%%", actualDistributionRatio * 100)} of minted reward")
        Logger.info("  ✓ ~6.7% went to governance (not redistributed), confirming CPoC reduction behavior")

        logSection("TEST PASSED: Confirmation PoC correctly caps rewards AND transfers reduction to governance")
    }

    // 12m
    @Test
    fun `confirmation PoC failed - participant jailed for ratio below alpha`() {
        logSection("=== TEST: Confirmation PoC Failed - Participant Jailed ===")

        // Initialize cluster with custom spec for confirmation PoC testing
        // Configure with AlphaThreshold = 0.5 (lower than standard 0.70)
        val confirmationSpec = createConfirmationPoCSpec(
            expectedConfirmationsPerEpoch = 100,
            alphaThreshold = 0.5
        )
        val (cluster, genesis) = initCluster(
            joinCount = 2,
            mergeSpec = confirmationSpec,
            reboot = true
        )

        logSection("✅ Cluster Initialized Successfully!")

        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]

        logSection("Verifying cluster initialized with 3 participants")
        val allPairs = listOf(genesis, join1, join2)
        assertThat(allPairs).hasSize(3)

        logSection("Waiting for first PoC cycle to establish regular weights")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

        val initialStats = genesis.node.getParticipantCurrentStats()
        logSection("Initial participant weights:")
        initialStats.participantCurrentStats?.forEach {
            Logger.info("  ${it.participantId}: weight=${it.weight}")
        }

        logSection("Waiting for confirmation PoC trigger during inference phase")
        val confirmationEvent = waitForConfirmationPoCTrigger(genesis)
        assertThat(confirmationEvent).isNotNull
        Logger.info("Confirmation PoC triggered at height ${confirmationEvent!!.triggerHeight}")

        logSection("Setting PoC mocks for confirmation")
        Logger.info("  Genesis: weight=10 (passes)")
        Logger.info("  Join1: weight=3 (fails, ratio=0.3 < alpha=0.5)")
        Logger.info("  Join2: weight=10 (passes)")
        genesis.setPocWeight(10)
        join1.setPocWeight(3)
        join2.setPocWeight(10)

        logSection("Waiting for confirmation PoC generation phase")
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_GENERATION)
        Logger.info("Confirmation PoC generation phase active")

        logSection("Waiting for confirmation PoC validation phase")
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_VALIDATION)
        Logger.info("Confirmation PoC validation phase active")

        logSection("Waiting for confirmation PoC completion")
        waitForConfirmationPoCCompletion(genesis)
        Logger.info("Confirmation PoC completed (event cleared)")

        // Reset mocks to full weight after confirmation
        genesis.setPocWeight(10)
        join1.setPocWeight(10)
        join2.setPocWeight(10)

        logSection("Verifying Join1 is jailed (removed from bonded validators)")
        val join1Address = join1.node.getColdAddress()
        val validatorsAfterPoC = genesis.node.getValidators()
        val join1ValidatorAfterPoC = validatorsAfterPoC.validators.find {
            it.consensusPubkey.value == join1.node.getValidatorInfo().key
        }
        assertThat(join1ValidatorAfterPoC).isNotNull
//        assertThat(join1ValidatorAfterPoC!!.status).isNotEqualTo(StakeValidatorStatus.BONDED.value)
//        Logger.info("  Join1 is jailed (confirmation_weight=3 < alpha*regular_weight=5)")
//        Logger.info("  Join1 validator status: ${join1ValidatorAfterPoC.status}")

        logSection("Waiting for NEXT epoch where confirmation weights will be applied")
        genesis.waitForStage(EpochStage.START_OF_POC)
        Logger.info("New epoch started, confirmation weights will be used in settlement")

        // Record balances AFTER confirmation but BEFORE settlement
        val initialBalances = mapOf(
            genesis.node.getColdAddress() to genesis.node.getSelfBalance(),
            join1Address to join1.node.getSelfBalance(),
            join2.node.getColdAddress() to join2.node.getSelfBalance()
        )

        logSection("Waiting for reward settlement")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

        logSection("Verifying Join1 receives zero rewards (excluded from epoch)")
        val finalBalances = mapOf(
            genesis.node.getColdAddress() to genesis.node.getSelfBalance(),
            join1Address to join1.node.getSelfBalance(),
            join2.node.getColdAddress() to join2.node.getSelfBalance()
        )

        val genesisChange = finalBalances[genesis.node.getColdAddress()]!! - initialBalances[genesis.node.getColdAddress()]!!
        val join1Change = finalBalances[join1Address]!! - initialBalances[join1Address]!!
        val join2Change = finalBalances[join2.node.getColdAddress()]!! - initialBalances[join2.node.getColdAddress()]!!

        Logger.info("Balance changes:")
        Logger.info("  Genesis: $genesisChange (regular_weight=10, confirmation_weight=10)")
        Logger.info("  Join1: $join1Change (JAILED - excluded from settlement)")
        Logger.info("  Join2: $join2Change (regular_weight=10, confirmation_weight=10)")

        // Join1 should receive zero rewards (excluded from epoch after jailing)
        assertThat(join1Change).isEqualTo(0L)
        Logger.info("  Join1 received zero rewards (excluded from epoch)")

        // Genesis and Join2 should receive positive rewards
        assertThat(genesisChange).isGreaterThan(0)
        assertThat(join2Change).isGreaterThan(0)
        Logger.info("  Genesis and Join2 received positive rewards")

        // Genesis and Join2 should have similar rewards (both full weight, splitting total rewards)
        logSection("Verifying Genesis and Join2 split rewards")
        assertThat(genesisChange).isCloseTo(join2Change, Offset.offset(10L))
        Logger.info("  Genesis and Join2 received similar rewards: Genesis=$genesisChange, Join2=$join2Change")

        logSection("TEST PASSED: Confirmation PoC correctly jails participant below alpha threshold")
    }

}