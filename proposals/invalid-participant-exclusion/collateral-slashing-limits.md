# Collateral Slashing Limits
As part of work on invalidation improvements, we want to make sure that slashing is never applied more than once for an epoch. While care is taken in  the invalidation logic not to call it twice, a stronger guarantee is preferable to avoid any oversight or changes in the future.

## x/collateral
### Adding slash reason to the Slash call to x/collateral
In order to differentiate, a reason will need to be specified for each slash. To prevent tight coupling, it will be a simple string. At present, only `invalidation` and `downtime` will be used

### Recording slashes as they happen
In the x/collateral module, we add a keyset, with a triple key of epoch_index,participant,reason. We set it whenever slashing succeeds

### Preventing future slashes
All slashes will be checked against this new keyset and ensured that it is unset before setting. If already set, an error should be returned

## x/inference
### Update calls, define reason consts
The slash calls should pass in the reason for the slash.

### Handle error when slashing is duplicated
`Slash` will return an error, which should be logged as an error, as this is not something we expect to happen, and it 
may represent a bug.

## Tests
Adding unit tests and integration tests as needed