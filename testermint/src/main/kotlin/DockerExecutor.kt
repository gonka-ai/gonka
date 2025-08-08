package com.productscience

import com.github.dockerjava.api.model.Volume
import com.github.dockerjava.core.DockerClientBuilder
import com.github.dockerjava.okhttp.OkDockerHttpClient
import org.tinylog.kotlin.Logger
import java.net.URI
import java.time.Duration
import java.util.concurrent.TimeUnit

data class DockerExecutor(val containerId: String, val config: ApplicationConfig) : CliExecutor {
    private val dockerClient = DockerClientBuilder.getInstance().build()
        
    override fun exec(args: List<String>, stdin: String?): List<String> {
        val output = ExecCaptureOutput()
        Logger.trace("Executing command: {}", args.joinToString(" "))
        
        val execCmd = if (stdin != null) {
            // Use shell to pass stdin via printf
            val stdinEscaped = stdin.replace("'", "'\\''")  // Escape single quotes
            val fullCommand = "printf '%s' '$stdinEscaped' | ${args.joinToString(" ")}"
            dockerClient.execCreateCmd(containerId)
                .withAttachStdout(true)
                .withAttachStderr(true)
                .withAttachStdin(false)
                .withTty(false)
                .withCmd("/bin/sh", "-c", fullCommand)
        } else {
            dockerClient.execCreateCmd(containerId)
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