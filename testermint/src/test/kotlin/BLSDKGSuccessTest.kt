import com.productscience.EpochStage
import com.productscience.initCluster
import com.productscience.logSection
import com.productscience.data.RequestThresholdSignatureDto
import com.productscience.data.toHexString
import com.productscience.logHighlight
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.junit.jupiter.api.Tag
import org.tinylog.kotlin.Logger
import java.util.Base64
import java.util.concurrent.TimeUnit

/**
 * BLS DKG Success Flow Testing with Testermint
 * 
 * This test suite validates the complete successful BLS DKG workflow using Testermint's
 * multi-node cluster architecture. Tests include real cryptographic operations and
 * cross-node consistency validation.
 * 
 * These tests are resource-intensive and require Docker. Run explicitly with:
 * - make run-bls-tests (or custom target)
 * - ./gradlew test --tests "BLSDKGSuccessTest" (bypasses excludeTags)
 * - IntelliJ: Right-click and run individual tests
 */
@Timeout(value = 15, unit = TimeUnit.MINUTES)
class BLSDKGSuccessTest : TestermintTest() {

    @Test
    @Tag("bls-integration")
    fun `complete BLS DKG success flow with 3 participants`() {
        logSection("Starting BLS DKG Success Flow Test")
        
        // Initialize cluster with 3 participants (genesis + 2 join nodes)
        val (cluster, genesis) = initCluster(joinCount = 2)
        
        // Verify we have the expected number of participants
        val allPairs = listOf(genesis) + cluster.joinPairs
        assertThat(allPairs).hasSize(3)
        logSection("Initialized cluster with ${allPairs.size} participants")
        
        // Trigger DKG initiation by waiting for SET_NEW_VALIDATORS stage
        logSection("Triggering DKG initiation")
        triggerDKGInitiation(genesis)
        
        // Capture the epoch ID immediately after DKG initiation to avoid race conditions
        val epochId = getCurrentEpochId(genesis)
        logSection("Captured epoch ID for test: $epochId")
        
        // Monitor DKG phases and validate progression (using captured epoch ID)
        logSection("Monitoring DKG phase progression")
        monitorDKGPhaseProgression(genesis, epochId)
        
        // Validate cross-node consistency (using captured epoch ID)
        logSection("Validating cross-node consistency")
        validateCrossNodeConsistency(allPairs, epochId)
        
        // Validate cryptographic correctness (using captured epoch ID)
        logSection("Validating cryptographic correctness")
        validateCryptographicCorrectness(allPairs, epochId)
        
        // Validate controller readiness for threshold signing (using captured epoch ID)
        logSection("Validating threshold signing readiness")
        validateThresholdSigningReadiness(allPairs, epochId)

        // Validate group key signature from previous epoch
        if (epochId > 1) {
            logSection("BLS_TEST: Validating group key signature from previous epoch")
            waitForDKGPhase(genesis, DKGPhase.SIGNED, epochId)
            logSection("BLS_TEST: Group key signature validated for epoch $epochId")
            validateGroupKeySignature(allPairs, epochId)
        } else {
            logSection("BLS_TEST: No previous epoch, skipping group key signature validation")
        }

        // Test threshold signing
        logSection("BLS_TEST: Testing threshold signing")
        testThresholdSigning(allPairs, epochId)        
        
        logSection("BLS DKG Success Flow Test completed successfully!")
    }
    
