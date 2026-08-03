FROM haproxy:3.2-alpine

# Round-robin reverse-proxy in front of N edge-api instances. Membership from
# DNS, health from active /readyz checks — no router-side state.

USER root
RUN apk add --no-cache socat

COPY haproxy.cfg.template /etc/haproxy/haproxy.cfg.template
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
