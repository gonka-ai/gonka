package com.productscience.prompts

import io.modelcontextprotocol.kotlin.sdk.*
import io.modelcontextprotocol.kotlin.sdk.server.RegisteredPrompt

val analyzeLogPrompt = RegisteredPrompt(
    Prompt(
        "Analyze log",
        "A good prompt for requesting an LLM analyze a testermint log",
        listOf(
            PromptArgument("path_to_logfile", "Full path to a log file to analyze", true)
        )
    )
) { request ->
    val logFile = request.arguments?.get("path_to_logfile")!!
    GetPromptResult("A prompt for opening and examining a logfile", listOf(
        PromptMessage(Role.user, TextContent("""
            Your job is to analyze a large logfile (at `$logFile`) using the `testermintlogs` mcp server. The logs are much too large to load directly into the context, so you MUST use this tool to analyze them. Follow these steps:
                        
            - Load the test log file
Load it into the testermintlogs tool by passing in the full path to the file.

- Use the testermintlogs tool to analyze the log per the resources in the tool
Rely on the Resources made available by the testermintlogs tool.
Start by loading the Step by Step instructions resource. This will give you an overview of the approach to use for examining the log. There are other critical resources, but load them as needed, not before hand to reduce token usage.

        """.trimIndent()))
    ))
}
