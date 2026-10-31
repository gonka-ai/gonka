# `internal` — helpers with no domain of their own

Five small packages that exist so the rest of the gateway does not have to repeat itself. Nothing here knows what a nonce or an escrow is.

| Package | What it is for |
| --- | --- |
| `logkey` | the field names every structured log line uses, in one place, so a collector can rely on them and a rename is a single edit |
| `leakcheck` | asserts in tests that a package left no goroutine behind — the gateway starts many, and one that outlives its owner is a leak nobody notices in production |
| `logcapture` | captures log output inside a test, so a line that is part of a contract can be asserted on |
| `safemath` | saturating `int64` arithmetic, so a counter stops at the bound instead of wrapping past it |
| `strictjson` | decodes bytes a host or a client sent: goccy is handed only what `encoding/json.Valid` accepts, because on some malformed input goccy reads past its buffer and panics, and a panic in an attempt's goroutine takes the whole gateway down |

`internal/` is not importable from outside `cmd/gateway`, which is the point: these are conveniences, not API.
