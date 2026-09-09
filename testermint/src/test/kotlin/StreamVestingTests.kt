import com.productscience.*
import com.productscience.data.spec
import com.productscience.data.AppState
import com.productscience.data.InferenceState
import com.productscience.data.InferenceParams
import com.productscience.data.EpochParams
import com.productscience.data.TokenomicsParams
import java.time.Duration
import java.net.SocketException
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.junit.jupiter.api.Assumptions
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger

class StreamVestingTests : TestermintTest() {
    private fun initVestingCluster(config: ApplicationConfig): Pair<LocalCluster, LocalInferencePair> {
        var lastFailure: Throwable? = null
        repeat(3) { attempt ->
            try {
                return initCluster(config = config, reboot = true)
            } catch (t: Throwable) {
                val shouldRetry =
                    t is IllegalStateException ||
                        generateSequence(t) { it.cause }.any { it is SocketException }
                if (!shouldRetry || attempt == 2) {
                    throw t
                }
                lastFailure = t
                Logger.warn("Stream vesting cluster bootstrap failed on attempt ${attempt + 1}, retrying: ${t.message}", "")
                Thread.sleep(Duration.ofSeconds(10))
            }
        }
        throw lastFailure ?: IllegalStateException("Stream vesting cluster bootstrap failed")
    }

