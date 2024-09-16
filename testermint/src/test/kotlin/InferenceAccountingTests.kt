import com.productscience.EpochLength
import com.productscience.getInferenceResult
import com.productscience.getLocalInferencePairs
import com.productscience.inferenceConfig
import com.productscience.initialize
import com.productscience.setNewValidatorsStage
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger

class InferenceAccountingTests : TestermintTest() {
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
        val inferences = generateSequence {
            getInferenceResult(highestFunded)
        }.take(4)
        // More than just debugging, this forces the evaluation of the sequence
        Logger.info("Inference count: ${inferences.count()}")
        val currentHeight = highestFunded.getCurrentBlockHeight()
        val preSettle = highestFunded.api.getParticipants()
        highestFunded.node.waitForMinimumBlock(getNextSettleBlock(currentHeight))

        val afterSettle = highestFunded.api.getParticipants()
        // Represents the change from when we first made the inference to after the settle
        for (participant in preSettle) {
            val participantAfter = afterSettle.first { it.id == participant.id }
            assertThat(participantAfter.refundsOwed).`as`("No refunds owed after settle for ${participant.id}")
                .isEqualTo(0)
            assertThat(participantAfter.coinsOwed).`as`("No coins owed after settle for ${participant.id}").isEqualTo(0)
            assertThat(participantAfter.balance).`as`("Balance has previous coinsOwed and refundsOwed for ${participant.id}")
                .isEqualTo(participant.balance + participant.coinsOwed + participant.refundsOwed)
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
