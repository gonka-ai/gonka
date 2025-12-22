# consensus_issues.instructions.md
applyTo:
 - inference-chain/**/*.go
---
This is a blockchain app. Certain things MUST NEVER enter the state of the blockchain or it will cause a consensus failure. Flag anything that might introduce any non-deteminism into the code. Examples:
- Using random number generators without a fixed seed
- Using system time without a fixed reference point
- Relying on external systems or APIs that may return different results at different times
- Using floating point arithmetic that may yield different results on different architectures. All FP math must use the `shopspring/decimal` package.
- Iterating over maps, since Go randomizes map iteration order

