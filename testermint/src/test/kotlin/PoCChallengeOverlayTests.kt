import com.productscience.data.ConfirmationPoCPhase
import com.productscience.data.getParticipant
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit

@Timeout(value = 20, unit = TimeUnit.MINUTES)
class PoCChallengeOverlayTests : TestermintTest() {
    @Test
    fun `create first then confirmation PoC rotates and skips target`() {
        logSection("=== TEST: PoCChallenge overlay create-first ===")
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
        val confirmationEvent = waitForConfirmationPoCInEpoch(genesis, created.epochIndex)
        Logger.info("Confirmation PoC triggered at height ${confirmationEvent.triggerHeight}")

        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_GENERATION)
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_VALIDATION)
        waitForConfirmationPoCCompletion(genesis)

        val afterCpoc = waitForOpenChallenge(genesis, target)
        Logger.info(
            "After cPoC start=${afterCpoc.startHeight} generating=${afterCpoc.generating} " +
                "failure=${afterCpoc.failureKind}"
        )
        assertThat(afterCpoc.startHeight).isNotEqualTo(created.startHeight)
        assertThat(afterCpoc.generating).isTrue()
        assertThat(afterCpoc.isUnsetFailure()).isTrue()
        assertThat(genesis.node.getRawParticipants().getParticipant(join1)?.status).isEqualTo("ACTIVE")

        val rotated = waitForRotatedCommit(genesis, target, afterCpoc.startHeight)
        assertThat(rotated.commits.any { it.pocStageStartBlockHeight == afterCpoc.startHeight }).isTrue()
    }
}