    @Test 
    @Tag("bls-integration")
    fun `BLS state consistency across cluster nodes`() {
        logSection("Testing BLS state consistency across cluster nodes")
        
        val (cluster, genesis) = initCluster(joinCount = 4)
        val allPairs = listOf(genesis) + cluster.joinPairs

        genesis.waitForStage(EpochStage.CLAIM_REWARDS, offset = 10)
        genesis.waitForStage(EpochStage.CLAIM_REWARDS, 2)

        
        // Trigger complete DKG flow
        logSection("Triggering DKG Init")
        triggerDKGInitiation(genesis)
        
        // Capture epoch ID once to avoid race conditions
        logSection("Waiting for DKG Phase")
        val epochId = getCurrentEpochId(genesis)
        waitForDKGPhase(genesis, DKGPhase.COMPLETED, epochId)

        logSection("Verifying BLS State from all nodes")
        // Query BLS state from all nodes
        val blsDataFromNodes = allPairs.map { pair ->
            pair.name to queryEpochBLSData(pair, epochId)
        }
        
        // Validate all nodes have identical BLS state
        val referenceData = blsDataFromNodes.first().second
        assertThat(referenceData).isNotNull()
        
        blsDataFromNodes.forEach { (nodeName, blsData) ->
            assertThat(blsData).isNotNull()
            assertThat(blsData?.dkgPhase).isEqualTo(DKGPhase.COMPLETED)
            assertThat(blsData?.groupPublicKey).isEqualTo(referenceData?.groupPublicKey)
            assertThat(blsData?.iTotalSlots).isEqualTo(referenceData?.iTotalSlots)
            assertThat(blsData?.tSlotsDegree).isEqualTo(referenceData?.tSlotsDegree)
            Logger.info("Node $nodeName has consistent BLS state")
        }
    }
    
    @Test
    @Tag("bls-integration")
    fun `cryptographic operations validation`() {
        logSection("Testing cryptographic operations validation")
        
        val (cluster, genesis) = initCluster(joinCount = 4)
        val allPairs = listOf(genesis) + cluster.joinPairs
        
        // Complete DKG flow
        triggerDKGInitiation(genesis)
        
        // Capture epoch ID once to avoid race conditions
        val epochId = getCurrentEpochId(genesis)
        waitForDKGPhase(genesis, DKGPhase.COMPLETED, epochId)
        
        val blsData = queryEpochBLSData(genesis, epochId)
        assertThat(blsData).isNotNull()
        assertThat(blsData?.groupPublicKey).isNotNull()
        
        // Validate group public key format (96-byte compressed G2)
        val groupPubKeyBytes = blsData?.groupPublicKey
        assertThat(groupPubKeyBytes).hasSize(96) // Compressed G2 format
        
        // Validate dealer commitments consistency
        validateDealerCommitments(blsData!!)
        
        // Validate participant slot assignments
        validateParticipantSlotAssignments(blsData)
        
        logSection("Cryptographic operations validated successfully")
    }
    
    // ========================================
    // Helper Functions
    // ========================================
    
    private fun triggerDKGInitiation(genesis: com.productscience.LocalInferencePair) {
        logSection("Waiting for SET_NEW_VALIDATORS stage to initiate DKG")
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        
        logSection("Waiting for DKG to start (DEALING phase)")
        waitForDKGPhase(genesis, DKGPhase.DEALING, getCurrentEpochId(genesis))
        
        logSection("DKG initiated successfully")
    }
    
    private fun monitorDKGPhaseProgression(genesis: com.productscience.LocalInferencePair, epochId: Long) {
        logSection("Monitoring DKG phase progression until completion")
        waitForDKGPhase(genesis, DKGPhase.COMPLETED, epochId)
        logSection("DKG phase progression completed successfully!")
    }
    
    private fun validateCrossNodeConsistency(allPairs: List<com.productscience.LocalInferencePair>, epochId: Long) {
        // Query BLS data from all nodes
        val blsDataList = allPairs.map { pair ->
            pair.name to queryEpochBLSData(pair, epochId)
        }
        
        val referenceData = blsDataList.first().second
        assertThat(referenceData).isNotNull()
        
        // Validate consistency across all nodes
        blsDataList.forEach { (nodeName, blsData) ->
            assertThat(blsData).isNotNull()
            assertThat(blsData?.epochId).isEqualTo(referenceData?.epochId)
            assertThat(blsData?.dkgPhase).isEqualTo(DKGPhase.COMPLETED)
            assertThat(blsData?.groupPublicKey).isEqualTo(referenceData?.groupPublicKey)
            Logger.info("Cross-node consistency validated for node: $nodeName")
        }
    }
    
