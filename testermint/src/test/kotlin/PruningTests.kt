import com.productscience.EpochStage
import com.productscience.inferenceRequest
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger
import java.time.Duration
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
        assertNotNull(inferenceState2, "Inference not in chain")
        genesis.waitForNextInferenceWindow()
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Checking after two epochs")
        val inferenceState3 = genesis.node.getInference(inferenceResult.id)
        assertThat(inferenceState3).withFailMessage { "Inference not pruned after two epochs" }.isNull()
    }

    @Test
    fun `prune PoCs`() {
        val (_, genesis) = initCluster()
        logSection("Waiting for non-zero epoch")
        // Zero epoch has no PoCs
        genesis.node.waitForState("non-zero epoch", staleTimeout = Duration.ofSeconds(60)){
            genesis.getEpochData().latestEpoch.pocStartBlockHeight != 0L
        }
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
        Logger.info("After one: $afterBatchCount, $afterValidationCount")
        logSection("Waiting for next epoch")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Getting PoC counts after epoch")
        val afterBatchCount2 = genesis.node.getPocBatchCount(startEpoch)
        val afterValidationCount2 = genesis.node.getPocValidationCount(startEpoch)
        Logger.info("After one: $afterBatchCount2, $afterValidationCount2")
        assertThat(afterBatchCount2).isZero()
        assertThat(afterValidationCount2).isZero()
    }
}