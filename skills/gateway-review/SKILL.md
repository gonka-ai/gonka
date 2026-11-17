---
name: gateway-review
description: One round of fresh-eyes review of your own finished change to the new gateway (devshard/cmd/gateway) — two read-only reviewers in parallel, one for correctness and one for craft, every finding verified before it is fixed. Run it when the change is complete and its tests pass, and only when you or the change's author asks for it.
---

# Gateway review

Your own diff, read by fresh eyes, once. Run it when you or the author of the change asks for a review round; never start it on your own initiative.

Invoking this skill **is** the request to spawn agents. Spawn them.

## When to run

Run it when the change is complete and its tests pass — not on a work in progress, and not as a substitute for running the tests yourself. A reviewer reading half a change reports noise.

Do not run it on a diff you did not write, or to review someone else's PR. That is a different job.

## The round

One round. Two reviewers. No second round.

1. **Package the diff into a file.** From the repository root, `skills/gateway-review/scripts/review-package.sh BASE [HEAD]` writes it and prints the path; with BASE alone the working tree is the far end, untracked files included. Hand reviewers that path — never paste a diff into a prompt.
2. **Dispatch both reviewers in a single message** so they run at once. Give each: the package path, the one half it owns, the binding rules below and the style checks under them. Every prompt also carries the machine rule below verbatim: leave nothing running, generate no synthetic load, clean up what you write.
3. **Verify every finding before acting on it.** A reviewer's claim is a hypothesis, not a fact. Confirm it against the code; where the claim is about behaviour, prove it with a failing test or a mutation. Reject what does not survive, and say so.
4. **Fix what survived.** Then re-run the tests the change touches.
5. **Report and stop.** If the fixes were large enough to deserve fresh eyes of their own, say that plainly and let the author decide whether to run the skill again — do not start another round yourself.

## The two reviewers

| reviewer | what it owns |
| --- | --- |
| correctness | edge cases, error paths, concurrency, what happens on the second call; and whether the tests would fail if the code were wrong — behaviour not implementation, deterministic, nothing passing because a test was weakened |
| craft | architecture and boundaries, what leaks across them; consistency with the surrounding code in naming, idiom, error style and comment density; duplication and DRY; KISS and YAGNI; whether the docs say what the code does |

Reviewers are read-only. They report; you fix.

## Binding rules for the new gateway

Hand these to both reviewers. A breach is a finding.

- **Gateway-only.** A change never alters code a host binary runs. Code outside `devshard/cmd/gateway` is touched only when hosts never run it; prove it with `go list -deps` and the callers.
- **Names** are full, meaningful words — no cryptic abbreviations, in code and in tests alike. Booleans read as predicates, functions as verbs, collections as plurals.
- **Comments.** None on unexported identifiers or struct fields. An exported identifier gets at most one doc line, starting with its name. No comment inside a `const (...)` or `var (...)` block: one before the block, or none. Reasoning belongs in the package's Markdown docs, written declaratively. A comment that names a CVE or a security reason stays next to the code it guards, inside a block or a literal included.
- **No magic values.** A number or an empty string with a meaning is a named constant, used at every return and comparison — no bare `""` or `0` standing for "none" or "unknown".
- **Grouping.** Adjacent top-level `const` and `var` declarations go in one block each; wire strings and typed enums live in the package's `vocabulary.go`.
- **State changes through methods** where that fits, rather than one type assigning another's fields.
- **File size.** A production file over 500 lines is split; test files may be longer.
- **Tests** carry a `// Test flow:` block, are deterministic (no real clock, network or sleeps), and test behaviour, not implementation.
- **Docs.** A new environment variable, setting or behaviour is documented where its neighbours are (`docs/operations.md` for variables). The docs describe the new gateway in its own terms; no run numbers.
- **Gates** before the change is called done: `go build ./...`, `go vet ./...`, `golangci-lint run`, `go test -race` on the touched packages.

## Style checks

The craft reviewer goes through the whole diff for each of these; a breach is a finding.

- **Magic numbers.** Every number in production code that carries a meaning is a named constant whose name says what the value means — never `two`, `half` or a bare `divisor`. The struct literal in `config/defaults.go` is exempt: each field names its value. A division by 2 that is the arithmetic itself, such as a midpoint or a median's middle index, needs no name. `golangci-lint run --default=none --enable=mnd --tests=false` lists the candidates.
- **Comments inside const and var blocks.** None inside a block; one comment before it, or none. The facts a moved comment carried must still be in that comment or in the README section it points to. A comment naming a CVE or a security reason stays next to the code it guards.
- **Empty-string and zero sentinels.** A `return ""`, `== ""` or `!= ""` on a named string type, or a bare `0` that means "unknown" or "none", is replaced by a named constant declared with the type's other values.
- **Grouping.** Adjacent top-level `const` and `var` declarations form one block; wire strings and typed enums sit in the package's `vocabulary.go`.
- **File size.** No production file grows past 500 lines; test files may.
- **Formatting.** `gofmt -l devshard/cmd/gateway` prints nothing.

## Rules that keep the round honest

- **Never pre-judge a finding for a reviewer.** No "do not flag X", no "at most minor". If you think a finding will be wrong, let it come and reject it with evidence.
- **Never fix by weakening a test.** If a test now fails, the change is wrong until proven otherwise.
- **A finding you choose not to fix is recorded, not dropped.** Name it and say why in the final report.
- **Do not let a reviewer rewrite the task.** Scope creep dressed as a finding is still scope creep; note it as a follow-up.
- **Verify, then trust.** Agents state wrong things confidently, and so does your own instrumentation — measure before you believe either.
- **A reviewer leaves the machine as it found it.** No synthetic CPU load — no busy loops (`while :; do :; done`), no `yes`, no `stress`, no "run it under contention to shake out a flake". Nothing may be left running in the background: every process a reviewer starts is waited on or killed in the same command that started it, because an agent that is interrupted never reaches its own cleanup line and orphans whatever it backgrounded. Build artifacts and logs a reviewer writes are its own to delete. If a finding really needs load or a reproduced flake, report it as a question and let the caller run it.
- **Two agents, no more.** They already share the caller's cores; two of them running `go test -race` is most of a machine.

## Reporting

End with, in a few lines: what the round found, what you fixed, what you rejected and why, and what remains open.
