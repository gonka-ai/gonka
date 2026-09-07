//! Test-only CosmWasm fixture for Gonka's contract gRPC allowlist.
//!
//! This crate is unaudited, intentionally exposes an adversarial raw-query
//! interface, and must never be deployed to a public or production network.

#![forbid(unsafe_code)]

mod contract;
mod msg;
mod proto;
