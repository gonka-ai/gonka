import com.productscience.*
import com.productscience.data.MsgSubmitNewParticipant
import com.productscience.data.getParticipant
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Tag
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger
import java.awt.Toolkit
import java.awt.datatransfer.StringSelection
import java.time.Duration
import kotlin.test.assertNotNull
import com.productscience.assertions.assertThat
class ParticipantTests : TestermintTest() {
    @Test
    @Tag("exclude")
    fun `print out gonka values`() {
        // useful for testing gonka client
        val (cluster, genesis) = initCluster()
        val addresses = cluster.allPairs.map {
            it.api.getPublicUrl() + ";" + it.node.getColdAddress()
        }
        val clipboardContent = buildString {
            appendLine("GONKA_ENDPOINTS=" + addresses.joinToString(separator = ","))
            appendLine("GONKA_ADDRESS=" + genesis.node.getColdAddress())
            appendLine("GONKA_PRIVATE_KEY=" + genesis.node.getColdPrivateKey())
        }

        Logger.warn(clipboardContent)
        val selection = StringSelection(clipboardContent)
        Toolkit.getDefaultToolkit().systemClipboard.setContents(selection, selection)
    }

    @Test
    fun `reputation increases after epoch participation`() {
        val (_, genesis) = initCluster()
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        genesis.waitForMlNodesToLoad()

        val startStats = genesis.node.getParticipantCurrentStats()
        logSection("Running inferences")
        runParallelInferences(genesis, 10, maxConcurrentRequests = 10)
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
        val (cluster, genesis) = initCluster(reboot = true)
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
        val inferenceRequest = inferenceRequestObject.copy(
            maxTokens = 20 // To not trigger bandwidth limit
        )
        runParallelInferences(
            genesis, 50, waitForBlocks = 3, maxConcurrentRequests = 50,
            inferenceRequest = inferenceRequest
        )
        genesis.waitForBlock(2) {
            it.node.getMinimumValidationAverage().minimumValidationAverage < startMin.minimumValidationAverage
        }
        logSection("verifying traffic basis decrease")
        val stopMin = genesis.node.getMinimumValidationAverage()
        assertThat(stopMin.minimumValidationAverage).isLessThan(startMin.minimumValidationAverage)
    }

    @Test
    fun `changing url propagates`() {
        val (_, genesis) = initCluster()
        logSection("waiting for next epoch")
        genesis.waitForNextEpoch()
        val genesisParticipant = genesis.node.getRawParticipants().getParticipant(genesis)
        assertNotNull(genesisParticipant, "Unable to get participant stats for genesis")
        assertThat(genesisParticipant.epochsCompleted).isGreaterThan(0)
        val addNewParticipantMessage = MsgSubmitNewParticipant(
            creator = genesis.node.getColdAddress(),
            url = "https://new-url.com",
        )
        val newMessage = genesis.submitMessage(addNewParticipantMessage)
        assertThat(newMessage).isSuccess()
        val updatedGenesisParticipant = genesis.node.getRawParticipants().getParticipant(genesis)
        assertNotNull(updatedGenesisParticipant, "Unable to get stats for new participant")
        assertThat(updatedGenesisParticipant.epochsCompleted).isEqualTo(genesisParticipant.epochsCompleted)
        assertThat(updatedGenesisParticipant.inferenceUrl).isEqualTo("https://new-url.com")
        assertThat(updatedGenesisParticipant.status).isEqualTo(genesisParticipant.status)
        assertThat(updatedGenesisParticipant.joinTime).isEqualTo(genesisParticipant.joinTime)
        assertThat(updatedGenesisParticipant.joinHeight).isEqualTo(genesisParticipant.joinHeight)
        genesis.waitForNextEpoch()
        val genesisParticipantAfterNewEpoch = genesis.api.getActiveParticipants().getParticipant(genesis)
        assertNotNull(genesisParticipantAfterNewEpoch, "Unable to get stats for new participant")
        assertThat(genesisParticipantAfterNewEpoch.inferenceUrl).isEqualTo("https://new-url.com")
    }
}
