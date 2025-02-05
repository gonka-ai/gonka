import com.productscience.ApplicationCLI
import com.productscience.LocalInferencePair
import com.productscience.data.AppExport
import com.productscience.data.InferenceParams
import com.productscience.data.Participant
import com.productscience.data.UnfundedInferenceParticipant
import com.productscience.getInferenceResult
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.inferenceRequest
import com.productscience.initialize
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger
import kotlin.math.roundToLong
import kotlin.test.assertNotNull

class InferenceAccountingTests : TestermintTest() {

    @Test
    fun `test get participants`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        highestFunded.node.waitForNextBlock()
        val participants = highestFunded.api.getParticipants()
        Logger.debug(participants)
        assertThat(participants).hasSize(3)
        val nextSettleBlock = highestFunded.getNextSettleBlock()
        highestFunded.node.waitForMinimumBlock(nextSettleBlock)
        val participantsAfterEach = highestFunded.api.getParticipants()
        Logger.debug(participantsAfterEach)
    }

    @Test
    fun `test get app export`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        val state = highestFunded.node.exportState()
        Logger.debug(state)
    }

    @Test
    fun `test get inference params`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        val params = highestFunded.node.getInferenceParams()
        Logger.info(params)
    }

    @Test
    fun `test escrow and pre settle amounts`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        val inferenceResult = generateSequence {
            getInferenceResult(highestFunded)
        }.first { it.inference.executedBy != it.inference.requestedBy }

        val inferenceCost = inferenceResult.inference.actualCost
        val escrowHeld = inferenceResult.inference.escrowAmount

        assertThat(inferenceResult.requesterBalanceChange).`as`("escrow withheld").isEqualTo(-escrowHeld)
        assertThat(inferenceResult.requesterOwedChange).`as`("requester not owed").isEqualTo(0)
        assertThat(inferenceResult.requesterRefundChange).`as`("requester assigned refund")
            .isEqualTo(escrowHeld - inferenceCost)
        assertThat(inferenceResult.executorRefundChange).isEqualTo(0)
        assertThat(inferenceResult.executorBalanceChange).isEqualTo(0)
        assertThat(inferenceResult.executorOwedChange).`as`("executor owed for inference").isEqualTo(inferenceCost)
    }

    @Test
    fun `test post settle amounts`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        val participants = highestFunded.api.getParticipants()
        participants.forEach {
            Logger.info("Participant: ${it.id}, Reputation: ${it.reputation}")
        }
        verifySettledInferences(highestFunded, 4)
        val postParticipants = highestFunded.api.getParticipants()
        postParticipants.forEach {
            Logger.info("Participant: ${it.id}, Reputation: ${it.reputation}")
        }

    }

    private fun verifySettledInferences(highestFunded: LocalInferencePair, inferenceCount: Int) {
        val inferences = generateSequence {
            getInferenceResult(highestFunded)
        }.take(inferenceCount)
        // More than just debugging, this forces the evaluation of the sequence
        Logger.info("Inference count: ${inferences.count()}")
        val currentHeight = highestFunded.getCurrentBlockHeight()
        val preSettle = highestFunded.api.getParticipants()
        val nextSettleBlock = highestFunded.getNextSettleBlock()
        highestFunded.node.waitForMinimumBlock(nextSettleBlock + 10)

        val afterSettle = highestFunded.api.getParticipants()
        val params = highestFunded.node.getInferenceParams()
        val coinRewards = calculateCoinRewards(preSettle, highestFunded.node.mostRecentExport, params)
        // Represents the change from when we first made the inference to after the settle
        for (participant in preSettle) {
            val participantAfter = afterSettle.first { it.id == participant.id }
            assertThat(participantAfter.refundsOwed).`as`("No refunds owed after settle for ${participant.id}")
                .isEqualTo(0)
            assertThat(participantAfter.coinsOwed).`as`("No coins owed after settle for ${participant.id}").isEqualTo(0)
            val expectedTotal = participant.balance + // Balance before settle
                    participant.coinsOwed + // Coins earned for performing inferences
                    participant.refundsOwed + // refunds from excess escrow
                    coinRewards[participant]!! // coins earned from the epoch
            Logger.info("Existing Balance: ${participant.balance}, Earned:${participant.coinsOwed}, " +
                    "Refunds:${participant.refundsOwed}, Rewards:${coinRewards[participant]}")
            assertThat(participantAfter.balance)
                .`as`("Balance has previous coinsOwed and refundsOwed for ${participant.id}")
                .isEqualTo(expectedTotal)
        }
    }

    @Test
    fun `test consumer only participant`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val genesis = initialize(pairs)
        // Spin up an ephemeral node to manage consumer keys and auth
        val consumerKey = "consumer1"
        ApplicationCLI(consumerKey, inferenceConfig).use { consumer ->
            consumer.createContainer(doNotStartChain = true)
            val newKey = consumer.createKey(consumerKey)
            Logger.warn("New key: ${newKey.address}")
            genesis.api.addUnfundedInferenceParticipant(
                UnfundedInferenceParticipant(
                    "",
                    listOf(),
                    "",
                    newKey.pubkey.key,
                    newKey.address
                )
            )
            genesis.node.waitForNextBlock()
            val participants = genesis.api.getParticipants()
            val consumerParticipant = participants.first { it.id == newKey.address }
            assertThat(consumerParticipant.balance).isGreaterThan(100_000_000)
            val consumerPair = LocalInferencePair(consumer, genesis.api, null, consumerKey)
            val result = consumerPair.makeInferenceRequest(inferenceRequest, newKey.address)
            assertThat(result).isNotNull
            val inference = generateSequence {
                genesis.node.waitForNextBlock()
                genesis.api.getInference(result.id)
            }.take(5).firstOrNull { it.executedBy != null }
            assertNotNull(inference, "Inference never finished")
            assertThat(inference.executedBy).isNotNull()
            assertThat(inference.requestedBy).isEqualTo(newKey.address)
            val participantsAfter = genesis.api.getParticipants()
            assertThat(participantsAfter).anyMatch { it.id == newKey.address }.`as`("Consumer listed in participants")
            val consumerAfter = participantsAfter.first { it.id == newKey.address }
            Logger.info("Executed by: ${inference.executedBy}")
            assertThat(participantsAfter).anyMatch { it.id == inference.executedBy }
                .`as`("Executor listed in participants")
            val executor = participantsAfter.first { it.id == inference.executedBy }
            assertThat(consumerAfter.balance).isEqualTo(consumerParticipant.balance - inference.escrowAmount)
                .`as`("Balance matches expectation")
            assertThat(executor.coinsOwed).isEqualTo(inference.actualCost).`as`("Coins owed does not match cost")
        }
    }

    private fun calculateCoinRewards(
        preSettle: List<Participant>,
        mostRecentExport: AppExport?,
        params: InferenceParams,
    ): Map<Participant, Long> {
        val bonusPercentage = 0.9
        return preSettle.associateWith { participant ->
            val coinsForParticipant = (participant.coinsOwed / (1 - bonusPercentage)).roundToLong()
            Logger.info("Participant: ${participant.id}, Owed: ${participant.coinsOwed}, " +
                    "Bonus: $bonusPercentage, RewardCoins: $coinsForParticipant")
            coinsForParticipant
        }
    }
}
