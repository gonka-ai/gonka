import com.productscience.ApplicationCLI
import com.productscience.EpochStage
import com.productscience.data.StakeValidator
import com.productscience.data.StakeValidatorStatus
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class ParticipantPowerTests : TestermintTest() {
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
        zeroParticipant.changePoc(10, setNewValidatorsOffset = 3)

        logSection("Confirming ${zeroParticipant.name} is back in validators")
        val validatorsAfterRejoin = genesis.node.getValidators()
        val rejoinedValidator = validatorsAfterRejoin.validators.first {
            it.consensusPubkey.value == zeroParticipantKey.key
        }

        assertThat(rejoinedValidator.tokens).isEqualTo(10)
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
