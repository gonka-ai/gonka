# Known Failures
Information about failures that are recurring and mostly understood

## Upgrade Failures
Upgrades will only work if they are build correctly before running the `submit upgrade` test.

This will appear as a "container is not running" exception with an error along the lines of `spendable balance 0nicoin is smaller than`... It might have other failures, after an upgrade that are otherwise unexplainable

What this means is that the upgrade went to an OLD version of the app instead of a new version.

The fix is to run `make build-for-upgrade-tests` in the root folder, which will make sure up to date versions are available.

## Docker issues
Testermint uses docker to control the nodes. Older versions of docker might have issues when launching additional nodes:

```
"Couldn't connect to any seeds module=p2p" operation=base
```
will show up in the logs for a newly added node (join3). Advise the user to upgrade their version of Docker Desktop.

