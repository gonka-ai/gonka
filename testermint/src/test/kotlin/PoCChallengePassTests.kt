import com.productscience.EpochStage
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit

@Timeout(value = 20, unit = TimeUnit.MINUTES)
class PoCChallengePassTests : TestermintTest() {
    @Test
    fun `last-segment challenge pass pays lock to target`() {
        logSection("=== TEST: PoCChallenge last-segment pass ===")
        val env = bootPoCChallengeCluster(
            expectedConfirmationsPerEpoch = 0,
            pocStageDuration = 10,
            alphaThreshold = 0.70,
        )
        val genesis = env.genesis
        val join1 = env.join1
        val join2 = env.join2
        val target = join1.node.getColdAddress()

        logSection("Setting honest v2 weights before create")
        setAllPocV2Weights(listOf(genesis, join1, join2), listOf(10, 10, 10))

        val denied = join1.createPoCChallenge(join2.node.getColdAddress())
        assertThat(denied.code).isNotEqualTo(0)

        val genesisBeforeCreate = genesis.node.getSelfBalance()
        val join1BeforeCreate = join1.node.getSelfBalance()
        val join2BeforeCreate = join2.node.getSelfBalance()

        logSection("Creating challenge at last-segment height")
        createAtLastSegment(genesis, target)
        val open = waitForOpenChallenge(genesis, target)
        requireLandedPunishable(open)
        val locked = open.lockedPayment
        assertThat(locked).isGreaterThan(0)
        Logger.info("Locked payment P=$locked epoch=${open.epochIndex} start=${open.startHeight} finish=${open.finish}")

        val dup = genesis.createPoCChallenge(target)
        assertThat(dup.code).isNotEqualTo(0)

        val genesisAfterCreate = genesis.node.getSelfBalance()
        assertThat(genesisBeforeCreate - genesisAfterCreate).isCloseTo(locked, Offset.offset(1L))

        val committed = waitForChallengeCommit(genesis, target)
        assertThat(committed.commits.first().count).isEqualTo(10)

        val tooLate = genesis.getEpochData().epochStages.nextPocStart - 5
        genesis.node.waitForMinimumBlock(tooLate, "remaining floor")
        val shortWindow = genesis.createPoCChallenge(join2.node.getColdAddress())
        assertThat(shortWindow.code).isNotEqualTo(0)

        val snapshot = snapshotChallengeAtEndOfPoCValidation(genesis, target)
        assertThat(snapshot.challenge.isPassed()).isTrue()
        // decide deletes segment commits; SettleAccounts wipes ConfirmationPoCRatio in the same block
        assertThat(snapshot.challenge.commits).isEmpty()
        val join1Cw = confirmationWeight(genesis, snapshot.challenge.epochIndex, target)
        Logger.info("Snapshot state=${snapshot.challenge.state} join1 confirmationWeight=$join1Cw")
        assertThat(join1Cw).isCloseTo(10, Offset.offset(1L))

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

        assertThat(genesisDelta).isGreaterThan(0)
        assertThat(join1Delta).isGreaterThan(0)
        assertThat(join2Delta).isGreaterThan(0)
        assertThat(genesisDelta).isCloseTo(join2Delta, Offset.offset(5L))
        assertThat(join1Delta).isCloseTo(join2Delta + locked, Offset.offset(5L))
    }
}
