# params_files_testermint.instructions.md
applyTo:
- inference-chain/proto/inference/inference/params.proto

---
If parameters are changed, look to make sure that the params Kotlin classes for testermint are updated accordingly. The classes file is located at `testermint/src/main/kotlin/data/AppExport.kt`. ALL parameters need to be in the Kotlin classes or Governance tests will fail.