    /**
     * PoC-driven vesting test. Bitcoin-style epoch rewards are unconditional
     * (paid for PoC weight at CLAIM_REWARDS, no inference traffic required), so
     * this drives no traffic at all and verifies, across one full reward cycle:
     *
     *  1. New epoch rewards land in the vesting schedule, not the liquid balance.
     *  2. The liquid balance grows by exactly the previously-scheduled first-epoch
     *     unlock (read from the schedule beforehand — no reward prediction needed).
     *  3. Aggregation: the new schedule's first epoch = old second epoch + first
     *     half of the new reward; second epoch = second half of the new reward
     *     (computed with calculateVestingScheduleChanges over an empty inference
     *     list, which yields pure PoC epoch-reward vesting).
     *
     * Replaces the classic-inference-driven version removed with PR #1386; that
     * version was also historically flaky because its expected-balance arithmetic
     * depended on per-inference cost settlement.
     */
    @Test
    fun `epoch rewards vest and unlock without inference traffic`() {
        // Configure genesis with 2-epoch vesting periods for fast testing
        val fastVestingSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::params] = spec<InferenceParams> {
                    this[InferenceParams::tokenomicsParams] = spec<TokenomicsParams> {
                        this[TokenomicsParams::workVestingPeriod] = 2L
                        this[TokenomicsParams::rewardVestingPeriod] = 2L
                    }
                    this[InferenceParams::epochParams] = spec<EpochParams> {
                        this[EpochParams::epochLength] = 25L
                    }
                }
            }
        }

        val fastVestingConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(fastVestingSpec) ?: fastVestingSpec
        )

        val (_, genesis) = initVestingCluster(fastVestingConfig)

        val params = genesis.node.getInferenceParams().params
        // Legacy (pre-Bitcoin) rewards were per-inference and cannot be exercised
        // without the removed classic inference flow; Bitcoin rewards are the
        // active system everywhere this test matters.
        Assumptions.assumeTrue(
            isBitcoinRewardsEnabled(params.bitcoinRewardParams),
            "Bitcoin rewards disabled; legacy per-inference vesting is untestable without classic inference"
        )

        val participantAddress = genesis.node.getColdAddress()

        logSection("Aligning to a post-CLAIM_REWARDS observation point")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)
        genesis.node.waitForNextBlock(2)

        logSection("Snapshotting balance and vesting schedule")
        val initialBalance = genesis.getBalance(participantAddress)
        val initialSchedule = genesis.node.queryVestingSchedule(participantAddress)
        val initialFirstEpoch = initialSchedule.vestingSchedule?.epochAmounts?.getOrNull(0)
            ?.coins?.sumOf { it.amount } ?: 0L
        val initialSecondEpoch = initialSchedule.vestingSchedule?.epochAmounts?.getOrNull(1)
            ?.coins?.sumOf { it.amount } ?: 0L
        val startEpoch = getRewardCalculationEpochIndex(genesis)
        logHighlight(
            "Initial state: balance=$initialBalance, vesting first=$initialFirstEpoch, " +
                "second=$initialSecondEpoch, rewardEpoch=$startEpoch"
        )

        logSection("Waiting one full reward cycle with zero inference traffic")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)
        genesis.node.waitForNextBlock(2)

        val endEpoch = getRewardCalculationEpochIndex(genesis)
        // The unlock/aggregation arithmetic below assumes exactly one CLAIM_REWARDS
        // cycle elapsed between the two snapshots.
        assertThat(endEpoch)
            .describedAs("expected exactly one reward cycle between snapshots")
            .isEqualTo(startEpoch + 1)

        logSection("Computing expected PoC epoch reward (no inferences)")
        val expectedSchedules = calculateVestingScheduleChanges(
            inferences = emptyList(),
            inferenceParams = params,
            participants = genesis.api.getParticipants(),
            startLastRewardedEpoch = startEpoch,
            endLastRewardedEpoch = endEpoch,
            vestingPeriod = 2
        )
        val expected = expectedSchedules[participantAddress] ?: LongArray(3) { 0L }
        val expectedNewFirst = expected[1]
        val expectedNewSecond = expected[2]
        val expectedNewReward = expectedNewFirst + expectedNewSecond
        logHighlight("Expected new epoch reward: $expectedNewReward (first=$expectedNewFirst, second=$expectedNewSecond)")

        logSection("Verifying rewards flowed with zero traffic and went to vesting")
        assertThat(expectedNewReward)
            .describedAs("PoC epoch rewards should accrue without any inference traffic")
            .isGreaterThan(0)

        logSection("Verifying balance grew by exactly the scheduled first-epoch unlock")
        val balanceAfterCycle = genesis.getBalance(participantAddress)
        assertThat(balanceAfterCycle - initialBalance)
            .describedAs("liquid balance change must equal the previously scheduled first-epoch unlock")
            .isCloseTo(initialFirstEpoch, Offset.offset(3L))

        logSection("Verifying vesting schedule aggregation")
        val scheduleAfterCycle = genesis.node.queryVestingSchedule(participantAddress)
        val epochAmounts = scheduleAfterCycle.vestingSchedule?.epochAmounts
        assertThat(epochAmounts)
            .describedAs("new rewards must create/extend a vesting schedule")
            .isNotEmpty()
        assertThat(epochAmounts).hasSize(2)

        val newFirstEpoch = epochAmounts?.getOrNull(0)?.coins?.sumOf { it.amount } ?: 0L
        val newSecondEpoch = epochAmounts?.getOrNull(1)?.coins?.sumOf { it.amount } ?: 0L
        logHighlight("Schedule after cycle: first=$newFirstEpoch, second=$newSecondEpoch")

        // First epoch slot = what was previously second + first half of the new reward.
        // Second epoch slot = second half of the new reward. Size stays 2 (aggregation,
        // not extension).
        assertThat(newFirstEpoch)
            .describedAs("first epoch = old second epoch + new reward's first half")
            .isCloseTo(initialSecondEpoch + expectedNewFirst, Offset.offset(3L))
        assertThat(newSecondEpoch)
            .describedAs("second epoch = new reward's second half")
            .isCloseTo(expectedNewSecond, Offset.offset(3L))

        logSection("=== POC-DRIVEN VESTING TEST COMPLETED ===")
        logHighlight("✅ Epoch rewards accrued with zero inference traffic")
        logHighlight("✅ Rewards vested instead of paying out immediately")
        logHighlight("✅ Exactly the scheduled first-epoch amount unlocked to liquid balance")
        logHighlight("✅ New rewards aggregated into the 2-epoch schedule without extending it")
    }
}
