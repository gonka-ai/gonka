# Step by Step Approach
A guide for how to begin understanding a log.

## Using resources
In addition to the Guides available as resources in the server, there are example SQL queries available as well. These can be used as good starting points for getting the basics of a failure.

## IMPORTANT: Test Sections!
TestSections (LIKE 'TestSection:%') give context about what is happening, and should be included in all steps!

## Step 1: Look at Errors first
1. All Errors
2. All TestSections

This is a good starting point for an error, letting you know exactly where the test went wrong. Maybe it was during initialization, which would indicate
a problem with config or bad state. But if it's in the test itself, the errors can tell you whether it was an 
unexpected problem such as an exception or an assertion failure, as well as many other issues.

**HOWEVER**: There are usually a few errors expected here and there, especially when any part of the system
is rebooted or reinitialized. Additionally, Upgrade tests will have several errors in them regarging
missing upgrade code, which is what TRIGGERS the upgrade.
### Known Errors: (ignore)
#### Related to restarting the chain:
- Stopping peer for error err=EOF
- Error dialing seed
- Couldn't connect to any seeds

## Step 2: Look at Known Failures
in the resource known_failures is a list of known failures, usually focusing on the error message and other
things you can do to narrow it down. Look for any known failures once you've gotten the list of errors and warnings
and there isn't anything obvious.

## Step 3: Load more guides
Load the remaining guides for context if there isn't a clear problem at this point.

## Step 4: Look at Errors and warning
1. All Errors (ERROR)
2. All Warnings (WARN)
3. All TestSections (LIKE 'TestSection:%')

This can give some more hints about what went wrong.

### Known Warnings (ignore)
#### Related to PoC
- Calculate: Participant didn't receive enough validations

## Step 5: Narrow by subsection
Usually, the best next step is to narrow by subsection and see what the problem might be.

## Step 6: Look at the test itself
If available, look at the test in the `testermint` directory to try and make sense of assertions.
