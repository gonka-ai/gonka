# Enable Tool / Function Calling for Qwen/QwQ-32B and Qwen/Qwen3-32B-FP8

## Summary

This proposal suggests enabling OpenAI-compatible tool/function calling support for `Qwen/Qwen3-32B-FP8`, `Qwen/QwQ-32B` models in the Gonka inference network.

Tool calling is a foundational capability for modern IDE-integrated AI agents (e.g. Continue, Cursor, VS Code extensions). Without this capability, Gonka-hosted models are effectively limited to chat-only usage and cannot participate in agent-based workflows such as code application, file edits, or command execution.

This is an off-chain proposal intended to gather feedback and reach consensus before proceeding to on-chain governance.

---

## Motivation

Developer-facing AI tooling has rapidly shifted from pure chat interfaces to **agent-based interaction models**, where the LLM is expected to:

- call structured tools/functions
- apply code changes
- read and write files
- execute commands
- reason over repositories

While Gonka already provides an OpenAI-compatible inference gateway, models are currently not exposed as tool-capable. As a result:

- IDEs disable agent / apply modes
- structured workflows fall back to plain chat
- Gonka inference cannot be used as a drop-in backend for popular developer tools

Enabling tool calling unlocks a **new and higher-value demand segment** for the network: long-running, tool-heavy, IDE-driven inference sessions.

---

## Evidence / Reproduction

The current behavior can be reproduced as follows:

1. The `Qwen/Qwen3-32B-FP8` model is visible via the OpenAI-compatible `/v1/models` endpoint.
2. Standard chat completions work as expected.
3. Requests that include `tools` / `tool_choice` are rejected, ignored, or cause IDE clients to disable agent features.
4. IDE integrations (e.g. Continue, Cursor) fall back to chat-only mode when using this model.

This behavior is consistent with the model not being declared or supported as tool-capable.

---

## Proposed Change

- Enable OpenAI-compatible tool/function calling for `Qwen/Qwen3-32B-FP8` and `Qwen/QwQ-32B` models
- Expose the capability explicitly so clients can reliably enable agent workflows
- Ensure the feature is **opt-in per model** and does not affect existing chat-only usage

This proposal does **not** mandate a specific implementation approach and intentionally leaves technical details to be validated by core maintainers.

---

## Scope

### In Scope
- Tool / function calling capability
- OpenAI-compatible request and response schema
- IDE agent enablement

### Out of Scope
- Model fine-tuning or retraining
- Changes to pricing, PoC, or economic parameters
- Mandatory streaming support (may be optional or phased)

---

## Compatibility and Safety

- No breaking changes to existing clients
- Chat-only usage remains unaffected
- Tool calling is activated only when explicitly requested
- Backwards compatibility with non-tooling clients is preserved

---

## Technical Notes

- Gonka already supports an OpenAI-compatible gateway interface
- Tool calling requires:
  - accepting structured `tools` definitions
  - returning structured tool calls/results
- The exact responsibility split between gateway and inference runtime is an open question and requires maintainer input

---

## Open Questions for Maintainers

- Where should tool-calling capability be declared and enforced (gateway vs inference runtime)?
- Is there an existing reference implementation for tool-capable models?
- Are there runtime constraints (timeouts, payload size, streaming) that should be documented upfront?

---

## Success Criteria

This proposal is considered successful if:

- IDE clients can reliably enable agent / apply modes when selecting `Qwen/Qwen3-32B-FP8` and`Qwen/QwQ-32B`
- Tool-calling requests follow OpenAI-compatible schemas without workarounds
- Chat-only usage remains unchanged
- The network gains support for IDE-driven agent workloads

---

## Path Forward

If consensus is reached on this proposal:

1. Submit an on-chain `TextProposal` referencing this document
2. If required, follow with a `SoftwareUpgradeProposal` to implement runtime changes

---

## References

- Gonka governance documentation  
  https://gonka.ai/host/proposals/

- Related off-chain proposal example  
  https://github.com/gonka-ai/gonka/pull/385

- OpenAI tool/function calling specification  
  https://platform.openai.com/docs/guides/function-calling
