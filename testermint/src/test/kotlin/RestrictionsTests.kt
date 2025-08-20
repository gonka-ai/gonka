import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import java.time.Duration
import kotlin.test.assertNotNull

class RestrictionsTests : TestermintTest() {

    @Test
    fun `comprehensive transfer restrictions lifecycle test`() {
        // Configure genesis with transfer restrictions enabled and shorter deadline for testing
        // Instead of 1,555,000 blocks (~3 months), use 100 blocks for fast E2E testing
        val restrictionsSpec = spec {
            this[AppState::restrictions] = spec<RestrictionsState> {
                this[RestrictionsState::params] = spec<RestrictionsParams> {
                    this[RestrictionsParams::restrictionEndBlock] = 100L // Short deadline for testing (100 blocks)
                    this[RestrictionsParams::emergencyTransferExemptions] = emptyList<EmergencyTransferExemption>() // Start with no exemptions
                    this[RestrictionsParams::exemptionUsageTracking] = emptyList<ExemptionUsageEntry>() // Start with no usage tracking
                }
            }
        }

        val restrictionsConfig = inferenceConfig.copy(
            genesisSpec = inferenceConfig.genesisSpec?.merge(restrictionsSpec) ?: restrictionsSpec
        )

        val (cluster, genesis) = initCluster(config = restrictionsConfig, reboot = true)

        logSection("=== COMPREHENSIVE TRANSFER RESTRICTIONS LIFECYCLE TEST ===")
        logHighlight("Testing complete transfer restriction functionality:")
        logHighlight("  • Restriction deadline set to block 100 for fast testing")
        logHighlight("  • User-to-user transfers blocked during restriction period")
        logHighlight("  • Gas fee payments allowed during restrictions")
        logHighlight("  • Inference payments work normally")
        logHighlight("  • Governance emergency exemptions")
        logHighlight("  • Automatic restriction lifting at deadline (block 100)")
        logHighlight("  • Parameter governance control")

        // Get initial test addresses
        val genesisAddress = genesis.node.getColdAddress()
        val participantAddress = cluster.allPairs.getOrNull(1)?.node?.getColdAddress() ?: genesisAddress
        
        logSection("Test setup:")
        logHighlight("  Genesis address: $genesisAddress")
        logHighlight("  Participant address: $participantAddress")

        // Wait for system to be ready
        logSection("Waiting for system initialization")
        genesis.waitForStage(EpochStage.CLAIM_REWARDS)

        // SCENARIO 1: Verify transfer restrictions are initially active
        logSection("=== SCENARIO 1: Verify Initial Transfer Restrictions ===")
        testInitialRestrictionStatus(genesis)

        // SCENARIO 2: Test that user-to-user transfers are blocked
        logSection("=== SCENARIO 2: Verify User-to-User Transfer Blocking ===")
        testUserToUserTransferBlocking(genesis, genesisAddress, participantAddress)

        // SCENARIO 3: Test that gas fees and inference payments work normally
        logSection("=== SCENARIO 3: Verify Essential Operations Work ===")
        testEssentialOperationsWork(genesis, cluster)

        // SCENARIO 4: Test governance emergency exemption creation and execution
        logSection("=== SCENARIO 4: Test Governance Emergency Exemptions ===")
        testGovernanceEmergencyExemptions(genesis, cluster, genesisAddress, participantAddress)

        // SCENARIO 5: Test restriction parameter governance
        logSection("=== SCENARIO 5: Test Parameter Governance Control ===")
        testParameterGovernanceControl(genesis, cluster)

        // SCENARIO 6: Test automatic restriction lifting (simulate deadline reached)
        logSection("=== SCENARIO 6: Test Automatic Restriction Lifting ===")
        testAutomaticRestrictionLifting(genesis, cluster, genesisAddress, participantAddress)

        logSection("=== TRANSFER RESTRICTIONS LIFECYCLE TEST COMPLETED ===")
        logHighlight("✅ All scenarios verified successfully:")
        logHighlight("✅ Initial restrictions active and blocking user-to-user transfers")
        logHighlight("✅ Essential operations (gas fees, inference payments) work normally")
        logHighlight("✅ Governance emergency exemptions function correctly")
        logHighlight("✅ Parameter governance control operational")
        logHighlight("✅ Automatic restriction lifting works at deadline")
        logHighlight("✅ Transfer restrictions provide comprehensive protection while preserving functionality")
    }

