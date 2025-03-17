## Share Rewards
When a task is validated, the rewards are shared between the validator(s) and the original worker.

There are some complications:
- More than one validator can come in, and they will cause a redistribution of the rewards each time.
- If the reward is not divisible by the number of validators, the remainder is given to the first worker (who will be the original worker).
- BUT if we redistribute an already uneven amount, we have to also redistribute the remainder.

The function returns the list of adjustments that should be made.