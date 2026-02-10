import com.productscience.data.*
import com.productscience.initCluster
import com.productscience.logSection
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.*
import java.time.Duration

class TrainingTests : TestermintTest() {
    @Test
    @Tag("unstable")
    fun test() {
        val (cluster, instance) = initCluster()
        val result = instance.node.exec(listOf("inferenced", "query", "inference", "hardware-nodes-all"))
        println("NODES!!!")
        println(result)

        val response = instance.api.startTrainingTask(
            StartTrainingDto(
                listOf(
                    HardwareResourcesDto("v5e", 2u),
                    HardwareResourcesDto("A600", 50u),
                ),
                TrainingConfigDto(
                    TrainingDatasetsDto("train", "test"),
                    100u,
                )
            )
        )

        instance.node.waitFor(
            check = { app ->
                // FIXME
                val result = app.execAndParse<Map<String, Any>>(listOf("query", "inference", "training-task-all"))
                println("QUERY RESULTS")
                println(result)
                true
            },
            description = "Training assigned",
            timeout = Duration.ofSeconds(40),
            sleepTimeMillis = 5000
        )

        println("RESPONSE!!!")
        println(response)
    }
}

@TestMethodOrder(MethodOrderer.OrderAnnotation::class)
class TrainingAllowListTests : TestermintTest() {
    @Test
    @Order(1)
    fun `message sending not allowed`() {
        val (cluster, genesis) = initCluster()
        val genesisAddress = genesis.node.getColdAddress()
        val joinAddress = cluster.joinPairs.first().node.getColdAddress()
        val messages: List<TxMessage> = getTrainingStartMessages(genesisAddress, joinAddress) + getTrainingExecMessages(genesisAddress)
        val responses = messages.associateWith {
            genesis.submitMessage(it)
        }

        softly {
            responses.forEach { (message, response) ->
                assertThat(response.code)
                    .`as` { "${message.type} returned not allowed. LOG: ${response.rawLog}" }
                    .isEqualTo(1139)
            }
        }
    }

    @Test
    @Order(3)
    fun `exec message sending allowed after vote`() {
        val (cluster, genesis) = initCluster()
        logSection("Adding genesis address to allow list")
        genesis.runProposal(
            cluster, MsgSetTrainingAllowList(
                addresses = listOf(),
                role = NodeRole.START.value,
            )
        )
        genesis.runProposal(
            cluster, MsgSetTrainingAllowList(
                addresses = listOf(genesis.node.getColdAddress()),
                role = NodeRole.EXEC.value
            )
        )
        val genesisAddress = genesis.node.getColdAddress()
        val joinAddress = cluster.joinPairs.first().node.getColdAddress()
        val messages: List<TxMessage> = getTrainingExecMessages(genesisAddress)
        logSection("Verifying messages can be sent")
        val responses = messages.associateWith {
            genesis.submitMessage(it)
        }

        softly {
            responses.forEach { (message, response) ->
                assertThat(response.code)
                    .`as` { "${message.type} not a valid request. LOG: ${response.rawLog}" }
                    .isNotEqualTo(18)
                assertThat(response.code)
                    .`as` { "${message.type} returned not allowed. LOG: ${response.rawLog}" }
                    .isNotEqualTo(1139)
            }
        }
    }

    @Test
    @Order(4)
    fun `start message sending allowed after vote`() {
        val (cluster, genesis) = initCluster()
        logSection("Adding genesis address to allow list")
        genesis.runProposal(
            cluster, MsgSetTrainingAllowList(
                addresses = listOf(),
                role = NodeRole.EXEC.value,
            )
        )
        genesis.runProposal(
            cluster, MsgSetTrainingAllowList(
                addresses = listOf(genesis.node.getColdAddress()),
                role = NodeRole.START.value
            )
        )
        val genesisAddress = genesis.node.getColdAddress()
        val joinAddress = cluster.joinPairs.first().node.getColdAddress()
        val messages: List<TxMessage> = getTrainingStartMessages(genesisAddress, joinAddress)
        logSection("Verifying messages can be sent")
        val responses = messages.associateWith {
            genesis.submitMessage(it)
        }

        softly {
            responses.forEach { (message, response) ->
                assertThat(response.code)
                    .`as` { "${message.type} not a valid request. LOG: ${response.rawLog}" }
                    .isNotEqualTo(18)
                assertThat(response.code)
                    .`as` { "${message.type} returned not allowed. LOG: ${response.rawLog}" }
                    .isNotEqualTo(1139)
            }
        }
    }