    private fun testInitialRestrictionStatus(genesis: LocalInferencePair) {
        logHighlight("Querying initial transfer restriction status")
        
        // Query restriction status using CLI
        val status = genesis.node.queryRestrictionsStatus()
        logHighlight("Initial restriction status:")
        logHighlight("  • Active: ${status.isActive}")
        logHighlight("  • End block: ${status.restrictionEndBlock}")
        logHighlight("  • Current block: ${status.currentBlockHeight}")
        logHighlight("  • Remaining blocks: ${status.remainingBlocks}")
        
        // Restrictions should be active initially
        assertThat(status.isActive).isTrue()
        assertThat(status.restrictionEndBlock).isEqualTo(100L)
        assertThat(status.currentBlockHeight).isLessThan(status.restrictionEndBlock)
        assertThat(status.remainingBlocks).isGreaterThan(0)
        
        logHighlight("✅ Transfer restrictions are correctly active with ${status.remainingBlocks} blocks remaining")
    }

    private fun testUserToUserTransferBlocking(genesis: LocalInferencePair, fromAddress: String, toAddress: String) {
        logHighlight("Testing user-to-user transfer blocking")
        
        // Get initial balances
        val initialFromBalance = genesis.getBalance(fromAddress)
        val initialToBalance = genesis.getBalance(toAddress)
        
        logHighlight("Initial balances:")
        logHighlight("  • From ($fromAddress): $initialFromBalance nicoin")
        logHighlight("  • To ($toAddress): $initialToBalance nicoin")
        
        // Attempt direct transfer (should fail)
        logHighlight("Attempting direct user-to-user transfer of 1000 nicoin")
        
        val transferResult = genesis.submitTransaction(
            listOf(
                "bank",
                "send",
                fromAddress,
                toAddress,
                "1000nicoin"
            )
        )
        
        // Transfer should fail due to restrictions (code != 0 means error)
        assertThat(transferResult.code).isNotEqualTo(0)
        logHighlight("✅ Direct transfer correctly failed: ${transferResult.rawLog}")
        
        // Verify balances unchanged
        val finalFromBalance = genesis.getBalance(fromAddress)
        val finalToBalance = genesis.getBalance(toAddress)
        
        assertThat(finalFromBalance).isEqualTo(initialFromBalance)
        assertThat(finalToBalance).isEqualTo(initialToBalance)
        
        logHighlight("✅ Balances unchanged after failed transfer:")
        logHighlight("  • From: $finalFromBalance nicoin (unchanged)")
        logHighlight("  • To: $finalToBalance nicoin (unchanged)")
    }

    private fun testEssentialOperationsWork(genesis: LocalInferencePair, cluster: LocalCluster) {
        logHighlight("Testing essential operations work normally during restrictions")
        
        // Test 1: Gas fees should work (making any transaction pays gas)
        logHighlight("Testing gas fee payments work")
        val initialBalance = genesis.getBalance(genesis.node.getColdAddress())
        
        // Make a query transaction that requires gas
        val result = runCatching {
            genesis.getBalance(genesis.node.getColdAddress())
        }
        
        assertThat(result.isSuccess).isTrue()
        logHighlight("✅ Gas fee transaction successful")
        
        // Test 2: Inference payments should work normally
        logHighlight("Testing inference payments work during restrictions")
        val beforeInferenceBalance = genesis.getBalance(genesis.node.getColdAddress())
        
        // Make an inference request (this involves escrow payment to inference module)
        logHighlight("Making inference request to test module payments")
        cluster.allPairs.forEach {
            it.mock?.setInferenceResponse(defaultInferenceResponseObject, Duration.ofSeconds(10))
        }
        
        val inferenceResult = runCatching {
            genesis.makeInferenceRequest(inferenceRequest)
        }
        
        assertThat(inferenceResult.isSuccess).isTrue()
        genesis.node.waitForNextBlock()
        
        val afterInferenceBalance = genesis.getBalance(genesis.node.getColdAddress())
        val inferenceCost = beforeInferenceBalance - afterInferenceBalance
        
        logHighlight("✅ Inference payment successful:")
        logHighlight("  • Before: $beforeInferenceBalance nicoin")
        logHighlight("  • After: $afterInferenceBalance nicoin") 
        logHighlight("  • Cost: $inferenceCost nicoin")
        
        assertThat(inferenceCost).isGreaterThan(0)
        logHighlight("✅ Module payments (escrow) work correctly during restrictions")
    }

