# Debugging Consensus Failure
A consensus failure will bring your test net to an absolute halt, with a message like this:
```
8:21AM ERR CONSENSUS FAILURE!!! err="+2/3 committed an invalid block: wrong Block.Header.AppHash.  Expected EA8EB0F570057ACD72D1D6190A780F4B66419ED9AA7AB623BC2956168C9C5C3E, got 5CB2D60B9595359B3C55F87F3E571B2893944455BCF31357E5F2606E11E3FDB9" module=consensus stack="goroutine 242 [running]:
runtime/debug.Stack()
	/usr/local/go/src/runtime/debug/stack.go:26 +0x5e
github.com/cometbft/cometbft/consensus.(*State).receiveRoutine.func2()
...
```

It means that your nodes have calculated the state differently, and therefore have different hashes.

**Be sure it's a wrong AppHash**. You can get other failures for when there is an error during the time you BUILD consensus, they will be a CONSENSUS FAILURE!!! but with different error messages.

## Main Causes
At present, there are two causes we have seen:
### Randomness in state calculation
All GUIDs, random numbers and anything else using randomness has to be calculated OUTSIDE chain state calculation. Any randomness in state calculations means consensus cannot be reached.
### Go's map iteration order
When iterating over a Go map, the order is **indeterminate**. It may be the same as another node, it MAY NOT. **This means any maps in your state will break your consensus**. It also means any iteration over maps that is **used** to generate lists or maps will ALSO break consensus.

## Debugging
It _should_ be pretty obvious when this happens, because you _just_ made some code changes and ran tests, and saw the failure. Then you smack your forehead, say "Oh yeah, don't use maps", and fix the issue.

**However**, if you're not sure, you can debug the state:
1. Note the block height of the failure.
1. Exec into a container that's running the node.
2. Run `inferenced export --height <block height>`.
3. The state will be in json on stdout.
4. Repeat this same process with other nodes

Now you can compare the json (after formatting it) and you'll see where the states of each node differed. That should be enough to zero in on the issue.

## Fixes
1. **Don't use maps**. Use slices or arrays. If we *need* maps, we'll need to implement a deterministic map.
2. **Don't use randomness** in state calculations. If you need randomness, it should be calculated outside of the state calculation. Passing it in from the API when creating a message, for example.
3. **Don't use map iteration to generate lists or maps**. If you need to do this, you'll need to implement a deterministic map, or you'll need to iterate on a sorted list of keys.

