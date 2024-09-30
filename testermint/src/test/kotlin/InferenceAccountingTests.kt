import com.productscience.COIN_HALVING_HEIGHT
import com.productscience.EPOCH_NEW_COIN
import com.productscience.EpochLength
import com.productscience.LocalInferencePair
import com.productscience.data.Participant
import com.productscience.getInferenceResult
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.initialize
import com.productscience.setNewValidatorsStage
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger
import kotlin.math.pow

class InferenceAccountingTests : TestermintTest() {

    @Test
    fun `test get participants`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        highestFunded.node.waitForNextBlock()
        val participants = highestFunded.api.getParticipants()
        Logger.debug(participants)
        assertThat(participants).hasSize(3)
        val nextSettleBlock = getNextSettleBlock(highestFunded.node.getStatus().syncInfo.latestBlockHeight)
        highestFunded.node.waitForMinimumBlock(nextSettleBlock)
        val participantsAfterEach = highestFunded.api.getParticipants()
        Logger.debug(participantsAfterEach)
    }

    @Test
    fun `get participants no initialize`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val requesterPair = pairs.first { it.name == "requester" }
        val executorPair = pairs.first { it.name == "executor" }
        val participants = requesterPair.api.getParticipants()
        val participants2 = executorPair.api.getParticipants()
        Logger.debug(participants)
        Logger.debug(participants2)
    }
    @Test
    fun `test escrow and pre settle amounts`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        val inferenceResult = generateSequence {
            getInferenceResult(highestFunded)
        }.first { it.inference.executedBy != it.inference.receivedBy }

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
        verifySettledInferences(highestFunded, 4)
    }

    private fun verifySettledInferences(highestFunded: LocalInferencePair, inferenceCount: Int) {
        val inferences = generateSequence {
            getInferenceResult(highestFunded)
        }.take(inferenceCount)
        // More than just debugging, this forces the evaluation of the sequence
        Logger.info("Inference count: ${inferences.count()}")
        val currentHeight = highestFunded.getCurrentBlockHeight()
        val preSettle = highestFunded.api.getParticipants()
        val nextSettleBlock = getNextSettleBlock(currentHeight)
        highestFunded.node.waitForMinimumBlock(nextSettleBlock)

        val afterSettle = highestFunded.api.getParticipants()
        val coinRewards = calculateCoinRewards(preSettle, EPOCH_NEW_COIN, nextSettleBlock - 1)
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
            assertThat(participantAfter.balance)
                .`as`("Balance has previous coinsOwed and refundsOwed for ${participant.id}")
                .isEqualTo(expectedTotal)
        }
    }

    @Test
    fun `test post settle amounts with halving`() {
        val pairs = getLocalInferencePairs(inferenceConfig)
        val highestFunded = initialize(pairs)
        if (highestFunded.getCurrentBlockHeight() < COIN_HALVING_HEIGHT) {
            highestFunded.node.waitForMinimumBlock(COIN_HALVING_HEIGHT.toLong())
        }
        verifySettledInferences(highestFunded, 4)
    }

    private fun calculateCoinRewards(
        preSettle: List<Participant>,
        rewards: Long,
        blockHeight: Long,
    ): Map<Participant, Long> {
        val halvings: Long = blockHeight / COIN_HALVING_HEIGHT
        val adjustedRewards = rewards / (2.0.pow(halvings.toInt())).toLong()
        Logger.debug(
            "Rewards calculation: baseRewards:$rewards, height:$blockHeight halvings:$halvings, " +
                    "adjusted:$adjustedRewards"
        )
        val totalWork = preSettle.sumOf { it.coinsOwed }
        return preSettle.associateWith { participant ->
            val share = participant.coinsOwed.toDouble() / totalWork
            Logger.debug("Participant ${participant.id} share: $share")
            Logger.debug("Participant ${participant.id} reward: ${(adjustedRewards * share).toLong()}")
            (adjustedRewards * share).toLong()
        }
    }

    private fun getNextSettleBlock(currentHeight: Long): Long {
        val blocksTillEpoch = EpochLength - (currentHeight % EpochLength)

        val nextSettle = currentHeight + blocksTillEpoch + setNewValidatorsStage + 1
        return if (nextSettle - EpochLength > currentHeight)
            nextSettle - EpochLength
        else
            return nextSettle
    }
}
