#!/bin/sh
# Bootstrap persistent router state and render the initial nginx configuration.
# Runtime mutations are performed by gonka-routerctl under the same file lock.
set -eu

exec /usr/local/bin/gonka-routerctl bootstrap