    @Test
    @Order(2)
    fun `test exec allow list messages`() {
        val (cluster, genesis) = initCluster()
        val role = NodeRole.EXEC.value
        val currentAllowList = genesis.node.getTrainingAllowList(NodeRole.fromValue(role))
        assertThat(currentAllowList).isEmpty()
        logSection("Adding genesis address to allow list")
        genesis.runProposal(
            cluster, MsgAddUserToTrainingAllowList(
                address = genesis.node.getColdAddress(),
                role = role
            )
        )
        val newAllowList = genesis.node.getTrainingAllowList(NodeRole.fromValue(role))
        assertThat(newAllowList).hasSize(1)
        assertThat(newAllowList.first()).isEqualTo(genesis.node.getColdAddress())
        logSection("Replacing entire address list")
        genesis.runProposal(
            cluster,
            MsgSetTrainingAllowList(addresses = cluster.joinPairs.map { it.node.getColdAddress() }, role = role)
        )
        val replacedAllowList = genesis.node.getTrainingAllowList(NodeRole.fromValue(role))
        assertThat(replacedAllowList).hasSize(cluster.joinPairs.size)
        assertThat(replacedAllowList).containsAll(cluster.joinPairs.map { it.node.getColdAddress() })
        logSection("Removing join address from allow list")
        genesis.runProposal(
            cluster, MsgRemoveUserFromTrainingAllowList(
                address = cluster.joinPairs.first().node.getColdAddress(),
                role = role
            )
        )
        val finalAllowList = genesis.node.getTrainingAllowList(NodeRole.fromValue(role))
        assertThat(finalAllowList).doesNotContain(cluster.joinPairs.first().node.getColdAddress())
    }

    private fun getTrainingStartMessages(
        genesisAddress: String,
        joinAddress: String,
    ) = listOf(
        MsgAssignTrainingTask(
            creator = genesisAddress,
            taskId = 5L,
            assignees = listOf(TrainingTaskAssignee(joinAddress, nodeIds = listOf("node1")))
        ),
        MsgClaimTrainingTaskForAssignment(
            creator = genesisAddress,
            taskId = 5L
        ),
        MsgCreateDummyTrainingTask(
            creator = genesisAddress,
            task = TrainingTask(
                requestedBy = genesisAddress,
                id = 500,
                assigner = genesisAddress,
                hardwareResources = listOf(
                    TrainingHardwareResources(
                        type = "v5e",
                        count = 5L
                    )
                ),
                assignees = listOf(TrainingTaskAssignee(joinAddress, nodeIds = listOf("node1"))),
            ),
        ),
        MsgCreateTrainingTask(
            creator = genesisAddress,
            hardwareResources = listOf(
                TrainingHardwareResources(
                    type = "v5e",
                    count = 5L
                )
            ),
            config = TrainingConfig(
            )
        ),
    )

    private fun getTrainingExecMessages(
        genesisAddress: String,
    ) = listOf(
        // We are exluding this because there appears to be a serialization issue,
        //            MsgSubmitTrainingKvRecord(
        //                creator = genesisAddress,
        //                taskId = 50L,
        //                participant = joinAddress,
        //                key = "key",
        //                value = "value"
        //            ),
        MsgJoinTraining(
            creator = genesisAddress,
            req = JoinTrainingRequest(
                "node", 50L, 5
            )
        ),
        MsgJoinTrainingStatus(
            creator = genesisAddress,
            req = JoinTrainingRequest(
                nodeId = "node",
                runId = 50L,
                outerStep = 5
            )
        ),
        MsgSetBarrier(
            creator = genesisAddress,
            req = SetBarrierRequest(
                barrierId = "barrier",
                nodeId = "node",
                runId = 50L,
                outerStep = 5
            )
        ),
        MsgTrainingHeartbeat(
            creator = genesisAddress,
            req = HeartbeatRequest(
                nodeId = "node",
                runId = 50L,
                outerStep = 5,
                innerStep = 5,
                timestamp = 5.5,
                epoch = 4,
                localRank = 5
            )
        )
    )
}


// Invalid request (ValidateBasic fails)
// TxResponse(height=0, txhash=CBBEE418ABB959AC72D865568252A5403DF4C97669DDD8140AEF59448A60E019, codespace=sdk, code=18, transactionData=null, rawLog=assignees[0].node_ids must be non-empty: invalid request, info=null, gasWanted=0, gasUsed=0, tx=null, timestamp=null, events=null)

// Not allowed:
// TxResponse(height=109, txhash=CADDF188BAEDD078DD7AE15C750E99DF852793B84FA557209613CC94A7A6D706, codespace=inference, code=1139, transactionData=, rawLog=failed to execute message; message index: 0: training not allowed for this address, info=