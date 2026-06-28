import com.productscience.EpochStage
import com.productscience.assertions.assertThat
import com.productscience.data.ClaimRecipientEntry
import com.productscience.getInferenceResult
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.logSection
import com.github.kittinunf.fuel.core.FuelError
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
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
        val setRecipient = participantPair.submitTransaction(
            setClaimRecipientsArgs(targetEpoch, recipient)
        )
        assertThat(setRecipient).isSuccess()
        assertThat(genesis.node.listClaimRecipients(participant).entries)
            .anyMatch { it.epoch == targetEpoch && it.recipient == recipient }

        logSection("Wait until target epoch $targetEpoch is active")
        while (genesis.getEpochData().latestEpoch.index < targetEpoch) {
            genesis.waitForNextEpoch()
        }
        genesis.waitForNextInferenceWindow()

        val rewardSeed = participantPair.api.getConfig().currentSeed
        logSection("Waiting for an inference assigned to participant $participant")
        val earnedInference = (1..20)
            .asSequence()
            .mapNotNull { attempt ->
                runCatching { getInferenceResult(genesis) }
                    .onFailure { error ->
                        val isTemporary500 = error is FuelError &&
                            error.message.orEmpty().contains("500 Internal Server Error")
                        val isChainLag = error is IllegalStateException &&
                            error.message.orEmpty().contains("Inference never logged in chain")
                        if (!isTemporary500 && !isChainLag) {
                            throw error
                        }
                        Logger.info(
                            "Inference attempt $attempt hit a transient response while waiting for participant assignment; retrying on the next block: ${error.message}"
                        )
                        genesis.waitForBlock(1) { true }
                    }
                    .getOrNull()
            }
            .firstOrNull { result ->
                result.inference.assignedTo == participant || result.inference.executedBy == participant
            }
            ?: error("Participant did not receive an inference in the target epoch")
        logSection("Participant served inference ${earnedInference.inference.inferenceId}")

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
            .noneMatch { it.epoch == targetEpoch && it.recipient == recipient }
    }

    @Test
    fun `claim recipient pruning waits until epoch is safely stale`() {
        val (cluster, genesis) = initCluster(config = claimRecipientConfig, reboot = true)
        cluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }

        cluster.withConsumer("claim-recipient-prune-consumer") { consumer ->
            val participant = consumer.address
            val recipient = genesis.node.createKey("claim-recipient-prune-${System.currentTimeMillis()}").address
            val targetEpoch = genesis.getEpochData().latestEpoch.index + 1

            logSection("Fund consumer before submitting recipient schedule")
            genesis.submitTransaction(
                listOf(
                    "bank",
                    "send",
                    genesis.node.getColdAddress(),
                    participant,
                    "100000${claimRecipientConfig.denom}"
                )
            )
            genesis.node.waitForNextBlock(2)

            logSection("Configure recipient for inactive participant epoch $targetEpoch")
            val setRecipient = consumer.pair.submitTransaction(
                setClaimRecipientsArgs(targetEpoch, recipient)
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
    }

    companion object {
        private val claimRecipientConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec
        )

        private fun setClaimRecipientsArgs(epoch: Long, recipient: String): List<String> {
            val entriesJson = """[{"epoch":$epoch,"recipient":"$recipient"}]"""
            return listOf(
                "inference",
                "set-claim-recipients",
                "--entries",
                entriesJson
            )
        }
    }
}
