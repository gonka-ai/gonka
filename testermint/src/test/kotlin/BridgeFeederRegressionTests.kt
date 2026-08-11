import com.productscience.EpochStage
import com.productscience.data.AppState
import com.productscience.data.BridgeBlockRequest
import com.productscience.data.BridgeContractAddressEntry
import com.productscience.data.BridgeReceiptRequest
import com.productscience.data.BridgeState
import com.productscience.data.ConfirmationPoCPhase
import com.productscience.data.InferenceState
import com.productscience.data.spec
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit

/**
 * Regression for the Kaitaku bridge-feeder stall (#1478 gap):
 * mid-epoch RemoveMember leaves IsActiveParticipant true, so the DAPI still
 * BroadcastTxSyncs MsgBridgeExchange. Without CheckTx ante, Sync returns Code=0,
 * confirm waits forever, and latest never advances.
 *
 * With BridgeExchangeEarlyRejectDecorator, CheckTx fails → Sync Code!=0 →
 * classifyBridgeExchangeSubmit skip → all APIs advance even when only in-group
 * validators signed on-chain.
 *
 * Receipts use the WGNK→GNK unwrap path: bridge contract is seeded in genesis
 * (`inference.bridge.contract_addresses`) and bridge_escrow is funded via bank
 * send (jq genesis merge replaces bank.balances arrays, so escrow cannot be
 * safely appended via Spec).
 *
 * Requires images built from *this* tree first: `cd testermint && ./prepare.sh`
 */
@Timeout(value = 25, unit = TimeUnit.MINUTES)
class BridgeFeederRegressionTests : TestermintTest() {

    // Cluster bring-up + confirmation PoC kick dominates wall time (~12–20m).
    @Test
    fun `kicked validator advances bridge cursor while only in-group votes land`() {
        logSection("=== Bridge feeder: mid-epoch kick + cursor advance ===")

        val chain = "ethereum"
        // Seeded in genesis → HandleNativeTokenRelease (WGNK unwrap).
        val contract = "0xbridgefeederregression000000000000000001"

        val confirmationSpec = createConfirmationPoCSpec(
            expectedConfirmationsPerEpoch = 1000,
            alphaThreshold = 0.5,
        )
        val bridgeGenesisSpec = confirmationSpec.merge(
            spec {
                this[AppState::inference] = spec<InferenceState> {
                    this[InferenceState::bridge] = BridgeState(
                        contractAddresses = listOf(
                            BridgeContractAddressEntry(
                                chainId = chain,
                                address = contract,
                            ),
                        ),
                    )
                }
            },
        )
        val (cluster, genesis) = initCluster(
            joinCount = 2,
            mergeSpec = bridgeGenesisSpec,
            reboot = true,
            resetMlNodes = false,
        )
        val join1 = cluster.joinPairs[0] // C — will be kicked
        val join2 = cluster.joinPairs[1]
        val allPairs = listOf(genesis, join1, join2)

        logSection("Establish first epoch weights")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 2)

        // Deterministic secp256k1 compressed pubkey (PubKeyToAddress-compatible).
        val ownerPubKey = "034daac7b5be60532b562b030824b2c9b7c450985b894dd5544f9c7dd807d8bb88"
        val ownerAddress = "unused-by-dapi-derives-from-pubkey"

        logSection("Preset bridge_escrow for WGNK unwrap (bank send)")
        // bridge_escrow is not in blocked-addrs; plain bank send works.
        val escrowAddr = genesis.node.getModuleAccount("bridge_escrow").account.value.address
        val escrowFund = 1_000_000L
        genesis.ensureGenesisSpendableForDevshard(escrowFund)
        val fundResp = genesis.submitTransaction(
            listOf(
                "bank", "send",
                genesis.node.getColdAddress(),
                escrowAddr,
                "$escrowFund${genesis.config.denom}",
            ),
        )
        assertThat(fundResp.code).isEqualTo(0)
        val escrowBal = genesis.node.getBalance(escrowAddr, genesis.config.denom).balance.amount
        assertThat(escrowBal).isGreaterThanOrEqualTo(escrowFund)
        Logger.info("bridge_escrow funded: addr={} balance={}", escrowAddr, escrowBal)

        fun receipt(amount: String, receiptIndex: String) = BridgeReceiptRequest(
            contractAddress = contract,
            ownerAddress = ownerAddress,
            ownerPubKey = ownerPubKey,
            amount = amount,
            receiptIndex = receiptIndex,
        )

