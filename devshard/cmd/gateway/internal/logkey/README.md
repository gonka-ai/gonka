# Log keys

`vocabulary.go` owns the log field vocabulary: attempt deadlines and delivery stages, client requests and replies, chain and escrow state, process and transport, height-follower sources and runtime-governance feeds. Emitters reference the constants so compiler checks carry every rename. Height-feed behavior is described in `../../heights/README.md`; governance-feed composition is in `../../app/runtime_params.go`.

`logkey_test.go` parses this vocabulary and finds gateway log keys across direct calls and key/value slices. `keyLiterals` reads only even offsets in an alternating key/value run, and `anyElement` recognizes both `any` and `interface{}`. These checks share the production constants rather than maintaining a second list.
