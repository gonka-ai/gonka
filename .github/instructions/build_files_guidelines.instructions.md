# build_files_guidelines.instructions.md
applyTo:
 - .github/workflows/*.yml
 - **/Makefile
 - **/Dockerfile
 - **/go.mod
---
- Versions in all makefiles, dockerfiles and github workflows and go.mod files should NEVER be "latest" as this results in undetermined future behavior
