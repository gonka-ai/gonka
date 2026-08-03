FROM haproxy:3.2-alpine

# Round-robin reverse-proxy in front of N edge-api instances. Membership from
# DNS, health from active /readyz checks — no router-side state.

# Root only to hand the render target to the haproxy user; socat is gone with
# the drain tooling — nothing in this image reads the runtime socket.
USER root
RUN mkdir -p /etc/haproxy /var/run/haproxy \
    && chown -R haproxy:haproxy /etc/haproxy /var/run/haproxy

COPY haproxy.cfg.template /etc/haproxy/haproxy.cfg.template
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

USER haproxy

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