    private fun validateCryptographicCorrectness(allPairs: List<com.productscience.LocalInferencePair>, epochId: Long) {
        val blsData = queryEpochBLSData(allPairs.first(), epochId)
        
        assertThat(blsData).isNotNull()
        assertThat(blsData?.groupPublicKey).isNotNull()
        
        // Validate group public key format (compressed G2)
        assertThat(blsData?.groupPublicKey).hasSize(96)
        
        // Validate dealer parts were submitted
        val dealerParts = blsData?.dealerParts
        assertThat(dealerParts).isNotNull()
        assertThat(dealerParts).hasSizeGreaterThan(0)
        
        // Validate verification submissions
        val verificationSubmissions = blsData?.verificationSubmissions
        assertThat(verificationSubmissions).isNotNull()
        assertThat(verificationSubmissions).hasSizeGreaterThan(0)
        
        Logger.info("Cryptographic correctness validated")
    }
    
    private fun validateThresholdSigningReadiness(allPairs: List<com.productscience.LocalInferencePair>, epochId: Long) {
        // Validate that controllers have the necessary data for threshold signing
        Logger.info("Validating threshold signing readiness for epoch $epochId with ${allPairs.size} controllers")
        
        allPairs.forEach { pair ->
            // Check that each controller can query the group public key
            val blsData = queryEpochBLSData(pair, epochId)
            Logger.info("Node ${pair.name}: blsData = ${if (blsData != null) "found" else "null"}")
            
            if (blsData != null) {
                Logger.info("Node ${pair.name}: dkgPhase = ${blsData.dkgPhase}, groupPublicKey = ${if (blsData.groupPublicKey != null) "${blsData.groupPublicKey!!.size} bytes" else "null"}")
            }
            
            assertThat(blsData?.groupPublicKey).isNotNull()
            assertThat(blsData?.dkgPhase).isEqualTo(DKGPhase.COMPLETED)
            
            Logger.info("Node ${pair.name} ready for threshold signing")
        }
        
        Logger.info("All controllers ready for threshold signing")
    }

    private fun validateGroupKeySignature(allPairs: List<com.productscience.LocalInferencePair>, epochId: Long) {
        val blsData = queryEpochBLSData(allPairs.first(), epochId)
        assertThat(blsData?.validationSignature).isNotNull()
        assertThat(blsData?.validationSignature).hasSize(48) // Compressed G1 format
        Logger.info("Group key signature validated for epoch $epochId")
    }

    private fun testThresholdSigning(allPairs: List<com.productscience.LocalInferencePair>, epochId: Long) {
        val genesis = allPairs.first()
        val requestId = "test_request_${epochId}"
        val requestIdBytes = requestId.toByteArray()
        val requestIdHex = requestIdBytes.toHexString()  // Convert to hex for API query
        val data = listOf("test_data")

        // Request a threshold signature via the API
        val requestDto = RequestThresholdSignatureDto(
            currentEpochId = epochId.toULong(),  // Convert Long to ULong
            chainId = "inference".toByteArray(),
            requestId = requestIdBytes,
            data = data.map { it.toByteArray() }
        )
        genesis.api.requestThresholdSignature(requestDto)
        Logger.info("Threshold signature requested via API for request ID: $requestId (hex: $requestIdHex)")

        // Wait for the signature to be created (with 10-block deadline, allow time for controller response)
        genesis.node.waitForNextBlock(12)

        // Query the signing status via API instead of CLI (using hex-encoded request ID)
        val signingStatus = genesis.api.queryBLSSigningStatus(requestIdHex)
        
        if (signingStatus.signingRequest == null) {
            Logger.error("Signing request not found! This suggests the request was never created, expired, or has a different ID")
        }
        
        val statusCode = signingStatus.signingRequest.status.toString().toInt()
        val statusEnum = ThresholdSigningStatus.fromValue(statusCode)
        Logger.info("Found signing request with status: $statusEnum ($statusCode)")
        assertThat(statusEnum).isEqualTo(ThresholdSigningStatus.COMPLETED)
        assertThat(signingStatus.signingRequest.finalSignature).isNotNull()
        val sigBytes = Base64.getDecoder().decode(signingStatus.signingRequest.finalSignature)
        assertThat(sigBytes).hasSize(48) // Compressed G1 format
        Logger.info("Threshold signature created successfully for request $requestId")
    }
    
    // ========================================
    // DKG Phase Management
    // ========================================
    
