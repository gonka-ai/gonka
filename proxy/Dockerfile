FROM nginx:1.28-alpine

# Install curl, gettext (for envsubst), jq, and bash (for setup-ssl.sh)
RUN apk add --no-cache curl gettext jq bash

# Copy nginx configuration template (unified)
COPY nginx.unified.conf.template /etc/nginx/nginx.unified.conf.template

# Copy scripts
COPY entrypoint.sh /entrypoint.sh
COPY setup-ssl.sh /setup-ssl.sh
RUN chmod +x /entrypoint.sh /setup-ssl.sh

# Create directories
RUN mkdir -p /var/log/nginx /etc/nginx/ssl

# Expose ports
EXPOSE 80 443

# Use custom entrypoint
ENTRYPOINT ["/entrypoint.sh"]

# Start nginx
CMD ["nginx", "-g", "daemon off;"] 