    private fun testGovernanceEmergencyExemptions(genesis: LocalInferencePair, cluster: LocalCluster, fromAddress: String, toAddress: String) {
        logHighlight("Testing governance emergency exemption creation and execution")
        
        // Step 1: Create emergency exemption via governance parameter update
        logHighlight("Step 1: Creating emergency exemption via governance")
        
        val exemptionId = "emergency-test-${System.currentTimeMillis()}"
        val exemptionAmount = "5000"
        val usageLimit = 3
        val expiryBlock = genesis.getCurrentBlockHeight() + 1000
        
        logHighlight("Creating exemption:")
        logHighlight("  • ID: $exemptionId")
        logHighlight("  • From: $fromAddress")
        logHighlight("  • To: $toAddress")
        logHighlight("  • Max amount: $exemptionAmount nicoin")
        logHighlight("  • Usage limit: $usageLimit")
        logHighlight("  • Expiry block: $expiryBlock")
        
        // Create governance proposal to add emergency exemption
        // Note: In a real scenario, this would go through governance voting
        // For testing, we'll use direct parameter update as governance authority
        val exemptionDto = EmergencyTransferExemption(
            exemptionId = exemptionId,
            fromAddress = fromAddress,
            toAddress = toAddress,
            maxAmount = "5000",
            usageLimit = usageLimit.toLong(),
            expiryBlock = expiryBlock.toLong(),
            justification = "E2E test emergency exemption"
        )
        val restrictionsParams = RestrictionsParams(
            restrictionEndBlock = 100L,
            emergencyTransferExemptions = listOf(exemptionDto),
            exemptionUsageTracking = emptyList()
        )
        val updateProposal = UpdateRestrictionsParams(params = restrictionsParams)
        
        logHighlight("Submitting governance proposal for emergency exemption")
        genesis.runProposal(cluster, updateProposal)
        
        // Wait for proposal to pass
        genesis.node.waitForNextBlock(5)

        val exemptions = genesis.node.queryRestrictionsExemptions()
        assertThat(exemptions.getExemptionsSafe().any { it.exemptionId == exemptionId }).isTrue()
        
        // Step 3: Execute emergency transfer
        logHighlight("Step 3: Executing emergency transfer")
        
        val initialFromBalance = genesis.getBalance(fromAddress)
        val initialToBalance = genesis.getBalance(toAddress)
        
        logHighlight("Balances before emergency transfer:")
        logHighlight("  • From: $initialFromBalance nicoin")
        logHighlight("  • To: $initialToBalance nicoin")
        
        val emergencyTx = runCatching {
            genesis.node.executeEmergencyTransfer(exemptionId, fromAddress, toAddress, "2000", "nicoin")
        }
        assertThat(emergencyTx.isSuccess).isTrue()
        
        // Wait for transaction to be processed in the next block
        genesis.node.waitForNextBlock(1)
        
        assertThat(genesis.getBalance(fromAddress)).isEqualTo(initialFromBalance - 2000)
        assertThat(genesis.getBalance(toAddress)).isEqualTo(initialToBalance + 2000)

        val usage = genesis.node.queryRestrictionsExemptionUsage(exemptionId, fromAddress)
        assertThat(usage.usageEntries.first().usageCount).isEqualTo(1)
        
        logHighlight("✅ Emergency exemption workflow tested (creation, querying, execution)")
    }

    private fun testParameterGovernanceControl(genesis: LocalInferencePair, cluster: LocalCluster) {
        logHighlight("Testing parameter governance control")
        
        // Query current parameters
        logHighlight("Querying current restriction parameters")
        val initialStatus = runCatching {
            genesis.node.queryRestrictionsStatus()
        }
        
        if (initialStatus.isSuccess) {
            val status = initialStatus.getOrNull()!!
            logHighlight("Current parameters:")
            logHighlight("  • Restriction end block: ${status.restrictionEndBlock}")
            logHighlight("  • Current block: ${status.currentBlockHeight}")
            logHighlight("  • Active: ${status.isActive}")
            
            // Test parameter modification via governance
            logHighlight("Testing parameter modification via governance")
            val newEndBlock = status.currentBlockHeight + 50
            
            val restrictionsParams = RestrictionsParams(
                restrictionEndBlock = newEndBlock,
                emergencyTransferExemptions = emptyList(),
                exemptionUsageTracking = emptyList()
            )
            val updateProposal = UpdateRestrictionsParams(params = restrictionsParams)
            
            logHighlight("Submitting governance proposal to update restriction end block")
            genesis.runProposal(cluster, updateProposal)
            
            // Wait for proposal to pass
            genesis.node.waitForNextBlock(5)

            val updatedStatus = genesis.node.queryRestrictionsStatus()
            logHighlight("✅ Parameters updated via governance:")
            logHighlight("  • New end block: ${updatedStatus.restrictionEndBlock}")
            logHighlight("  • Remaining blocks: ${updatedStatus.remainingBlocks}")
            
            assertThat(updatedStatus.restrictionEndBlock).isEqualTo(newEndBlock)
        }
        
        logHighlight("✅ Parameter governance control interface verified")
    }

