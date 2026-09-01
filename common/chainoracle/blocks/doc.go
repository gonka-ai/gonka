// Package blocks is the shared hash-only block-header contract.
//
// Hosts take Latest/Subscribe from Comet NewBlock. HTTP lookup is
// GET /block/:height and GET /block/:height/prove. Missing /block/:height
// (old dapi) returns a dummy header so L6 does not mark. Transport failures
// on a mounted route are 5xx so failover can still use chain At (§7.3).
//
// Production dapi and testenv mock-dapi mount those unary routes via
// blocks/server. Strict dependency rule: this package MUST NOT import
// the devshard module.
package blocks
