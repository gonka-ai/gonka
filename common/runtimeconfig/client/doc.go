// Package client implements the GetRuntimeConfig long-poll consumer: gRPC loop,
// chain-poll fallback, and the adaptive supervisor that switches between them.
// It is transport-agnostic over Snapshot (common/runtimeconfig/types) and free of
// devshard imports so dapi, devshardd, and edge-api can share one client.
package client
