FROM nginx:1.27-alpine

# Sticky reverse-proxy in front of N versiond instances. Hashes by escrow id
# from the URL so the same session always lands on the same versiond. Plays
# along with nginx:alpine's stock /docker-entrypoint.d/ pipeline.

COPY nginx.conf.template /etc/nginx/template/nginx.conf.template
COPY entrypoint.sh /docker-entrypoint.d/40-render-versiond-upstream.sh
RUN chmod +x /docker-entrypoint.d/40-render-versiond-upstream.sh
