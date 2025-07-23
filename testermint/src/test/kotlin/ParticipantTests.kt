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
import org.junit.jupiter.api.Test
import org.tinylog.kotlin.Logger
import java.time.Duration
import kotlin.test.assertNotNull

class ParticipantTests : TestermintTest() {
    @Test
    fun `reputation increases after epoch participation`() {
        val (_, genesis) = initCluster()
        genesis.waitForNextInferenceWindow()

        val startStats = genesis.node.getParticipantCurrentStats()
        logSection("Running inferences")
        runParallelInferences(genesis, 10)
        logSection("Waiting for next epoch")
        genesis.waitForNextInferenceWindow()
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

    @Test
    fun `power to zero removes participant from validators`() {
        val (cluster, genesis) = initCluster()
        genesis.markNeedsReboot()
        val zeroParticipant = cluster.joinPairs.first()
        logSection("Setting ${zeroParticipant.name} to 0 power")
        val zeroParticipantKey = zeroParticipant.node.getValidatorInfo()
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        zeroParticipant.changePoc(0, setNewValidatorsOffset = 3)
        logSection("Confirming ${zeroParticipant.name} is removed from validators")
        val validatorsAfter = genesis.node.getValidators()
        val zeroValidator = validatorsAfter.validators.first {
            it.consensusPubkey.value == zeroParticipantKey.key
        }
        assertThat(zeroValidator.tokens).isZero
        assertThat(zeroValidator.status).isEqualTo(StakeValidatorStatus.UNBONDING.value)
        val cometValidators = genesis.node.getCometValidators()
        assertThat(cometValidators.validators).noneMatch {
            it.pubKey.key == zeroParticipantKey.key
        }
        assertThat(cometValidators.validators).hasSize(2)
    }

    @Test
    fun `power to zero and back again restores validator`() {
        val (cluster, genesis) = initCluster()
        val zeroParticipant = cluster.joinPairs.first()
        logSection("Setting ${zeroParticipant.name} to 0 power")
        val zeroParticipantKey = zeroParticipant.node.getValidatorInfo()
        val participants = genesis.api.getParticipants()
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        genesis.markNeedsReboot()
        // Looks like comet validators will only be changed with a 2 block more
        // setNewValidators -- EndBlock: change module state: active participants, epoch groups
        // setNewValidators + 1 -- EndBlock: epoch group change is detected and a call to staking is made
        // setNewValidators + 2 -- staking module update validator update is visible
        // setNewValidators + 3 -- the staking update is propagated to comet
        zeroParticipant.changePoc(0, setNewValidatorsOffset = 3)
        logSection("Confirming ${zeroParticipant.name} is removed from validators")
        val validatorsAfter = genesis.node.getValidators()
        val zeroValidator = validatorsAfter.validators.first {
            it.consensusPubkey.value == zeroParticipantKey.key
        }
        assertThat(zeroValidator.tokens).isZero
        assertThat(zeroValidator.status).isEqualTo(StakeValidatorStatus.UNBONDING.value)
        // Ideally just add here smth like "wait for 1 block?"
        val cometValidators = genesis.node.getCometValidators()
        assertThat(cometValidators.validators).noneMatch {
            it.pubKey.key == zeroParticipantKey.key
        }
        assertThat(cometValidators.validators).hasSize(2)

        logSection("Setting ${zeroParticipant.name} back to 15 power")
        zeroParticipant.changePoc(15, setNewValidatorsOffset = 3)

        logSection("Confirming ${zeroParticipant.name} is back in validators")
        val validatorsAfterRejoin = genesis.node.getValidators()
        val rejoinedValidator = validatorsAfterRejoin.validators.first {
            it.consensusPubkey.value == zeroParticipantKey.key
        }

        assertThat(rejoinedValidator.tokens).isEqualTo(15)
        assertThat(rejoinedValidator.status).isEqualTo(StakeValidatorStatus.BONDED.value)
        val cometValidatorsAfterRejoin = genesis.node.getCometValidators()
        assertThat(cometValidatorsAfterRejoin.validators).anyMatch {
            it.pubKey.key == zeroParticipantKey.key
        }
        assertThat(cometValidatorsAfterRejoin.validators).hasSize(3)
    }

    @Test
    fun `change a participants power`() {
        val (_, genesis) = initCluster(reboot = true)
        logSection("Changing ${genesis.name} power to 11")
        genesis.changePoc(11)
        logSection("Verifying change")
        val tokensAfterChange = genesis.node.getStakeValidator().tokens

        logSection("Changing ${genesis.name} power back to 10")
        genesis.changePoc(10)

        logSection("Verifying change back")
        val updatedGenesisTokens = genesis.node.getStakeValidator().tokens

        assertThat(updatedGenesisTokens).isEqualTo(10)
        assertThat(tokensAfterChange).isEqualTo(11)
    }
}

fun ApplicationCLI.getStakeValidator(): StakeValidator {
    val validators = getValidators()
    val valKey = getValidatorInfo().key
    val validator = validators.validators.first { it.consensusPubkey.value == valKey }
    return validator
}
