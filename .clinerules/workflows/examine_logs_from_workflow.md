Help the user figure out what happened in a Github Action test that failed.
<detailed_sequence_of_steps>
## 1. Identify the workflow to analyze
<ask_followup_question>
<question>What Tests do you want to analyze?</question>
</ask_followup_question>
The user should describe the Tests they want to analyze. Unless otherwise specified, assume they mean the "Integration Tests" workflow.

They could specify a run for a specific branch or commit or PR, or simply whatever the latest results are.
## 2. Load the workflow run overview
Use the GitHub CLI tool to find the workflow run specified by the user. Do NOT download the entire logs, they are too large. Instead, use the GitHub CLI tool to get the workflow run overview. This will give you a summary of the workflow run, including the status of each job and step. 

## 3. Matrix job
"Integration Tests" runs a matrix job, so each Test Class is run in it's own job, with it's own result (the job will fail if there are any test failures) and logs.

## 4. Download the logs
The logs will be Artifacts attached to the workflow run. Download them to a directory that has some idenifying information about the workflow run, such as the commit hash or PR number. Download the FILES locally, make sure they don't end up in the LLM context. Only download the logs for jobs that failed.

## 5. Identify the failing tests
There will be a file in the logs directory called `failures.log`. This file will contain a list of the tests that failed, along with the reason for the failure. You can use this file to identify which tests to analyze.

## 6. Summarize what has failed, ask for next steps
Now simply summarize the failures (how many tests, which tests, which classes)
<ask_followup_question>
<question>Which tests do you want to analyze?</question>
</ask_followup_question>
The user may want to analyze all the tests, or just a specific test.

## 6. Use the testermintlogs tool to analyze the log
Look at `examine_test_log.md` for details on how to analyze the logs. The test log for a failure will be in the logs directory, and will be named after the test case, with `ClassName-test name might have spaces.log` as the name.

## 7. Summarize all the findings and include CLI commands for further analysis
Focus on next steps and the likely cause of the failures. If a failure is clearly a known failure, be sure and emphasize that.
Format a copy-pasteable cmd to run lnav on the log file so the user can grab that and look at the log file themselves.
Fairly simple, just `lnav <log file>`, with the full path and quotes if needed.

## 8. Be prepared for a drill down request
The user may want you to drill down into a specific test failure. Use the Resources provided by the `testermintlogs` tool to understand how to drill down and look. You can also look at the test file itself to understand the test and what it is supposed to do. If you have questions about the test, ask the user for clarification.

</detailed_sequence_of_steps>