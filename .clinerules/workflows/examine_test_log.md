# Examine a test log
Your job is to use the testermintlogs tool to analyze a test log file. The log file is MUCH too large to open directly, and will either be too big for most models
or will cost a TON of tokens, so you should use the testermintlogs tool to sift through the logs.
<detailed_sequence_of_steps>
## 1. Identify the test to analyze
Look up the most recent test runs in ./testermint/logs/failures.log.
Present the user with a list of the most recent failures, and ask them to pick either one or describe which ones to analyze.
## 2. Load the test log file
The test log for a failure will be in the logs directory, and will be named after the test case, with `ClassName-test name might have spaces.log` as the name.

Load it into the testermintlogs tool by passing in the full path to the file.
## 3. Use the testermintlogs tool to analyze the log
Start by loading the Step by Step instructions resource. This will give you an overview of the approach to use for examining the log. There are other critical resources, but load them as needed, not before hand to reduce token usage. USE THE TOOL AND THE RESOURCES TO UNDERSTAND THE LOGS!

## 4. Expand the context as needed
The source code for the tests themselves are in the `testermint` folder in this project, and the product code is in `inference-chain` and `decentralized-api`. Use these (especially the test code) to help you understand what the test is doing and what might be going wrong.

## 5. Summarize all the findings as a final step.
Focus on next steps and the likely cause of the failures. If a failure is clearly a known failure, be sure and emphasize that.

Format a copy-pasteable cmd to run lnav on the log file so the user can grab that and look at the log file themselves.
Fairly simple, just `lnav <log file>`, with the full path and quotes if needed.

## 6. Offer to rerun certain tests
<ask_followup_question>
Ask the user if they want to rerun any test or tests based on the findings.
<question>Would you like to rerun any tests based on these findings?</question>
</ask_followup_question>

## 7. Backup the original log file for comparson

## 8. Rerun tests if requested
Use gradle for the testermint project. You can run by test names, class names or tags. For example:
```bash
cd testermint && ./gradlew test --tests "TestClassName.*"
```

## 9. Repeat the analysis if tests were rerun
Go back to step 1 based on the new test results. Pay special attention to if the failure happened again in the exact same way.

</detailed_sequence_of_steps>
