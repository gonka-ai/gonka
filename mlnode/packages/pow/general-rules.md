INTRODUCTION
This document is our worksheet for proposal implementation. This documentation contains only tasks, their statuses and implementation details.

STRICT RULES FOR LLM ASSISTANTS:
1. NEVER delete this introduction section
2. NEVER use emojis anywhere in code, comments, or documentation - they are forbidden
3. Work ONLY on tasks marked [WIP] - ignore all others
4. All code must be inside `packages/pow` directory only
5. Reference code using function names and actual code snippets, NOT line numbers
6. Keep all solutions minimalistic, simple, clear and concise
7. Write minimal code that solves the exact requirement
8. All implementations MUST NOT break existing tests
9. Preserve the exact task format structure below
10. Every task MUST be covered properly by tests - either unit tests or GPU tests
11. After task marked as [DONE], ALL tests MUST be verified to pass
12. TESTS ARE CRITICAL - they ensure code quality and prevent regressions

TESTING FRAMEWORK:
We run 2 types of tests locally:

**Unit Tests (CPU-only)**: `make unit-tests-local`
- Location: `tests/unit/`
- Purpose: Test logic, algorithms, and non-GPU components
- Example: `test_worker_nonce_iterator()` validates NonceIterator collision-free nonce generation
- Run single test: `pytest -v tests/unit/test_empty.py::test_worker_nonce_iterator`
- Run specific test pattern: `pytest -k "nonce" tests/unit/`

- **Performance Tests**: `CUDA_VISIBLE_DEVICES=0,1 python3 tests/test_nonce_rate.py`

**GPU Tests**: `make unit-tests-gpu-local` 
- Location: `tests/unit-gpu/`
- Purpose: Test full GPU workflow including model initialization, compute operations, and controller coordination
- Example: `test_compute_simple()` validates end-to-end proof generation and validation
- Example: `test_parallel_controller()` tests multi-GPU coordination across all available devices
- Run single test: `pytest -v tests/unit-gpu/test_example.py::test_compute_simple`
- Run specific test pattern: `pytest -k "controller" tests/unit-gpu/`

**IMPORTANT**: GPU tests automatically detect and utilize ALL available CUDA devices via `torch.cuda.device_count()` and MUST work on machines with different GPU configurations (single GPU, multi-GPU, different GPU types). Tests validate that the system scales properly across hardware configurations.

TASK FORMAT (mandatory structure):
[STATUS]: Task Title
    Detailed description with function references like `ClassName.method_name()`: `actual_code_snippet`
    
VALID STATUS VALUES:
- [TODO]: Not started
- [WIP]: Currently working on this task
- [DONE]: Completed successfully

DOCUMENTATION STYLE:
- Reference functions: `ClassName.method_name()` 
- Quote actual code: `variable = some_value`
- File references: `filename.py` without paths
- NO line numbers - use function names and code quotes instead
