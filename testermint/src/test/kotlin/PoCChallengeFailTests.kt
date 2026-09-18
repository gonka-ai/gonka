import com.productscience.EpochStage
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit

@Timeout(value = 20, unit = TimeUnit.MINUTES)
class PoCChallengeFailTests : TestermintTest() {
    @Test
    fun `last-segment challenge fail refunds lock and inactivates target`() {
        logSection("=== TEST: PoCChallenge last-segment fail ===")
        val env = bootPoCChallengeCluster(
            expectedConfirmationsPerEpoch = 0,
            pocStageDuration = 10,
            alphaThreshold = 0.70,
        )
        val genesis = env.genesis
        val join1 = env.join1
        val join2 = env.join2
        val target = join1.node.getColdAddress()

        logSection("Setting fail v2 weights before create")
        setAllPocV2Weights(listOf(genesis, join1, join2), listOf(10, 3, 10))

        val join1BeforeCreate = join1.node.getSelfBalance()
        val join2BeforeCreate = join2.node.getSelfBalance()

        logSection("Creating challenge at last-segment height")
        createAtLastSegment(genesis, target)
        val open = waitForOpenChallenge(genesis, target)
        requireLandedPunishable(open)
        val locked = open.lockedPayment
        assertThat(locked).isGreaterThan(0)
        Logger.info("Locked payment P=$locked epoch=${open.epochIndex}")

        val genesisAfterCreate = genesis.node.getSelfBalance()
        val committed = waitForChallengeCommit(genesis, target)
        assertThat(committed.commits.first().count).isEqualTo(3)
        val snapshot = snapshotChallengeAtEndOfPoCValidation(genesis, target)
        val join1AtSnap = participantAtHeight(genesis, join1, snapshot.height)
        Logger.info("Snapshot failure=${snapshot.challenge.failureKind} join1 status=${join1AtSnap?.status}")
        assertThat(snapshot.challenge.isChallengeFailed()).isTrue()
        assertThat(join1AtSnap?.status).isEqualTo("INACTIVE")

        logSection("Waiting for settlement")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)
        assertThat(challengeOf(genesis, target)).isNull()

        val genesisAfter = genesis.node.getSelfBalance()
        val join1After = join1.node.getSelfBalance()
        val join2After = join2.node.getSelfBalance()
        val genesisDelta = genesisAfter - genesisAfterCreate
        val join1Delta = join1After - join1BeforeCreate
        val join2Delta = join2After - join2BeforeCreate
        Logger.info("Deltas genesis=$genesisDelta join1=$join1Delta join2=$join2Delta")

        assertThat(join2Delta).isGreaterThan(0)
        assertThat(join1Delta).isCloseTo(0, Offset.offset(2L))
        assertThat(genesisDelta).isCloseTo(join2Delta + locked, Offset.offset(5L))
    }
}