    private fun waitForDKGPhase(pair: com.productscience.LocalInferencePair, targetPhase: DKGPhase, epochId: Long, maxAttempts: Int = 20) {
        var currentPhase: DKGPhase? = null
        var attempts = 0
        
        Logger.info("Waiting for DKG phase $targetPhase (or higher) for epoch $epochId")
        
        while (attempts < maxAttempts) {
            val epochBLSData = queryEpochBLSData(pair, epochId)
            currentPhase = epochBLSData?.dkgPhase
            
            // Check if DKG failed - this is always an error regardless of target phase
            if (currentPhase == DKGPhase.FAILED) {
                Logger.error("❌ DKG failed for epoch $epochId while waiting for $targetPhase")
                error("DKG failed for epoch $epochId while waiting for $targetPhase")
            }
            
            // Check if we've reached the target phase or higher
            if (currentPhase != null && currentPhase.value >= targetPhase.value) {
                Logger.info("✅ DKG Phase $currentPhase reached for epoch $epochId (target was $targetPhase)")
                return
            }
            
            // Wait for next block and try again
            pair.node.waitForNextBlock()
            attempts++
            
            if (attempts % 10 == 0) {
                Logger.info("Still waiting for DKG phase $targetPhase (current: $currentPhase, attempts: $attempts)")
            }
        }
        
        error("Timeout waiting for DKG phase $targetPhase (current: $currentPhase, attempts: $attempts)")
    }
    
    private fun validateDKGPhase(pair: com.productscience.LocalInferencePair, epochId: Long, expectedPhase: DKGPhase) {
        val blsData = queryEpochBLSData(pair, epochId)
        assertThat(blsData).isNotNull()
        assertThat(blsData?.dkgPhase).isEqualTo(expectedPhase)
        Logger.info("DKG phase validation passed: $expectedPhase")
    }
    
    // ========================================
    // Chain Query Functions
    // ========================================
    
    private fun getCurrentEpochId(pair: com.productscience.LocalInferencePair): Long {
        // Calculate current epoch based on block height and epoch length
        val currentHeight = pair.getCurrentBlockHeight()
        val epochLength = pair.getEpochLength()
        // TODO: It's a temparary fix, remove it. Generate odd-numbered epochs (1, 3, 5, 7...) instead of sequential (1, 2, 3, 4...)
        val calculatedEpochId = (currentHeight / epochLength)
        Logger.info("Calculated epoch ID: $calculatedEpochId (block: $currentHeight, epoch length: $epochLength)")
        
        // Try to find which epoch actually has DKG data by checking recent epochs
        // DKG might be running for the current epoch or the next epoch
        val epochsToTry = listOf(calculatedEpochId, calculatedEpochId + 1, calculatedEpochId - 2).filter { it >= 1 }
        
        for (epochId in epochsToTry) {
            try {
                val blsData = pair.node.queryEpochBLSData(epochId)
                if (blsData != null) {
                    Logger.info("Found DKG data for epoch $epochId")
                    return epochId
                }
            } catch (e: Exception) {
                Logger.debug("No DKG data found for epoch $epochId: ${e.message}")
            }
        }
        
        // If no existing DKG data found, return the calculated epoch (DKG might start soon)
        Logger.info("No existing DKG data found, using calculated epoch ID: $calculatedEpochId")
        return calculatedEpochId
    }
    
    private fun queryEpochBLSData(pair: com.productscience.LocalInferencePair, epochId: Long): EpochBLSData? {
        for (attempt in 1..3) {
            try {
                // Query BLS module for epoch data using the extension function
                return pair.node.queryEpochBLSData(epochId)
            } catch (e: Exception) {
                // Handle specific error cases more gracefully
                val errorMessage = e.message ?: "Unknown error"
                when {
                    errorMessage.contains("NotFound") || errorMessage.contains("key not found") -> {
                        Logger.debug("No DKG data found for epoch $epochId")
                        return null
                    }
                    errorMessage.contains("Expected BEGIN_OBJECT but was STRING") -> {
                        Logger.debug("CLI returned error string instead of JSON for epoch $epochId")
                        return null
                    }
                    else -> {
                        if (attempt < 3) {
                            val sleepMs = 100L * attempt * attempt
                            Logger.warn("Failed to query epoch BLS data for epoch $epochId (attempt $attempt/3): $errorMessage. Retrying in ${sleepMs}ms...")
                            Thread.sleep(sleepMs)
                        } else {
                            Logger.warn("Failed to query epoch BLS data for epoch $epochId after 3 attempts: $errorMessage")
                            return null
                        }
                    }
                }
            }
        }
        return null
    }
    
