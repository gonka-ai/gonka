import com.productscience.EpochStage
import com.productscience.inferenceRequest
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import kotlin.test.assertNotNull

class PruningTests : TestermintTest() {
    @Test
    fun `prune inferences`() {
        val (_, genesis) = initCluster(reboot = true)
        genesis.waitForNextInferenceWindow()
        logSection("Making Inference")
        val inferenceResult = genesis.makeInferenceRequest(inferenceRequest)
        genesis.node.waitForNextBlock()
        val inferenceState1 = genesis.node.getInference(inferenceResult.id)
        assertNotNull(inferenceState1, "Inference not in chain")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Checking after one epoch")
        val inferenceState2 = genesis.node.getInference(inferenceResult.id)
        assertNotNull(inferenceState1, "Inference not in chain")
        genesis.waitForNextInferenceWindow()
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Checking after two epochs")
        val inferenceState3 = genesis.node.getInference(inferenceResult.id)
        assertThat(inferenceState3).withFailMessage { "Inference not pruned after two epochs" }.isNull()
    }

    @Test
    fun `prune PoCs`() {
        val (_, genesis) = initCluster()
        logSection("Getting PoC counts")
        val startEpoch = genesis.getEpochData().latestEpoch.pocStartBlockHeight
        val startBatchCount = genesis.node.getPocBatchCount(startEpoch)
        val startValidationCount = genesis.node.getPocValidationCount(startEpoch)
        assertThat(startBatchCount).isNotZero()
        assertThat(startValidationCount).isNotZero()
        logSection("Waiting for next epoch")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Getting PoC counts after epoch")
        val afterBatchCount = genesis.node.getPocBatchCount(startEpoch)
        val afterValidationCount = genesis.node.getPocValidationCount(startEpoch)
        assertThat(afterBatchCount).isZero()
        assertThat(afterValidationCount).isZero()
    }
}