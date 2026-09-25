import com.productscience.EpochStage
import com.productscience.assertions.assertThat
import com.productscience.contractArtifactPath
import com.productscience.data.AppState
import com.productscience.data.RestrictionsParams
import com.productscience.data.RestrictionsState
import com.productscience.data.UnfundedInferenceParticipant
import com.productscience.data.spec
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.installWasmArtifact
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit

@Timeout(value = 20, unit = TimeUnit.MINUTES)
class ClaimRecipientTests : TestermintTest() {
    @Test
    fun `claim rewards can be routed to configured recipient`() {
        val (cluster, genesis) = initCluster(config = claimRecipientConfig, reboot = true)
        cluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }

        logSection("Clear pending claims before configuring recipient")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 3)

        val participantPair = cluster.joinPairs.first()
        val participant = participantPair.node.getColdAddress()
        val recipient = genesis.node.createKey("claim-recipient-${System.currentTimeMillis()}").address
        val targetEpoch = genesis.getEpochData().latestEpoch.index + 1
        val recipientBalanceBefore = genesis.getBalance(recipient)

        logSection("Configure recipient for epoch $targetEpoch")
        val setRecipient = participantPair.node.setClaimRecipients(claimRecipientsJson(targetEpoch, recipient))
        assertThat(setRecipient).isSuccess()
        assertThat(genesis.node.listClaimRecipients(participant).entries)
            .anyMatch { it.epoch == targetEpoch && it.recipient == recipient }

        logSection("Wait until target epoch $targetEpoch is active")
        while (genesis.getEpochData().latestEpoch.index < targetEpoch) {
            genesis.waitForNextEpoch()
        }
        val rewardSeed = participantPair.api.getConfig().currentSeed

        genesis.markNeedsReboot()
        participantPair.stopApiContainer()
        logSection("Stopped participant API to prevent auto-claim before manual verification")

        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)
        val claimResponse = participantPair.submitTransaction(
            listOf(
                "inference",
                "claim-rewards",
                rewardSeed.seed.toString(),
                rewardSeed.epochIndex.toString(),
            )
        )
        assertThat(claimResponse).isSuccess()

        val recipientBalanceAfter = genesis.getBalance(recipient)
        assertThat(recipientBalanceAfter)
            .`as`("configured recipient receives the claim payout")
            .isGreaterThan(recipientBalanceBefore)
        assertThat(genesis.node.listClaimRecipients(participant).entries)
            .`as`("recipient entry is retained after claim for late same-epoch payouts")
            .anyMatch { it.epoch == targetEpoch && it.recipient == recipient }
    }

    @Test
    fun `claim recipient pruning waits until epoch is safely stale`() {
        val (cluster, genesis) = initCluster(config = claimRecipientConfig, reboot = true)
        cluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }

        val participantKey = genesis.node.createKey("claim-recipient-prune-participant-${System.currentTimeMillis()}")
        val participant = participantKey.address
        genesis.api.addUnfundedInferenceParticipant(
            UnfundedInferenceParticipant(
                url = "",
                models = listOf(),
                validatorKey = "",
                pubKey = participantKey.pubkey.key,
                address = participant
            )
        )
        genesis.node.waitForNextBlock(2)

        val recipient = genesis.node.createKey("claim-recipient-prune-${System.currentTimeMillis()}").address
        val targetEpoch = genesis.getEpochData().latestEpoch.index + 1

        logSection("Configure recipient for inactive participant epoch $targetEpoch")
        val setRecipient = genesis.node.setClaimRecipients(
            claimRecipientsJson(targetEpoch, recipient),
            from = participantKey.name
        )
        assertThat(setRecipient).isSuccess()
        assertThat(genesis.node.listClaimRecipients(participant).entries)
            .anyMatch { it.epoch == targetEpoch && it.recipient == recipient }

        logSection("Advance until target epoch is claimable, but not pruneable")
        while (genesis.getEpochData().latestEpoch.index < targetEpoch + 1) {
            genesis.waitForNextEpoch()
        }
        assertThat(genesis.node.listClaimRecipients(participant).entries)
            .`as`("recipient entry is still present while the epoch is only one epoch old")
            .anyMatch { it.epoch == targetEpoch && it.recipient == recipient }

        logSection("Advance until target epoch is past the pruning threshold")
        while (genesis.getEpochData().latestEpoch.index < targetEpoch + 5) {
            genesis.waitForNextEpoch()
        }
        genesis.node.waitForNextBlock(2)

        assertThat(genesis.node.listClaimRecipients(participant).entries)
            .`as`("recipient entry is pruned only after it is safely stale")
            .noneMatch { it.epoch == targetEpoch && it.recipient == recipient }
    }

    /**
     * The point of the recipient schedule: a scheduled recipient does not have
     * to be a wallet. Here the epoch's rewards are routed to an immutable
     * splitter contract, which then fans them out to several payees — a payout
     * split that the chain itself has no notion of.
     *
     * Note the two-step nature: crediting the contract does not run it. A bank
     * transfer has no entry point in CosmWasm, so the rewards sit on the
     * contract's balance until someone cranks `distribute`.
     *
     * Requires the artifact:
     *   cd inference-chain/contracts/reward-splitter && ./build.sh
     */
    @Test
    fun `scheduled recipient can be a contract that splits rewards between payees`() {
        val (cluster, genesis) = initCluster(config = claimRecipientConfig, reboot = true)
        cluster.allPairs.forEach { pair ->
            pair.waitForMlNodesToLoad()
        }

        logSection("Deploy a 7:3 splitter")
        val containerPath = genesis.installWasmArtifact(
            contractArtifactPath("reward-splitter", "reward_splitter.wasm")
        )
        val storeTx = genesis.node.storeWasmCode(containerPath)
        assertThat(storeTx.code).`as`("store code tx: ${storeTx.rawLog}").isEqualTo(0)

        val suffix = System.currentTimeMillis()
        val alpha = genesis.node.createKey("split-alpha-$suffix").address
        val beta = genesis.node.createKey("split-beta-$suffix").address
        val instantiateTx = genesis.node.instantiateWasmContract(
            codeId = storeTx.getCodeId()!!,
            initMsg = """{"payees":[{"address":"$alpha","shares":7},{"address":"$beta","shares":3}]}""",
            label = "claim-recipient-splitter-$suffix",
            noAdmin = true,
        )
        assertThat(instantiateTx.code).`as`("instantiate tx: ${instantiateTx.rawLog}").isEqualTo(0)
        val splitter = instantiateTx.getContractAddress()!!

        logSection("Clear pending claims before scheduling")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 3)

        val participantPair = cluster.joinPairs.first()
        val participant = participantPair.node.getColdAddress()
        val targetEpoch = genesis.getEpochData().latestEpoch.index + 1

        logSection("Schedule the splitter as recipient for epoch $targetEpoch")
        val setRecipient = participantPair.node.setClaimRecipients(claimRecipientsJson(targetEpoch, splitter))
        assertThat(setRecipient).isSuccess()
        assertThat(genesis.node.listClaimRecipients(participant).entries)
            .anyMatch { it.epoch == targetEpoch && it.recipient == splitter }

        logSection("Wait until target epoch $targetEpoch is active")
        while (genesis.getEpochData().latestEpoch.index < targetEpoch) {
            genesis.waitForNextEpoch()
        }
        val rewardSeed = participantPair.api.getConfig().currentSeed

        genesis.markNeedsReboot()
        participantPair.stopApiContainer()
        logSection("Stopped participant API to prevent auto-claim before manual verification")

        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)
        val claimResponse = participantPair.submitTransaction(
            listOf(
                "inference",
                "claim-rewards",
                rewardSeed.seed.toString(),
                rewardSeed.epochIndex.toString(),
            )
        )
        assertThat(claimResponse).isSuccess()

        val received = genesis.getBalance(splitter)
        assertThat(received)
            .`as`("rewards land on the contract, which the transfer does not execute")
            .isGreaterThan(0L)

        val alphaBefore = genesis.getBalance(alpha)
        val betaBefore = genesis.getBalance(beta)

        logSection("Crank the splitter")
        val distributeTx = genesis.node.executeWasmContract(splitter, """{"distribute":{}}""")
        assertThat(distributeTx.code).`as`("distribute tx: ${distributeTx.rawLog}").isEqualTo(0)

        val alphaShare = received * 7 / 10
        val betaShare = received * 3 / 10
        assertThat(genesis.getBalance(alpha) - alphaBefore)
            .`as`("alpha receives floor(7/10) of the claimed rewards")
            .isEqualTo(alphaShare)
        assertThat(genesis.getBalance(beta) - betaBefore)
            .`as`("beta receives floor(3/10) of the claimed rewards")
            .isEqualTo(betaShare)
        assertThat(genesis.getBalance(splitter))
            .`as`("only the rounding remainder stays behind")
            .isEqualTo(received - alphaShare - betaShare)
    }

    companion object {
        /**
         * One genesis for the whole class, so no test forces an extra cluster
         * rebuild just by differing in configuration.
         *
         * The bootstrap transfer restriction is lifted because the splitter
         * test needs it: paying a contract is a module operation and is always
         * allowed, but the contract paying its payees out is a user-to-user
         * transfer, which `x/restrictions` rejects until block 1555000. The
         * other tests are indifferent to it — their payouts come from a module
         * account either way.
         */
        private val claimRecipientConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(
                spec {
                    this[AppState::restrictions] = spec<RestrictionsState> {
                        this[RestrictionsState::params] = spec<RestrictionsParams> {
                            this[RestrictionsParams::restrictionEndBlock] = 0L
                        }
                    }
                }
            )
        )

        private fun claimRecipientsJson(epoch: Long, recipient: String): String =
            """[{"epoch":$epoch,"recipient":"$recipient"}]"""
    }
}
