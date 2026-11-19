FROM nginx:1.30.7-alpine3.24

COPY nginx.conf.template /etc/nginx/template/nginx.conf.template
COPY entrypoint.sh /docker-entrypoint.d/40-render-edge-api-upstream.sh
RUN chmod +x /docker-entrypoint.d/40-render-edge-api-upstream.sh
