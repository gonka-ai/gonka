package com.productscience

import com.github.dockerjava.api.exception.NotFoundException
import com.github.dockerjava.api.model.Volume
import com.github.dockerjava.core.DockerClientBuilder
import com.github.dockerjava.okhttp.OkDockerHttpClient
import org.tinylog.kotlin.Logger
import java.net.URI
import java.time.Duration
import java.util.concurrent.TimeUnit

class DockerExecutor(val containerId: String, val config: ApplicationConfig) : CliExecutor {
    private val dockerClient = DockerClientBuilder.getInstance().build()
    @Volatile
    private var currentContainerId: String = containerId
        
    override fun exec(args: List<String>, stdin: String?): List<String> {
        try {
            return execWithContainer(currentContainerId, args, stdin)
        } catch (e: NotFoundException) {
            val refreshedContainerId = refreshContainerId()
            if (refreshedContainerId == null || refreshedContainerId == currentContainerId) {
                throw e
            }
            Logger.warn(
                "Docker exec hit stale container ID {}, retrying with {} for pair {}",
                currentContainerId,
                refreshedContainerId,
                config.pairName
            )
            currentContainerId = refreshedContainerId
            return execWithContainer(currentContainerId, args, stdin)
        }
    }

    private fun execWithContainer(targetContainerId: String, args: List<String>, stdin: String?): List<String> {
        val output = ExecCaptureOutput()
        Logger.trace("Executing command: {}", args.joinToString(" "))

        val execCmd = if (stdin != null) {
            // Use shell to pass stdin via printf
            val stdinEscaped = stdin.replace("'", "'\\''")
            val fullCommand = "printf '%s' '$stdinEscaped' | ${args.joinToString(" ")}"
            dockerClient.execCreateCmd(targetContainerId)
                .withAttachStdout(true)
                .withAttachStderr(true)
                .withAttachStdin(false)
                .withTty(false)
                .withCmd("/bin/sh", "-c", fullCommand)
        } else {
            dockerClient.execCreateCmd(targetContainerId)
                .withAttachStdout(true)
                .withAttachStderr(true)
                .withAttachStdin(false)
                .withTty(false)
                .withCmd(*args.toTypedArray())
        }
        
        val execCreateCmdResponse = execCmd.exec()
        val execResponse = dockerClient.execStartCmd(execCreateCmdResponse.id).exec(output)
        
        val completed = execResponse.awaitCompletion(60, TimeUnit.SECONDS)
        if (!completed) {
            Logger.warn("Command timed out after 60 seconds: {}", args.joinToString(" "))
            throw RuntimeException("Docker exec command timed out: ${args.joinToString(" ")}")
        }
        
        Logger.trace("Command complete: output={}", output.output)
        return output.output
    }

    private fun refreshContainerId(): String? {
        val candidateNames = setOf(
            "/${config.pairName}-node",
            "${config.pairName}-node"
        )

        return dockerClient.listContainersCmd().exec()
            .firstOrNull { container ->
                container.names.any { it in candidateNames }
            }
            ?.id
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
