import com.productscience.EpochStage
import com.productscience.assertions.assertThat
import com.productscience.data.ClaimRecipientEntry
import com.productscience.data.UnfundedInferenceParticipant
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit

@Timeout(value = 20, unit = TimeUnit.MINUTES)
class ClaimRecipientTests : TestermintTest() {
    @Test
    fun `claim rewards can be routed to configured recipient`() {
        val (cluster, genesis) = initCluster(config = claimRecipientConfig, reboot = true)
        cluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }

        logSection("Clear pending claims before configuring recipient")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 3)

        val participantPair = cluster.joinPairs.first()
        val participant = participantPair.node.getColdAddress()
        val recipient = genesis.node.createKey("claim-recipient-${System.currentTimeMillis()}").address
        val targetEpoch = genesis.getEpochData().latestEpoch.index + 1
        val recipientBalanceBefore = genesis.getBalance(recipient)

        logSection("Configure recipient for epoch $targetEpoch")
        val setRecipient = participantPair.node.setClaimRecipients(claimRecipientsJson(targetEpoch, recipient))
        assertThat(setRecipient).isSuccess()
        assertThat(genesis.node.listClaimRecipients(participant).entries)
            .anyMatch { it.epoch == targetEpoch && it.recipient == recipient }

        logSection("Wait until target epoch $targetEpoch is active")
        while (genesis.getEpochData().latestEpoch.index < targetEpoch) {
            genesis.waitForNextEpoch()
        }
        val rewardSeed = participantPair.api.getConfig().currentSeed

        genesis.markNeedsReboot()
        participantPair.stopApiContainer()
        logSection("Stopped participant API to prevent auto-claim before manual verification")

        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)
        val claimResponse = participantPair.submitTransaction(
            listOf(
                "inference",
                "claim-rewards",
                rewardSeed.seed.toString(),
                rewardSeed.epochIndex.toString(),
            )
        )
        assertThat(claimResponse).isSuccess()

        val recipientBalanceAfter = genesis.getBalance(recipient)
        assertThat(recipientBalanceAfter)
            .`as`("configured recipient receives the claim payout")
            .isGreaterThan(recipientBalanceBefore)
        assertThat(genesis.node.listClaimRecipients(participant).entries)
            .`as`("recipient entry is retained after claim for late same-epoch payouts")
            .anyMatch { it.epoch == targetEpoch && it.recipient == recipient }
    }

    @Test
    fun `claim recipient pruning waits until epoch is safely stale`() {
        val (cluster, genesis) = initCluster(config = claimRecipientConfig, reboot = true)
        cluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }

        val participantKey = genesis.node.createKey("claim-recipient-prune-participant-${System.currentTimeMillis()}")
        val participant = participantKey.address
        genesis.api.addUnfundedInferenceParticipant(
            UnfundedInferenceParticipant(
                url = "",
                models = listOf(),
                validatorKey = "",
                pubKey = participantKey.pubkey.key,
                address = participant
            )
        )
        genesis.node.waitForNextBlock(2)

        val recipient = genesis.node.createKey("claim-recipient-prune-${System.currentTimeMillis()}").address
        val targetEpoch = genesis.getEpochData().latestEpoch.index + 1

        logSection("Configure recipient for inactive participant epoch $targetEpoch")
        val setRecipient = genesis.node.setClaimRecipients(
            claimRecipientsJson(targetEpoch, recipient),
            from = participantKey.name
        )
        assertThat(setRecipient).isSuccess()
        assertThat(genesis.node.listClaimRecipients(participant).entries)
            .anyMatch { it.epoch == targetEpoch && it.recipient == recipient }

        logSection("Advance until target epoch is claimable, but not pruneable")
        while (genesis.getEpochData().latestEpoch.index < targetEpoch + 1) {
            genesis.waitForNextEpoch()
        }
        assertThat(genesis.node.listClaimRecipients(participant).entries)
            .`as`("recipient entry is still present while the epoch is only one epoch old")
            .anyMatch { it.epoch == targetEpoch && it.recipient == recipient }

        logSection("Advance until target epoch is past the pruning threshold")
        while (genesis.getEpochData().latestEpoch.index < targetEpoch + 5) {
            genesis.waitForNextEpoch()
        }
        genesis.node.waitForNextBlock(2)

        assertThat(genesis.node.listClaimRecipients(participant).entries)
            .`as`("recipient entry is pruned only after it is safely stale")
            .noneMatch { it.epoch == targetEpoch && it.recipient == recipient }
    }

    companion object {
        private val claimRecipientConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec
        )

        private fun claimRecipientsJson(epoch: Long, recipient: String): String =
            """[{"epoch":$epoch,"recipient":"$recipient"}]"""
    }
}