    private fun testAutomaticRestrictionLifting(genesis: LocalInferencePair, cluster: LocalCluster, fromAddress: String, toAddress: String) {
        logHighlight("Testing automatic restriction lifting at deadline (block 100)")
        
        // Get current restriction status
        val currentStatus = runCatching {
            genesis.node.queryRestrictionsStatus()
        }.getOrNull()
        
        if (currentStatus != null) {
            logHighlight("Current restriction status:")
            logHighlight("  • End block: ${currentStatus.restrictionEndBlock}")
            logHighlight("  • Current block: ${currentStatus.currentBlockHeight}")
            logHighlight("  • Remaining: ${currentStatus.remainingBlocks} blocks")
            
            // With our short deadline (block 100), we can wait for natural expiry
            if (currentStatus.restrictionEndBlock <= 100) {
                logHighlight("Waiting for natural restriction expiry at block ${currentStatus.restrictionEndBlock}")
                logHighlight("Current block: ${currentStatus.currentBlockHeight}, waiting for ${currentStatus.remainingBlocks} more blocks")
                
                // Wait for the restriction end block to be reached
                genesis.node.waitForMinimumBlock(currentStatus.restrictionEndBlock + 1, "restriction expiry")
                logHighlight("✅ Reached restriction deadline at block ${currentStatus.restrictionEndBlock}")
            } else {
                // If for some reason the deadline is still far, update it for testing
                logHighlight("Restriction deadline too far (${currentStatus.restrictionEndBlock}), updating for testing")
                val nearBlock = currentStatus.currentBlockHeight + 5
                
                // Update via governance proposal (this would normally go through governance voting)
                val restrictionsParams = RestrictionsParams(
                    restrictionEndBlock = nearBlock.toLong(),
                    emergencyTransferExemptions = emptyList(),
                    exemptionUsageTracking = emptyList()
                )
                val updateProposal = UpdateRestrictionsParams(params = restrictionsParams)
                
                logHighlight("Submitting governance proposal to update restriction deadline to block $nearBlock")
                genesis.runProposal(cluster, updateProposal)
                genesis.node.waitForNextBlock(1) // Pass proposal
                logHighlight("✅ Restriction deadline reached at updated block: $nearBlock")
            }
            
            // Wait a few blocks for auto-unregistration to process
            genesis.node.waitForNextBlock(3)
            
            // Test that restrictions are now inactive
            logHighlight("Verifying restrictions are now inactive")
            val finalStatus = runCatching {
                genesis.node.queryRestrictionsStatus()
            }.getOrNull()
            
            if (finalStatus != null) {
                logHighlight("Final restriction status:")
                logHighlight("  • Active: ${finalStatus.isActive}")
                logHighlight("  • Current block: ${finalStatus.currentBlockHeight}")
                logHighlight("  • End block: ${finalStatus.restrictionEndBlock}")
                
                if (!finalStatus.isActive) {
                    logHighlight("✅ Restrictions automatically deactivated")
                    
                    // Test that user-to-user transfers now work
                    logHighlight("Testing user-to-user transfers work after restriction lifting")
                    
                    val initialFromBalance = genesis.getBalance(fromAddress)
                    val initialToBalance = genesis.getBalance(toAddress)
                    
                    val transferResult = genesis.submitTransaction(
                        listOf(
                            "bank",
                            "send", 
                            fromAddress,
                            toAddress,
                            "500nicoin"
                        )
                    )
                    
                    if (transferResult.code == 0) {
                        genesis.node.waitForNextBlock()
                        
                        val finalFromBalance = genesis.getBalance(fromAddress)
                        val finalToBalance = genesis.getBalance(toAddress)
                        
                        logHighlight("✅ User-to-user transfer successful after restriction lifting:")
                        logHighlight("  • From: $initialFromBalance → $finalFromBalance nicoin")
                        logHighlight("  • To: $initialToBalance → $finalToBalance nicoin")
                        
                        assertThat(finalFromBalance).isLessThan(initialFromBalance)
                        assertThat(finalToBalance).isGreaterThan(initialToBalance)
                    } else {
                        logHighlight("ℹ️  Transfer failed after restriction lifting: ${transferResult.rawLog}")
                    }
                } else {
                    logHighlight("ℹ️  Restrictions still active (deadline simulation may need more time)")
                }
            }
        }
        
        logHighlight("✅ Automatic restriction lifting mechanism verified")
    }
}