    // ========================================
    // Cryptographic Validation Functions
    // ========================================
    
    private fun validateDealerCommitments(blsData: EpochBLSData) {
        logHighlight("Validating dealer commitments for epoch ${blsData.epochId}")
        
        // Validate that we have dealer parts
        assertThat(blsData.dealerParts).isNotEmpty()
        logHighlight("Found ${blsData.dealerParts.size} dealer parts")
        
        // Log detailed information about each dealer part
        blsData.dealerParts.forEachIndexed { index, dealerPart ->
            logHighlight("Dealer part $index: address='${dealerPart.dealerAddress}', commitments=${dealerPart.commitments.size}, participantShares=${dealerPart.participantShares.size}")
        }
        
        // Filter only non-empty dealer parts for validation
        val activeDealerParts = blsData.dealerParts.filter { it.dealerAddress.isNotEmpty() }
        logHighlight("Active dealer parts (non-empty): ${activeDealerParts.size}")
        
        activeDealerParts.forEachIndexed { index, dealerPart ->
            logHighlight("Validating active dealer part $index: ${dealerPart.dealerAddress}")
            
            // Validate commitments exist
            assertThat(dealerPart.commitments).isNotEmpty()
            logHighlight("Dealer ${dealerPart.dealerAddress} has ${dealerPart.commitments.size} commitments")
            
            // Validate commitment format (should be G2 points)
            dealerPart.commitments.forEachIndexed { commitmentIndex, commitment ->
                if (commitmentIndex < 3) { // Only log first few commitments to avoid spam
                    logHighlight("Commitment $commitmentIndex size: ${commitment.size} bytes")
                }
                // G2 points can be:
                // - Compressed: 96 bytes
                // - Uncompressed: 192 bytes  
                // - Other formats depending on implementation
                assertThat(commitment.size).isGreaterThan(0) // Just ensure it's not empty
                assertThat(commitment.size).isLessThan(300) // Reasonable upper bound
                
                // Log first few bytes to understand the format
                if (commitmentIndex == 0) {
                    val firstBytes = commitment.take(10).joinToString(", ")
                    logHighlight("First commitment starts with: [$firstBytes...]")
                }
            }
            
            // Validate participant shares structure
            assertThat(dealerPart.participantShares).hasSize(blsData.participants.size)
            logHighlight("Dealer ${dealerPart.dealerAddress} has shares for ${dealerPart.participantShares.size} participants")
            
            // Validate each participant's encrypted shares
            dealerPart.participantShares.forEachIndexed { participantIndex, shares ->
                val participant = blsData.participants[participantIndex]
                val expectedSlotCount = (participant.slotEndIndex - participant.slotStartIndex + 1)
                logHighlight("Participant $participantIndex (${participant.address}): expected slots=$expectedSlotCount, actual shares=${shares.encryptedShares.size}")

                // For now, we are going to check for EITHER expectedSlotCount OR expectedSlotCount*2, because we
                // are producing encryptedShares for both warm and hot key
                assertThat(shares.encryptedShares.size).isIn(expectedSlotCount, expectedSlotCount * 2)
                
                // Validate encrypted share format (should be non-empty)
                shares.encryptedShares.forEachIndexed { shareIndex, encryptedShare ->
                    assertThat(encryptedShare).isNotEmpty()
                    if (shareIndex == 0) {
                        Logger.info("First encrypted share for participant $participantIndex: ${encryptedShare.size} bytes")
                    }
                }
            }
            
            logHighlight("✅ Dealer ${dealerPart.dealerAddress} validation passed")
        }
        
        logHighlight("✅ Dealer commitments validation passed")
    }
    
