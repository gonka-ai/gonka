i worked at plannin/poc and now switching to clean up. 

I: 
1. reviewing all code changes, comparing to a0cdbf64f6ac05f86f9edede1770c614a4cfc228
2. removing all unused or weird code, make sure it matches with .cursorrules/rules.md
3. removing all code related to poc v1, as it's not needed anymore 


Your goal:
1. Check current not commited stage to understand where am i and what i'm doing 
2. finish local task if smth it not builds of if some test not passing (fix issues / update tests if needed)
rely on smth like:
```
go test -count=1 ./...
```

separately in decentralized-api  and inference-chain
3. finish refactoring if neede (only local! in files i worked at)

-----
# Commit
when i say commit changes. commite with clean, clear and short message similar to /Users/morgachev/workspace/gonka/text.md but more cleare
be cleare that it's cleanup / refactoring / simplifaication

if i say ammend commite, ammend and update last message accordingly