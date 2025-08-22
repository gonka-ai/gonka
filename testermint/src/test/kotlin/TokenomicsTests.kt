import com.productscience.EpochStage
import com.productscience.LocalCluster
import com.productscience.data.AppState
import com.productscience.data.Decimal
import com.productscience.data.GenesisOnlyParams
import com.productscience.data.InferenceState
import com.productscience.data.spec
import com.productscience.inferenceConfig
import com.productscience.inferenceRequest
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.data.Offset
import org.junit.jupiter.api.Test

class TokenomicsTests : TestermintTest() {
    @Test
    fun createTopMiner() {
        // Disable power capping for this test to allow unlimited weight accumulation
        val noCappingSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::genesisOnlyParams] = spec<GenesisOnlyParams> {
                    this[GenesisOnlyParams::maxIndividualPowerPercentage] = Decimal.fromDouble(0.0) // Disable power capping
                }
            }
        }

        val noCappingConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(noCappingSpec) ?: noCappingSpec
        )

        val (_, genesis) = initCluster(config = noCappingConfig, reboot = true)
        logSection("Setting PoC weight to 100")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        genesis.changePoc(100)
        logSection("Verifying top miner added")
        val topMiners = genesis.node.getTopMiners()
        assertThat(topMiners.topMiner).hasSize(1)
        val topMiner = topMiners.topMiner.first()
        assertThat(topMiner.address).isEqualTo(genesis.node.getColdAddress())
        val startTime = topMiner.firstQualifiedStarted
        assertThat(topMiner.lastQualifiedStarted).isEqualTo(startTime)
        assertThat(topMiner.lastUpdatedTime).isEqualTo(startTime)
        logSection("Waiting for next Epoch")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        logSection("Verifying top miner updated")
        val topMiners2 = genesis.node.getTopMiners()
        assertThat(topMiners2.topMiner).hasSize(1)
        val topMiner2 = topMiners2.topMiner.first()
        assertThat(topMiner2.address).isEqualTo(genesis.node.getColdAddress())
        assertThat(topMiner2.firstQualifiedStarted).isEqualTo(startTime)
        assertThat(topMiner2.lastQualifiedStarted).isEqualTo(startTime)
        val epochLength = genesis.getParams().epochParams.epochLength
        // FIXME: try to use block timestamps to get a more precise expected time estimation
        assertThat(topMiner2.qualifiedTime).isCloseTo(epochLength * 5, Offset.offset(5))
        assertThat(topMiner2.lastUpdatedTime).isEqualTo(startTime + topMiner2.qualifiedTime!!)
    }

    @Test
    fun payTopMiner() {
        val fastRewardSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::genesisOnlyParams] = spec<GenesisOnlyParams> {
                    this[GenesisOnlyParams::topRewardPeriod] = 100L
                }
            }
        }

        val fastRewards = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(fastRewardSpec) ?: fastRewardSpec
        )
        val (localCluster, genesis) = initCluster(config = fastRewards, reboot = true)
        val firstJoin = localCluster.joinPairs.first()
        val initialBalance = firstJoin.node.getSelfBalance("ngonka")
        logSection("Setting PoC weight to 100")
        firstJoin.changePoc(100)
        val blockUntilReward = genesis.node.getGenesisState().appState.inference.genesisOnlyParams.topRewardPeriod / 5
        val settlesUntilReward = blockUntilReward / genesis.getParams().epochParams.epochLength
        logSection("Making Inferences")
        (0 until settlesUntilReward + 1).forEach { i ->
            logSection("Making set $i of ${settlesUntilReward + 1} inferences")
            // Odds of not getting either one of the requests or some of the validations are tiny
            genesis.makeInferenceRequest(inferenceRequest)
            genesis.makeInferenceRequest(inferenceRequest)
            genesis.makeInferenceRequest(inferenceRequest)
            logSection("Waiting for next Epoch")
            genesis.waitForStage(EpochStage.START_OF_POC)
            genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        }
        logSection("Verifying rewards")
        val topMiners = genesis.node.getTopMiners()
        assertThat(topMiners.topMiner).hasSize(1)
        val topMiner = topMiners.topMiner.first()
        assertThat(topMiner.address).isEqualTo(firstJoin.node.getColdAddress())
        val standardizedExpectedReward = getTopMinerReward(localCluster)
        val currentBalance = firstJoin.node.getSelfBalance("ngonka")
        // greater, because it's done validation work at some point, no doubt.
        assertThat(currentBalance - initialBalance).isGreaterThan(standardizedExpectedReward)
        
        // Mark for reboot to reset parameters for subsequent tests
        genesis.markNeedsReboot()
    }

    private fun getTopMinerReward(localCluster: LocalCluster): Long {
        val genesisState = localCluster.genesis.node.getGenesisState()
        val genesisParams = genesisState.appState.inference.genesisOnlyParams
        val expectedReward = genesisParams.topRewardAmount / genesisParams.topRewardPayouts
        val standardizedExpectedReward =
            genesisState.appState.bank.denomMetadata.first().convertAmount(expectedReward, genesisParams.supplyDenom)
        return standardizedExpectedReward
    }

}