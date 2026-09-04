FROM haproxy:3.2-alpine

LABEL ai.gonka.component="versiond-router"

# Sticky reverse-proxy in front of N versiond instances. Hashes by escrow id
# from the URL so the same session always lands on the same versiond, and takes
# a host out of rotation from active /readyz checks rather than from router-side
# state. SSE is streamed without response buffering and bounded by an idle
# timeout rather than by a total request deadline.

# Root only long enough to install socat and hand the writable paths to the
# haproxy user the base image already defines: the entrypoint renders into
# /etc/haproxy, and the stats socket lives under /var/run/haproxy. (/var/run is
# a symlink to /run; a runtime that mounts a tmpfs over /run would discard the
# directory — none of the shipped compose files do.)
USER root
RUN apk add --no-cache curl jq socat \
    && mkdir -p /etc/haproxy /var/run/haproxy /var/lib/gonka-router \
       /usr/local/lib/router-runtime \
    && chown -R haproxy:haproxy /etc/haproxy /var/run/haproxy \
       /var/lib/gonka-router

COPY versiond-router/haproxy.cfg.template /etc/haproxy/haproxy.cfg.template
COPY versiond-router/pool-backend.cfg.template /etc/haproxy/pool-backend.cfg.template
COPY versiond-router/entrypoint.sh /usr/local/bin/entrypoint.sh
# Internal diagnostic, deliberately off PATH: it is a formatter over the
# Runtime API that the citest harness parses, not an operator CLI.
COPY versiond-router/pool-status /usr/local/lib/versiond-router/pool-status
COPY router-runtime/catalog-cache router-runtime/catalog-reconciler router-runtime/catalog-status router-runtime/version-contract /usr/local/lib/router-runtime/
RUN chmod +x /usr/local/bin/entrypoint.sh \
    /usr/local/lib/versiond-router/pool-status \
    /usr/local/lib/router-runtime/catalog-cache \
    /usr/local/lib/router-runtime/catalog-reconciler \
    /usr/local/lib/router-runtime/catalog-status

USER haproxy

# HAProxy interprets SIGUSR1 as a soft stop: listeners close while established
# connections continue until the runtime grace period expires.
STOPSIGNAL SIGUSR1

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
