FROM haproxy:3.2-alpine

ARG CATALOG_CACHE_PROTOCOL_VERSION=2
LABEL ai.gonka.component="proxy-router" \
      ai.gonka.catalog-cache-protocol-version="${CATALOG_CACHE_PROTOCOL_VERSION}" \
      ai.gonka.proxy-policy-contract="1"

USER root
RUN apk add --no-cache curl jq socat \
    && mkdir -p /etc/haproxy /var/run/haproxy /usr/local/lib/proxy-router \
       /usr/local/lib/router-runtime /var/lib/gonka-router \
    && chown -R haproxy:haproxy /etc/haproxy /var/run/haproxy \
       /var/lib/gonka-router

COPY proxy-router/haproxy.cfg.template /etc/haproxy/haproxy.cfg.template
COPY proxy-router/versiond-backend.cfg.template /etc/haproxy/versiond-backend.cfg.template
COPY proxy-router/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY proxy-router/route-status /usr/local/lib/proxy-router/route-status
COPY router-runtime/catalog-cache router-runtime/catalog-reconciler router-runtime/catalog-status /usr/local/lib/router-runtime/
RUN chmod +x /usr/local/bin/entrypoint.sh \
    /usr/local/lib/proxy-router/route-status \
    /usr/local/lib/router-runtime/catalog-cache \
    /usr/local/lib/router-runtime/catalog-reconciler \
    /usr/local/lib/router-runtime/catalog-status

USER haproxy

EXPOSE 80 443 18081 18082 8404

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
