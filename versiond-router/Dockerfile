FROM haproxy:3.2-alpine

# Sticky reverse-proxy in front of N versiond instances. Hashes by escrow id
# from the URL so the same session always lands on the same versiond, and takes
# a host out of rotation from active /readyz checks rather than from router-side
# state. Plays nice with SSE (generous tunnel timeout, no response buffering).

USER root
RUN apk add --no-cache socat

COPY haproxy.cfg.template /etc/haproxy/haproxy.cfg.template
COPY pool-backend.cfg.template /etc/haproxy/pool-backend.cfg.template
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
# Internal diagnostic, deliberately off PATH: it is a formatter over the
# Runtime API that the citest harness parses, not an operator CLI.
COPY pool-status /usr/local/lib/versiond-router/pool-status
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/lib/versiond-router/pool-status

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
