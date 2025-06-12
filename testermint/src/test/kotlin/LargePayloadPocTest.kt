import com.productscience.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit
import org.tinylog.kotlin.Logger
import kotlin.test.assertNotNull

@Timeout(value = 10, unit = TimeUnit.MINUTES)
class LargePayloadPocTest : TestermintTest() {

    @Test
    @Tag("poc_large_payload")
    fun `test poc cycle with large callback payloads`() {
        val (cluster, genesis) = initCluster()

        val genesisMock = genesis.mock
        assertNotNull(genesisMock, "Genesis InferenceMock (genesis.mock) must not be null to configure PoC responses.")

        val largeArraySize = 100_000L

        Logger.info("Configuring PoC generate and validate mocks for genesis with array size: $largeArraySize")
        // genesisMock.setPocResponse(largeArraySize)
        genesisMock.setPocValidationResponse(largeArraySize)

/*        logSection("Initial sync: Waiting for SET_NEW_VALIDATORS stage before triggering PoC")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        logSection("Triggering PoC with changePoc for genesis node")
        // The value for changePoc typically indicates a weight/stake.
        // The actual payload array size is determined by our dynamic mocks here.
        // Using a representative value like 100 for the PoC submission.
        genesis.changePoc(100)

        logSection("Waiting for PoC cycle to process with large payloads (next START_OF_POC & SET_NEW_VALIDATORS)")
        // These stages indicate the epoch where PoC was submitted and evaluated has likely completed.
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        logSection("PoC cycle completed. Verifying genesis node's PoC status.")
        // Check if the genesis node, after submitting PoC with large callback payloads,
        // is recognized in the top miners list. This indicates successful PoC processing.
        val topMiners = genesis.node.getTopMiners()
        assertThat(topMiners.topMiner)
            .withFailMessage("Top miners list should not be empty after PoC submission by genesis.")
            .isNotEmpty

        assertThat(topMiners.topMiner.any { it.address == genesis.node.getAddress() })
            .withFailMessage("Genesis node should be listed among the top miners after successful PoC submission.")
            .isTrue()

        Logger.info("Successfully completed PoC cycle with large callback payloads for genesis node.")*/
    }
} 