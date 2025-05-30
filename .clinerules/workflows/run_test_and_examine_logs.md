<detailed_sequence_of_steps>
# 1. Identify the test to run
<ask_followup_question>
<question>What test (or tests) would you like to run?</question>
</ask_followup_question>
After the user has selected the test, verify that it is a test,tag or class by looking in the testermint project. Ask follow up questions if unnable to find the test.

# 2. Build and start the chain
```bash
cd local-test-net
./stop-rebuild-launch.sh
```

# 4. Run the test(s)
Use gradle for the testerming project. You can run by test names, class names or tags. For example:
```bash
cd testermint && ./gradlew test --tests "TestClassName.*"
```
or, for tags:
```bash
cd testermint && ./gradlew test --tests "*" -DexcludeTags=unstable,exclude
```

# 5. Examine the logs
First, look at the ./testermint/logs/failures.log file to see if there are any failures. If there are, you can use the testermintlogs tool to analyze the logs. The logs will be in the logs directory, and will be named after the test case, with `ClassName-test name might have spaces.log` as the name.

Refer to examine_test_log.md for full details on how to examine the logs. You can use the testermintlogs tool to analyze the logs, and you can also use the test code and product code to help you understand what is going wrong.

# 6. Summarize the findings
Report back failure/success as well as an analysis of any failures. If a failure is clearly a known failure, be sure and emphasize that.

</detailed_sequence_of_steps>