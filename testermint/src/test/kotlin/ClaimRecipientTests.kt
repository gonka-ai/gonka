import com.productscience.EpochStage
import com.productscience.assertions.assertThat
import com.productscience.data.ClaimRecipientEntry
import com.productscience.data.MsgSetClaimRecipients
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
    fun `claim recipient schedule can be set and cleared`() {
        val (cluster, genesis) = initCluster(config = claimRecipientConfig, reboot = true)
        cluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }

        logSection("Clear pending claims before configuring recipient")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 3)

        val participant = genesis.node.getColdAddress()
        val recipient = genesis.node.createKey("claim-recipient-${System.currentTimeMillis()}").address
        val targetEpoch = genesis.getEpochData().latestEpoch.index + 1

        logSection("Configure recipient for epoch $targetEpoch")
        val setRecipient = genesis.submitMessage(
            MsgSetClaimRecipients(
                creator = participant,
                entries = listOf(ClaimRecipientEntry(epoch = targetEpoch, recipient = recipient))
            )
        )
        assertThat(setRecipient).isSuccess()
        assertThat(genesis.node.listClaimRecipients(participant).entries)
            .anyMatch { it.epoch == targetEpoch && it.recipient == recipient }

        logSection("Clear recipient for epoch $targetEpoch")
        val clearRecipient = genesis.submitMessage(
            MsgSetClaimRecipients(
                creator = participant,
                entries = listOf(ClaimRecipientEntry(epoch = targetEpoch, recipient = ""))
            )
        )
        assertThat(clearRecipient).isSuccess()
        assertThat(genesis.node.listClaimRecipients(participant).entries)
            .noneMatch { it.epoch == targetEpoch && it.recipient == recipient }
    }

    @Test
    fun `claim recipient pruning waits until epoch is safely stale`() {
        val (cluster, genesis) = initCluster(config = claimRecipientConfig, reboot = true)
        cluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }

        logSection("Clear pending claims before configuring stale recipient")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 3)

        val participant = genesis.node.getColdAddress()
        val recipient = genesis.node.createKey("claim-recipient-prune-${System.currentTimeMillis()}").address
        val targetEpoch = genesis.getEpochData().latestEpoch.index + 1

        logSection("Configure recipient for epoch $targetEpoch without creating claimable rewards")
        val setRecipient = genesis.submitMessage(
            MsgSetClaimRecipients(
                creator = participant,
                entries = listOf(ClaimRecipientEntry(epoch = targetEpoch, recipient = recipient))
            )
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
        while (genesis.getEpochData().latestEpoch.index < targetEpoch + 3) {
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
    }
}
