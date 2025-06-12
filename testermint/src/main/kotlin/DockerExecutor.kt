package com.productscience

import com.github.dockerjava.api.model.Volume
import com.github.dockerjava.core.DockerClientBuilder
import org.tinylog.kotlin.Logger

data class DockerExecutor(val containerId: String, val config: ApplicationConfig) : CliExecutor {
    private val dockerClient = DockerClientBuilder.getInstance().build()
    override fun exec(args: List<String>): List<String> {
        val execCreateCmdResponse = dockerClient.execCreateCmd(containerId)
            .withAttachStdout(true)
            .withAttachStderr(true)
            .withTty(true)
            .withCmd(*args.toTypedArray())
            .exec()

        val output = ExecCaptureOutput()
        Logger.trace("Executing command: {}", args.joinToString(" "))
        val execResponse = dockerClient.execStartCmd(execCreateCmdResponse.id).exec(output)
        execResponse.awaitCompletion()
        Logger.trace("Command complete: output={}", output.output)
        return output.output
    }

    override fun kill() {
        Logger.info("Killing container, id={}", containerId)
        dockerClient.killContainerCmd(containerId).exec()
        dockerClient.removeContainerCmd(containerId).exec()
    }
    override fun createContainer(doNotStartChain: Boolean) {
        this.killNameConflicts()
        Logger.info("Creating container,  id={}", containerId)
        var createCmd = dockerClient.createContainerCmd(config.nodeImageName)
            .withName(containerId)
            .withVolumes(Volume(config.mountDir))
        if (doNotStartChain) {
            createCmd = createCmd.withCmd("tail", "-f", "/dev/null")
        }
        createCmd.exec()
        dockerClient.startContainerCmd(containerId).exec()
    }

    private fun killNameConflicts() {
        val containers = dockerClient.listContainersCmd().exec()
        containers.forEach {
            if (it.names.contains("/$containerId")) {
                Logger.info("Killing conflicting container, id={}", it.id)
                dockerClient.killContainerCmd(it.id).exec()
                dockerClient.removeContainerCmd(it.id).exec()
            }
        }
    }

}