    private fun validateParticipantSlotAssignments(blsData: EpochBLSData) {
        Logger.info("Validating participant slot assignments for epoch ${blsData.epochId}")
        
        // Validate that we have participants
        assertThat(blsData.participants).isNotEmpty()
        Logger.info("Found ${blsData.participants.size} participants")
        
        // Track slot coverage
        val assignedSlots = mutableSetOf<Int>()
        var totalWeight = 0.0
        
        blsData.participants.forEach { participant ->
            Logger.info("Participant ${participant.address}: slots ${participant.slotStartIndex}-${participant.slotEndIndex}, weight ${participant.percentageWeight}")
            
            // Validate slot range is valid
            assertThat(participant.slotStartIndex).isGreaterThanOrEqualTo(0)
            assertThat(participant.slotEndIndex).isGreaterThanOrEqualTo(participant.slotStartIndex)
            
            // Validate secp256k1 public key format (should be 33 bytes compressed)
            assertThat(participant.secp256k1PublicKey).hasSize(33)
            
            // Validate weight is positive
            val weight = participant.percentageWeight
            assertThat(weight).isGreaterThan(0.0)
            totalWeight += weight
            
            // Check for slot overlaps
            val participantSlots = (participant.slotStartIndex..participant.slotEndIndex).toSet()
            val overlap = assignedSlots.intersect(participantSlots)
            assertThat(overlap).isEmpty() // No slot should be assigned to multiple participants
            
            assignedSlots.addAll(participantSlots)
        }
        
        // Validate total slot coverage
        val expectedSlots = (0 until blsData.iTotalSlots).toSet()
        assertThat(assignedSlots).isEqualTo(expectedSlots) // All slots should be assigned exactly once
        
        // Validate total weight sums to approximately 100% (allowing for floating point precision)
        assertThat(totalWeight).isBetween(99.99, 100.01)
        
        // Validate threshold parameter
        assertThat(blsData.tSlotsDegree).isGreaterThan(0)
        assertThat(blsData.tSlotsDegree).isLessThanOrEqualTo(blsData.iTotalSlots / 2)
        
        Logger.info("✅ Participant slot assignments validation passed")
        Logger.info("Total slots: ${blsData.iTotalSlots}, Threshold: ${blsData.tSlotsDegree}, Total weight: $totalWeight%")
    }
    
}

// ========================================
// Data Classes and Enums (Based on actual protobuf types)
// ========================================

enum class DKGPhase(val value: Int) {
    UNDEFINED(0),
    DEALING(1),
    VERIFYING(2), 
    COMPLETED(3),
    FAILED(4),
    SIGNED(5)
}

enum class ThresholdSigningStatus(val value: Int) {
    UNSPECIFIED(0),
    COLLECTING_SIGNATURES(1),
    AGGREGATING(2),
    COMPLETED(3),
    EXPIRED(5);

    companion object {
        fun fromValue(value: Int): ThresholdSigningStatus =
            values().firstOrNull { it.value == value } ?: UNSPECIFIED
    }
}

data class EpochBLSData(
    val epochId: Long,
    val iTotalSlots: Int,
    val tSlotsDegree: Int,
    val participants: List<BLSParticipantInfo>,
    val dkgPhase: DKGPhase,
    val dealingPhaseDeadlineBlock: Long,
    val verifyingPhaseDeadlineBlock: Long,
    val groupPublicKey: ByteArray?,
    val dealerParts: List<DealerPartStorage>,
    val verificationSubmissions: List<VerificationVectorSubmission>,
    val validDealers: List<Boolean>,
    val validationSignature: ByteArray?
)

data class BLSParticipantInfo(
    val address: String,
    val percentageWeight: Double,
    val secp256k1PublicKey: ByteArray,
    val slotStartIndex: Int,
    val slotEndIndex: Int
)

data class DealerPartStorage(
    val dealerAddress: String,
    val commitments: List<ByteArray>,
    val participantShares: List<EncryptedSharesForParticipant>
)

data class EncryptedSharesForParticipant(
    val encryptedShares: List<ByteArray>
)

data class VerificationVectorSubmission(
    val dealerValidity: List<Boolean>
)

// ========================================
// Extension Functions for ApplicationCLI
// ========================================

/**
 * Extension function to query BLS epoch data from the chain
 * This implements the actual gRPC query to the BLS module
 */
