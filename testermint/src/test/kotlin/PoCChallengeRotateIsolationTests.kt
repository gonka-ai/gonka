import com.productscience.data.ConfirmationPoCPhase
import com.productscience.data.getParticipant
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit

@Timeout(value = 20, unit = TimeUnit.MINUTES)
class PoCChallengeRotateIsolationTests : TestermintTest() {
    @Test
    fun `create first then cPoC rotate does not leak first-segment artifacts`() {
        logSection("=== TEST: PoCChallenge create-first rotate isolation ===")
        val env = bootPoCChallengeCluster(
            expectedConfirmationsPerEpoch = 0,
            pocStageDuration = 5,
            alphaThreshold = 0.01,
        )
        val genesis = env.genesis
        val join1 = env.join1
        val join2 = env.join2
        val target = join1.node.getColdAddress()

        logSection("Setting honest v2 weights before create")
        setAllPocV2Weights(listOf(genesis, join1, join2), listOf(10, 10, 10))

        logSection("Creating challenge before cPoC is enabled")
        createChallengeFirst(genesis, target)
        val created = waitForOpenChallenge(genesis, target)
        requireLandedPunishable(created)
        Logger.info("Created challenge start=${created.startHeight} finish=${created.finish} epoch=${created.epochIndex}")

        logSection("Enabling saturated cPoC after create")
        enableConfirmationPoc(env.cluster, genesis, expectedConfirmationsPerEpoch = 1000)

        val committed = waitForChallengeCommit(genesis, target)
        assertThat(committed.commits.first().count).isEqualTo(10)
        requireEmitWindow(committed, genesis.getCurrentBlockHeight())
        logSection("Emitting a second batch on the first segment")
        join1.emitPocV2Batch()
        val accumulated = waitForChallengeCommitCount(
            genesis,
            target,
            count = 20,
            startHeight = created.startHeight,
            deadline = committed.finish - 1,
        )
        assertThat(accumulated.commits.first { it.count >= 20 }.count).isEqualTo(20)
        Logger.info("First segment accumulated count=20 start=${created.startHeight} finish=${committed.finish}")

        val confirmationEvent = waitForConfirmationPoCInEpoch(genesis, created.epochIndex)
        Logger.info("Confirmation PoC triggered at height ${confirmationEvent.triggerHeight}")

        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_GENERATION)
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_VALIDATION)
        waitForConfirmationPoCCompletion(genesis)

        val afterCpoc = waitForOpenChallenge(genesis, target)
        Logger.info(
            "After cPoC start=${afterCpoc.startHeight} generating=${afterCpoc.generating} " +
                "state=${afterCpoc.state}"
        )
        assertThat(afterCpoc.startHeight).isNotEqualTo(created.startHeight)
        assertThat(afterCpoc.generating).isTrue()
        assertThat(afterCpoc.isOpen()).isTrue()
        assertThat(genesis.node.getRawParticipants().getParticipant(join1)?.status).isEqualTo("ACTIVE")

        val rotated = waitForRotatedCommit(genesis, target, afterCpoc.startHeight)
        val rotatedCount = rotated.commits
            .filter { it.pocStageStartBlockHeight == afterCpoc.startHeight }
            .maxOf { it.count }
        assertThat(rotatedCount).isEqualTo(10)
    }
}
