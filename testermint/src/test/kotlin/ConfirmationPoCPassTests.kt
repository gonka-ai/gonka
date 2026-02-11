import com.productscience.EpochStage
import com.productscience.data.ConfirmationPoCPhase
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Percentage
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger

class ConfirmationPoCPassTests : TestermintTest() {
    // 12m
    @Test
    fun `confirmation PoC passed - same rewards`() {
        logSection("=== TEST: Confirmation PoC Passed - Same Rewards ===")

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

        logSection("Setting PoC mocks for confirmation (same weight=10)")
        genesis.setPocWeight(10)
        join1.setPocWeight(10)
        join2.setPocWeight(10)

        logSection("Waiting for confirmation PoC trigger during inference phase")
        val confirmationEvent = waitForConfirmationPoCTrigger(genesis)
        assertThat(confirmationEvent).isNotNull
        Logger.info("Confirmation PoC triggered at height ${confirmationEvent!!.triggerHeight}")

        logSection("Waiting for confirmation PoC generation phase")
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_GENERATION)
        Logger.info("Confirmation PoC generation phase active")

        logSection("Waiting for confirmation PoC validation phase")
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_VALIDATION)
        Logger.info("Confirmation PoC validation phase active")

        logSection("Waiting for confirmation PoC completion")
        waitForConfirmationPoCCompletion(genesis)
        Logger.info("Confirmation PoC completed (event cleared)")

        logSection("Waiting for NEXT epoch where confirmation weights will be applied")
        // Confirmation weights are only calculated and applied during the next epoch's settlement
        genesis.waitForStage(EpochStage.START_OF_POC)
        Logger.info("New epoch started, confirmation weights will be used in settlement")

        // Record balances AFTER confirmation but BEFORE settlement
        val initialBalances = mapOf(
            genesis.node.getColdAddress() to genesis.node.getSelfBalance(),
            join1.node.getColdAddress() to join1.node.getSelfBalance(),
            join2.node.getColdAddress() to join2.node.getSelfBalance()
        )

        logSection("Waiting for reward settlement with confirmation weights")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

        logSection("Verifying rewards are calculated using full weight")
        val finalBalances = mapOf(
            genesis.node.getColdAddress() to genesis.node.getSelfBalance(),
            join1.node.getColdAddress() to join1.node.getSelfBalance(),
            join2.node.getColdAddress() to join2.node.getSelfBalance()
        )

        // All participants should have received rewards based on their full weight
        val balanceChanges = mutableListOf<Long>()
        finalBalances.forEach { (address, finalBalance) ->
            val initialBalance = initialBalances[address]!!
            val change = finalBalance - initialBalance
            balanceChanges.add(change)
            Logger.info("  $address: balance change = $change")
            // Should have positive reward (not capped since confirmation weight matches regular weight)
            assertThat(change).isGreaterThan(0)
        }

        // All participants have same weight (10) and same confirmation weight (10)
        // So they should receive identical rewards
        logSection("Verifying all balance changes are identical")
        val expectedChange = balanceChanges[0]
        balanceChanges.forEach { change ->
            assertThat(change.toDouble()).isCloseTo(expectedChange.toDouble(), Percentage.withPercentage(1.0))
        }
        Logger.info("  All participants received identical rewards: $expectedChange")

        logSection("TEST PASSED: Confirmation PoC with same weight does not affect rewards")
    }

}