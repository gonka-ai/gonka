import com.productscience.ApplicationCLI
import com.productscience.EpochStage
import com.productscience.createSpec
import com.productscience.data.EpochPhase
import com.productscience.data.StakeValidator
import com.productscience.data.StakeValidatorStatus
import com.productscience.data.UpdateParams
import com.productscience.data.spec
import com.productscience.getNextStage
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Disabled
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger
import java.time.Duration
import kotlin.test.assertNotNull
import java.awt.Toolkit
import java.awt.datatransfer.StringSelection

class ParticipantTests : TestermintTest() {
    @Test
    @Tag("exclude")
    fun `print out gonka values`() {
        // useful for testing gonka client
        val (cluster, genesis) = initCluster()
        val addresses = cluster.allPairs.map {
            it.api.getPublicUrl() + ";" + it.node.getAddress()
        }
        val clipboardContent = buildString {
            appendLine("GONKA_ENDPOINTS=" + addresses.joinToString(separator = ","))
            appendLine("GONKA_ADDRESS=" + genesis.node.getAddress())
            appendLine("GONKA_PRIVATE_KEY=" + genesis.node.getPrivateKey())
        }

        Logger.warn(clipboardContent)
        val selection = StringSelection(clipboardContent)
        Toolkit.getDefaultToolkit().systemClipboard.setContents(selection, selection)
    }

    @Test
    fun `reputation increases after epoch participation`() {
        val (_, genesis) = initCluster()
        genesis.waitForNextInferenceWindow()

        val startStats = genesis.node.getParticipantCurrentStats()
        logSection("Running inferences")
        runParallelInferences(genesis, 10)
        logSection("Waiting for next epoch")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        logSection("verifying reputation increase")
        val endStats = genesis.node.getParticipantCurrentStats()
        val startParticipants = startStats.participantCurrentStats!!
        val endParticipants = endStats.participantCurrentStats!!

        val statsPairs = startParticipants.zip(endParticipants)
        statsPairs.forEach { (start, end) ->
            assertThat(end.participantId).isEqualTo(start.participantId)
            assertThat(end.reputation).isGreaterThan(start.reputation)
        }
    }

    @Test
    fun `add node after snapshot`() {
        val (cluster, genesis) = initCluster()
        logSection("Waiting for snapshot height")
        genesis.node.waitForMinimumBlock(102)
        val height = genesis.node.getStatus().syncInfo.latestBlockHeight
        logSection("Adding a new node after snapshot height reached")
        val biggerCluster = cluster.withAdditionalJoin()
        assertThat(biggerCluster.joinPairs).hasSize(3)
        val newPair = biggerCluster.joinPairs.find { it.name == "/join" + biggerCluster.joinPairs.size }
        assertThat(newPair).isNotNull
        logSection("Verifying new node has joined for " + newPair!!.name)
        Thread.sleep(Duration.ofSeconds(30))
        newPair.node.waitForMinimumBlock(height + 20)
        logSection("Verifying state was loaded from snapshot")
        val currentHeight = genesis.node.getStatus().syncInfo.latestBlockHeight
        assertThat(newPair.node.logOutput.minimumHeight).isGreaterThan(99)
        assertThat(newPair.node.logOutput.minimumHeight).isLessThan(currentHeight)
    }

    @Test
    fun `traffic basis decreases minimum average validation`() {
        val (_, genesis) = initCluster()
        logSection("Making sure traffic basis is low")
        var startMin = genesis.node.getMinimumValidationAverage()
        if (startMin.trafficBasis >= 10) {
            // Wait for current and previous values to no longer apply
            genesis.node.waitForMinimumBlock(startMin.blockHeight + genesis.getEpochLength() * 2, "twoEpochsAhead")
            startMin = genesis.node.getMinimumValidationAverage()
        }
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)
        logSection("Running inferences")
        runParallelInferences(genesis, 50, waitForBlocks = 1)
        genesis.waitForBlock(2) {
            it.node.getMinimumValidationAverage().minimumValidationAverage < startMin.minimumValidationAverage
        }
        logSection("verifying traffic basis decrease")
        val stopMin = genesis.node.getMinimumValidationAverage()
        assertThat(stopMin.minimumValidationAverage).isLessThan(startMin.minimumValidationAverage)
    }
}
