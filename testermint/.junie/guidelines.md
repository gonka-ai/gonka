# AI Guidelines for Testermint Dev
Please refer to the README.md for general info and guidelines on how to use testermint generally.

## Json serialization
All json serialization uses GSON, and is configured to automatically convert snake_case json into camelCase Kotlin properties, thus there is no need to add extra annotations or use camel_case in Kotlin code.

## DO NOT RUN TestermintTest EVERY TIME!!!
These tests (with test classes that inherit from TestermintTest) are actually quite expensive and slow to run, these are integration tests. Do not run them unless explicitly asked to.