        fun postToAll(blockNumber: Long, receipts: List<BridgeReceiptRequest> = emptyList()) {
            val body = BridgeBlockRequest(
                blockNumber = blockNumber.toString(),
                originChain = chain,
                receiptsRoot = "0xroot$blockNumber",
                receipts = receipts,
            )
            // Sequential: each admin POST holds the HTTP request until drain finishes
            // (broadcast + up to 60s confirm). Parallel POSTs amplify mempool contention.
            allPairs.forEach { pair ->
                Logger.info("POST bridge/block={} to {}", blockNumber, pair.name)
                try {
                    pair.api.postBridgeBlock(body)
                } catch (e: Exception) {
                    throw IllegalStateException(
                        "postBridgeBlock failed on ${pair.name} block=$blockNumber: ${e.message}",
                        e,
                    )
                }
            }
        }

        fun awaitLatest(expected: Long, timeoutSec: Long = 180) {
            val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(timeoutSec)
            while (System.nanoTime() < deadline) {
                val latests = allPairs.map { it.api.getLatestBridgeBlock(chain).blockNumber }
                Logger.info("Bridge latest per node: $latests (want $expected)")
                if (latests.all { it == expected }) {
                    return
                }
                genesis.node.waitForNextBlock(1)
            }
            error("Timed out waiting for all APIs latest=$expected; got=${
                allPairs.map { it.api.getLatestBridgeBlock(chain).blockNumber }
            }")
        }

        // DAPI bootstraps after minBlocksToBootstrap=6 buffered blocks.
        val bootstrapStart = 1_000_000L
        logSection("Bootstrap bridge queue (6 empty blocks)")
        for (n in bootstrapStart until bootstrapStart + 6) {
            postToAll(n)
        }
        // After bootstrap, latest becomes min-1; drain then advances through empty blocks.
        awaitLatest(bootstrapStart + 5)

        val x = bootstrapStart + 6
        logSection("All validators process receipts at X=$x and X+1")
        postToAll(x, listOf(receipt("100", "0")))
        awaitLatest(x)
        postToAll(x + 1, listOf(receipt("101", "0")))
        awaitLatest(x + 1)

        val txBeforeKick = genesis.node.queryBridgeTransaction(chain, (x + 1).toString(), "0")
        assertThat(txBeforeKick.bridgeTransactions).isNotEmpty
        val votersBefore = txBeforeKick.bridgeTransactions.first().validators.toSet()
        Logger.info("Voters before kick: $votersBefore")
        assertThat(votersBefore).hasSize(3)

        logSection("Kick join1 via confirmation PoC (INACTIVE → RemoveMember)")
        val confirmationEvent = waitForConfirmationPoCTrigger(genesis)
        assertThat(confirmationEvent).isNotNull
        genesis.setPocWeight(10)
        join1.setPocWeight(3) // ratio 0.3 < alpha 0.5
        join2.setPocWeight(10)
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_GENERATION)
        waitForConfirmationPoCPhase(genesis, ConfirmationPoCPhase.CONFIRMATION_POC_VALIDATION)
        waitForConfirmationPoCCompletion(genesis)
        genesis.setPocWeight(10)
        join1.setPocWeight(10)
        join2.setPocWeight(10)

        val epochIndex = genesis.getEpochData().latestEpoch.index
        val eg = genesis.node.queryEpochGroupData(epochIndex).epochGroupData
        assertThat(eg.epochGroupId).isGreaterThan(0)
        val members = genesis.node.queryGroupMembers(eg.epochGroupId).members
            .mapNotNull { it.member?.address }
            .toSet()
        val join1Addr = join1.node.getColdAddress()
        Logger.info("Epoch $epochIndex group members after kick: $members")
        assertThat(members)
            .`as`("join1 must be removed from stamped SDK group_members (ante path precondition)")
            .doesNotContain(join1Addr)

        // ActiveParticipants / validation_weights often still list join1 — that is the bug shape.
        val stillListedInWeights = eg.validationWeights.any { it.memberAddress == join1Addr }
        Logger.info("join1 still in EpochGroupData.validationWeights=$stillListedInWeights")

        val x2 = x + 2
        logSection("Post receipt X+2=$x2 after kick — expect 2 on-chain votes, 3 API cursors")
        postToAll(x2, listOf(receipt("102", "0")))
        awaitLatest(x2)

        val txAfter = genesis.node.queryBridgeTransaction(chain, x2.toString(), "0")
        assertThat(txAfter.bridgeTransactions).isNotEmpty
        val votersAfter = txAfter.bridgeTransactions.first().validators.toSet()
        Logger.info("Voters after kick on X+2: $votersAfter")
        assertThat(votersAfter)
            .`as`("only in-group validators should have signed")
            .hasSize(2)
            .doesNotContain(join1Addr)
        assertThat(votersAfter).contains(genesis.node.getColdAddress(), join2.node.getColdAddress())

        logSection("PASS: kicked node advanced latest without on-chain vote (ante → #1478 skip)")
    }
}
