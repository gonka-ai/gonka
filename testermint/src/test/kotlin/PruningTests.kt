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
        genesis.node.waitForNextBlock(2)
        val inferenceState1 = genesis.node.getInference(inferenceResult.id)
        assertNotNull(inferenceState1, "Inference not in chain")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, offset = 2)
        logSection("Checking after one epoch")
        val inferenceState2 = genesis.node.getInference(inferenceResult.id)
        assertNotNull(inferenceState2, "Inference not in chain")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS, offset = 2)
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

        val startEpoch = genesis.getEpochData().latestEpoch
        val startEpochBlockHeight = genesis.getEpochData().latestEpoch.pocStartBlockHeight
        logSection("Getting PoC counts. startEpoch.Index = ${startEpoch.index} startEpoch.pocStartBlockHeight: $startEpochBlockHeight")
        val startBatchCount = genesis.node.getPocBatchCount(startEpochBlockHeight)
        val startValidationCount = genesis.node.getPocValidationCount(startEpochBlockHeight)
        assertThat(startBatchCount).isNotZero()
        assertThat(startValidationCount).isNotZero()

        logSection("Waiting for next (+1) epoch. epoch.Index = ${startEpoch.index + 1}")
        genesis.waitForNextEpoch()

        val epoch2 = genesis.getEpochData().latestEpoch
        logSection("Getting PoC counts after epoch. epoch.Index = ${epoch2.index}. epoch.pocStartBlockHeight: ${epoch2.pocStartBlockHeight}")
        val afterBatchCount = genesis.node.getPocBatchCount(startEpochBlockHeight)
        val afterValidationCount = genesis.node.getPocValidationCount(startEpochBlockHeight)
        Logger.info("After one: $afterBatchCount, $afterValidationCount")
        assertThat(startBatchCount).isNotZero()
        assertThat(startValidationCount).isNotZero()

        logSection("Waiting for next (+2) epoch. epoch.Index = ${epoch2.index + 1}")
        genesis.waitForNextEpoch()

        val epoch3 = genesis.getEpochData().latestEpoch
        logSection("Getting PoC counts after epoch. epoch.Index = ${epoch3.index}. epoch.pocStartBlockHeight: ${epoch3.pocStartBlockHeight}")
        val afterBatchCount2 = genesis.node.getPocBatchCount(startEpochBlockHeight)
        val afterValidationCount2 = genesis.node.getPocValidationCount(startEpochBlockHeight)
        Logger.info("After one: $afterBatchCount2, $afterValidationCount2")
        assertThat(afterBatchCount2).isZero()
        assertThat(afterValidationCount2).isZero()
    }
}