fun com.productscience.ApplicationCLI.queryEpochBLSData(epochId: Long): EpochBLSData? {
    return try {
        // Query the BLS module for epoch data using the established pattern
        val result: Map<String, Any> = this.execAndParse(
            listOf("query", "bls", "epoch-data", epochId.toString())
        )
        
        // Parse the result into our data structure
        parseEpochBLSDataFromQuery(result)
    } catch (e: Exception) {
        // Handle specific error cases more gracefully
        val errorMessage = e.message ?: "Unknown error"
        when {
            errorMessage.contains("NotFound") || errorMessage.contains("key not found") -> {
                Logger.debug("No DKG data found for epoch $epochId")
                null
            }
            errorMessage.contains("Expected BEGIN_OBJECT but was STRING") -> {
                Logger.debug("CLI returned error string instead of JSON for epoch $epochId")
                null
            }
            else -> {
                Logger.warn("Failed to query epoch BLS data for epoch $epochId: $errorMessage")
                null
            }
        }
    }
}

/**
 * Helper function to parse query results into EpochBLSData
 * Parses the JSON response from the CLI query
 */
private fun parseEpochBLSDataFromQuery(result: Map<String, Any>): EpochBLSData? {
    return try {
        // The CLI response structure is: { "epoch_data": { ... } }
        @Suppress("UNCHECKED_CAST")
        val epochData = (result["epoch_data"] as? Map<String, Any>) ?: return null
        
        // Parse basic fields
        val epochId = when (val value = epochData["epoch_id"]) {
            is String -> value.toLongOrNull() ?: 0L
            is Number -> value.toLong()
            else -> 0L
        }
        val iTotalSlots = when (val value = epochData["i_total_slots"]) {
            is String -> value.toIntOrNull() ?: 0
            is Number -> value.toInt()
            else -> 0
        }
        val tSlotsDegree = when (val value = epochData["t_slots_degree"]) {
            is String -> value.toIntOrNull() ?: 0
            is Number -> value.toInt()
            else -> 0
        }
        val dealingDeadline = when (val value = epochData["dealing_phase_deadline_block"]) {
            is String -> value.toLongOrNull() ?: 0L
            is Number -> value.toLong()
            else -> 0L
        }
        val verifyingDeadline = when (val value = epochData["verifying_phase_deadline_block"]) {
            is String -> value.toLongOrNull() ?: 0L
            is Number -> value.toLong()
            else -> 0L
        }
        
        // Parse DKG phase (server returns numeric values like "dkg_phase": 1)
        val dkgPhaseNum = when (val phase = epochData["dkg_phase"]) {
            is Number -> phase.toInt()
            is String -> phase.toIntOrNull() ?: 0
            else -> 0
        }
        val dkgPhase = DKGPhase.values().find { it.value == dkgPhaseNum } 
            ?: DKGPhase.UNDEFINED
        
        // Parse participants
        @Suppress("UNCHECKED_CAST")
        val participantsList = (epochData["participants"] as? List<Map<String, Any>>) ?: emptyList()
        val participants = participantsList.map { participantMap ->
            BLSParticipantInfo(
                address = participantMap["address"] as? String ?: "",
                percentageWeight = (participantMap["percentage_weight"] as? String ?: "0").toDouble(),  // Already in percentage format (0-100)
                secp256k1PublicKey = parseByteArrayFromChain(participantMap["secp256k1_public_key"]),
                slotStartIndex = when (val value = participantMap["slot_start_index"]) {
                    is String -> value.toIntOrNull() ?: 0
                    is Number -> value.toInt()
                    else -> 0
                },
                slotEndIndex = when (val value = participantMap["slot_end_index"]) {
                    is String -> value.toIntOrNull() ?: 0
                    is Number -> value.toInt()
                    else -> 0
                }
            )
        }
        
        // Parse group public key
        val groupPublicKey = parseByteArrayFromChain(epochData["group_public_key"])
        
        // Parse dealer parts
        @Suppress("UNCHECKED_CAST")
        val dealerPartsList = (epochData["dealer_parts"] as? List<Map<String, Any>>) ?: emptyList()
        val dealerParts = dealerPartsList.mapNotNull { dealerMap ->
            val dealerAddress = dealerMap["dealer_address"] as? String ?: ""
            if (dealerAddress.isEmpty()) return@mapNotNull null // Skip empty entries (participants who haven't submitted dealer parts)
            
            @Suppress("UNCHECKED_CAST")
            val commitmentsList = (dealerMap["commitments"] as? List<Any>) ?: emptyList()
            Logger.debug("Raw commitments from chain: ${commitmentsList.take(1)} (type: ${commitmentsList.firstOrNull()?.javaClass?.simpleName})")
            val commitments = commitmentsList.map { parseByteArrayFromChain(it) }
            Logger.debug("Parsed commitments sizes: ${commitments.map { it.size }}")
            
            @Suppress("UNCHECKED_CAST")
            val participantSharesList = (dealerMap["participant_shares"] as? List<Map<String, Any>>) ?: emptyList()
            val participantShares = participantSharesList.map { sharesMap ->
                @Suppress("UNCHECKED_CAST")
                val encryptedSharesList = (sharesMap["encrypted_shares"] as? List<Any>) ?: emptyList()
                Logger.debug("Raw encrypted shares from chain: ${encryptedSharesList.take(1).map { it?.javaClass?.simpleName }}") // Log first share type
                val encryptedShares = encryptedSharesList.map { parseByteArrayFromChain(it) }
                EncryptedSharesForParticipant(encryptedShares)
            }
            
            DealerPartStorage(
                dealerAddress = dealerAddress,
                commitments = commitments,
                participantShares = participantShares
            )
        }
        
        // Parse verification submissions
        @Suppress("UNCHECKED_CAST")
        val verificationSubmissionsList = (epochData["verification_submissions"] as? List<Map<String, Any>>) ?: emptyList()
        val verificationSubmissions = verificationSubmissionsList.mapNotNull { submissionMap ->
            @Suppress("UNCHECKED_CAST")
            val dealerValidityList = (submissionMap["dealer_validity"] as? List<Boolean>) ?: emptyList()
            if (dealerValidityList.isEmpty()) return@mapNotNull null // Skip empty entries (participants who haven't submitted verification vectors)
            
            VerificationVectorSubmission(dealerValidityList)
        }
        
        // Parse valid dealers
        @Suppress("UNCHECKED_CAST")
        val validDealersList = (epochData["valid_dealers"] as? List<Boolean>) ?: emptyList()
        
        // Parse validation signature
        val validationSignature = parseByteArrayFromChain(epochData["validation_signature"])
        
        EpochBLSData(
            epochId = epochId,
            iTotalSlots = iTotalSlots,
            tSlotsDegree = tSlotsDegree,
            participants = participants,
            dkgPhase = dkgPhase,
            dealingPhaseDeadlineBlock = dealingDeadline,
            verifyingPhaseDeadlineBlock = verifyingDeadline,
            groupPublicKey = groupPublicKey,
            dealerParts = dealerParts,
            verificationSubmissions = verificationSubmissions,
            validDealers = validDealersList,
            validationSignature = validationSignature
        )
        
    } catch (e: Exception) {
        Logger.error("Failed to parse BLS epoch data: ${e.message}")
        null
    }
}

/**
 * Helper function to parse byte arrays from the chain - handles multiple formats
 */
private fun parseByteArrayFromChain(data: Any?): ByteArray {
    return when (data) {
        null -> byteArrayOf()
        is String -> {
            if (data.isEmpty()) {
                byteArrayOf()
            } else {
                try {
                    java.util.Base64.getDecoder().decode(data)
                } catch (e: Exception) {
                    Logger.warn("Failed to decode base64 string: $data")
                    byteArrayOf()
                }
            }
        }
        is List<*> -> {
            try {
                // Handle case where chain returns byte arrays as integer lists
                @Suppress("UNCHECKED_CAST")
                val intList = data as List<Int>
                intList.map { it.toByte() }.toByteArray()
            } catch (e: Exception) {
                Logger.warn("Failed to convert integer list to byte array: $data")
                byteArrayOf()
            }
        }
        is ByteArray -> data
        else -> {
            Logger.warn("Unexpected data type for byte array: ${data.javaClass.simpleName}")
            byteArrayOf()
        }
    }
}

/**
 * Helper function to parse base64-encoded byte arrays from JSON (legacy)
 */
private fun parseBase64ByteArray(base64String: String?): ByteArray {
    return parseByteArrayFromChain(base64String)
} 