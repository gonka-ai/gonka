FROM haproxy:3.0-alpine

# Sticky reverse-proxy in front of N versiond instances. Hashes by escrow id
# from the URL so the same session always lands on the same versiond, and takes
# a host out of rotation from active /readyz checks rather than from router-side
# state. Plays nice with SSE (generous tunnel timeout, no response buffering).

USER root
RUN apk add --no-cache socat

COPY haproxy.cfg.template /etc/haproxy/haproxy.cfg.template
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
COPY gonka-drain /usr/local/bin/gonka-drain
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/gonka-drain

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
