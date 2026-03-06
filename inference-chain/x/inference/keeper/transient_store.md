---
id: transient_store
type: implementation
regex_filters: ["transientStoreService"]
---
- TransientStore is a part of the Cosmos SDK. 
- It stores data purely in memory. 
- It stores the data as bytes, similar to KVStore.
- Bytes will STILL need to be marshaled and unmarshalled into ProtoBuf.
- It gets rolled back for failed transactions, just like KVStore. 
- At the end of a block (during commit), it gets entirely deleted, so the scope is ALWAYS only during the current block.
- Since it doesn't involve disk writes or modifying a merkle tree, it is MUCH cheaper to write to. 
- It is slightly cheaper to read from, but is usually not worth the trouble as a pure read cache.
- It could be useful as a cache for composite or calculated data, or data stored deep in a large object for marshaling
- The production TransientStore does not support Iterators
