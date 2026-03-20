import com.productscience.*
import com.productscience.data.DebugStatsResponse
import com.productscience.data.DeveloperInferencesResponse
import com.productscience.data.StatsModelsResponse
import com.productscience.data.StatsSummaryResponse
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.time.Duration
import java.util.concurrent.TimeUnit

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class StatsApiE2ETest : TestermintTest() {

    @Test
    fun `stats endpoints return data for ingested finished inferences`() {
        val (cluster, genesis) = initCluster(reboot = true)
        genesis.markNeedsReboot()
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)

        cluster.allPairs.forEach {
            it.mock?.setInferenceResponse(defaultInferenceResponseObject, Duration.ofSeconds(1))
        }

        val generated = runParallelInferencesWithResults(
            genesis = genesis,
            count = 6,
            waitForBlocks = 6,
            maxConcurrentRequests = 12,
        )
        assertThat(generated).hasSize(6)

        val developer = genesis.node.getColdAddress()
        val expectedIds = generated.map { it.inferenceId }.toSet()
        val expectedTokens = generated.sumOf {
            (it.promptTokenCount ?: 0).toLong() + (it.completionTokenCount ?: 0).toLong()
        }
        val timeFrom = generated.minOf { it.startBlockTimestamp } - 1_000
        val timeTo = generated.maxOf { it.endBlockTimestamp ?: it.startBlockTimestamp } + 1_000

        val snapshot = waitForStatsVisibility(
            genesis = genesis,
            developer = developer,
            expectedIds = expectedIds,
            timeFrom = timeFrom,
            timeTo = timeTo,
        )

        val observedDevIds = snapshot.developerInferences.stats
            .map { it.inference.inferenceId }
            .toSet()
        assertThat(observedDevIds).containsAll(expectedIds)

        val observedExpectedTokens = snapshot.developerInferences.stats
            .filter { expectedIds.contains(it.inference.inferenceId) }
            .sumOf { it.inference.totalTokenCount }
        assertThat(observedExpectedTokens).isEqualTo(expectedTokens)

        val expectedModel = inferenceRequestObject.model
        val modelStats = snapshot.models.statsModels.firstOrNull { it.model == expectedModel }
        assertThat(modelStats).isNotNull
        assertThat(modelStats!!.inferences).isGreaterThanOrEqualTo(expectedIds.size)
        assertThat(modelStats.aiTokens).isGreaterThanOrEqualTo(expectedTokens)

        assertThat(snapshot.summaryByTime.inferences).isGreaterThanOrEqualTo(expectedIds.size)
        assertThat(snapshot.summaryByTime.aiTokens).isGreaterThanOrEqualTo(expectedTokens)

        assertThat(snapshot.summaryByEpochs.inferences).isGreaterThanOrEqualTo(expectedIds.size)
        assertThat(snapshot.summaryByEpochs.aiTokens).isGreaterThanOrEqualTo(expectedTokens)

        assertThat(snapshot.developerSummaryByEpochs.inferences).isGreaterThanOrEqualTo(expectedIds.size)
        assertThat(snapshot.developerSummaryByEpochs.aiTokens).isGreaterThanOrEqualTo(expectedTokens)

        val debugIdsForDeveloper = snapshot.debug.statsByTime
            .firstOrNull { it.developer == developer }
            ?.stats
            ?.map { it.inference.inferenceId }
            ?.toSet()
            ?: emptySet()
        assertThat(debugIdsForDeveloper).containsAll(expectedIds)
    }

    private fun waitForStatsVisibility(
        genesis: LocalInferencePair,
        developer: String,
        expectedIds: Set<String>,
        timeFrom: Long,
        timeTo: Long,
    ): StatsSnapshot {
        var lastError: Throwable? = null
        var lastSnapshot: StatsSnapshot? = null

        repeat(40) { attempt ->
            try {
                val snapshot = StatsSnapshot(
                    models = genesis.api.getStatsModels(timeFrom, timeTo),
                    developerInferences = genesis.api.getStatsDeveloperInferences(developer, timeFrom, timeTo),
                    developerSummaryByEpochs = genesis.api.getStatsDeveloperSummaryEpochs(developer, 5),
                    summaryByEpochs = genesis.api.getStatsSummaryEpochs(5),
                    summaryByTime = genesis.api.getStatsSummaryTime(timeFrom, timeTo),
                    debug = genesis.api.getStatsDebugDevelopers(),
                )
                lastSnapshot = snapshot

                val observedIds = snapshot.developerInferences.stats
                    .map { it.inference.inferenceId }
                    .toSet()
                if (observedIds.containsAll(expectedIds)) {
                    return snapshot
                }
            } catch (err: Throwable) {
                lastError = err
            }

            logSection("Stats ingestion not complete yet (attempt ${attempt + 1}/40), waiting 2s")
            Thread.sleep(Duration.ofSeconds(2))
        }

        if (lastError != null) {
            throw IllegalStateException("Failed waiting for stats ingestion to become visible", lastError)
        }
        error("Timed out waiting for stats ingestion; last snapshot=$lastSnapshot")
    }

    private data class StatsSnapshot(
        val models: StatsModelsResponse,
        val developerInferences: DeveloperInferencesResponse,
        val developerSummaryByEpochs: StatsSummaryResponse,
        val summaryByEpochs: StatsSummaryResponse,
        val summaryByTime: StatsSummaryResponse,
        val debug: DebugStatsResponse,
    )
}
