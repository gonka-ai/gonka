FROM haproxy:3.2-alpine

LABEL ai.gonka.component="proxy-router"

USER root
RUN mkdir -p /etc/haproxy /var/run/haproxy \
    && chown -R haproxy:haproxy /etc/haproxy /var/run/haproxy

COPY haproxy.cfg.template /etc/haproxy/haproxy.cfg.template
COPY versiond-backend.cfg.template /etc/haproxy/versiond-backend.cfg.template
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

USER haproxy

EXPOSE 80 443 18081 18082 8404

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
