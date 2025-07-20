#!/bin/sh

# Set default values for environment variables if not provided

export PROXY_ADD_NODE_PREFIX=${PROXY_ADD_NODE_PREFIX:-false}
export API_PORT=${API_PORT:-9000}
export CHAIN_RPC_PORT=${CHAIN_RPC_PORT:-26657}
export CHAIN_API_PORT=${CHAIN_API_PORT:-1317}
export CHAIN_GRPC_PORT=${CHAIN_GRPC_PORT:-9090}

# Service names - configurable for Docker vs Kubernetes
export API_SERVICE_NAME=${API_SERVICE_NAME:-api}
export NODE_SERVICE_NAME=${NODE_SERVICE_NAME:-node}
export EXPLORER_SERVICE_NAME=${EXPLORER_SERVICE_NAME:-explorer}

# Set KEY_NAME_PREFIX based on PROXY_ADD_NODE_PREFIX flag and KEY_NAME being set
if [ "${PROXY_ADD_NODE_PREFIX}" = "true" ] && [ -n "${KEY_NAME}" ] && [ "${KEY_NAME}" != "" ]; then
    export KEY_NAME_PREFIX="${KEY_NAME}-"
else
    export KEY_NAME_PREFIX=""
fi

# Set final service names
export FINAL_API_SERVICE="${KEY_NAME_PREFIX}${API_SERVICE_NAME}"
export FINAL_NODE_SERVICE="${KEY_NAME_PREFIX}${NODE_SERVICE_NAME}"
export FINAL_EXPLORER_SERVICE="${KEY_NAME_PREFIX}${EXPLORER_SERVICE_NAME}"

# Check if dashboard is enabled
DASHBOARD_ENABLED="false"
if [ -n "${DASHBOARD_PORT}" ] && [ "${DASHBOARD_PORT}" != "" ]; then
    DASHBOARD_ENABLED="true"
    export DASHBOARD_PORT=${DASHBOARD_PORT}
fi

# Log the configuration being used
echo "🔧 Nginx Proxy Configuration:"
echo "   KEY_NAME: $KEY_NAME"
echo "   PROXY_ADD_NODE_PREFIX: $PROXY_ADD_NODE_PREFIX"
echo "   API Service: $FINAL_API_SERVICE:$API_PORT"
echo "   Node Service: $FINAL_NODE_SERVICE (API:$CHAIN_API_PORT, RPC:$CHAIN_RPC_PORT, gRPC:$CHAIN_GRPC_PORT)"
echo "   Explorer Service: $FINAL_EXPLORER_SERVICE:$DASHBOARD_PORT"

if [ "$DASHBOARD_ENABLED" = "true" ]; then
    echo "   DASHBOARD_PORT: $DASHBOARD_PORT (enabled)"
    echo "🖥️  Dashboard: Enabled - root path will proxy to explorer"
    
    # Set up dashboard upstream and root location for enabled dashboard
    export DASHBOARD_UPSTREAM="upstream dashboard_backend {
        server ${FINAL_EXPLORER_SERVICE}:${DASHBOARD_PORT};
    }"
    
    export ROOT_LOCATION="location / {
            proxy_pass http://dashboard_backend/;
            proxy_set_header Host \$\$host;
            proxy_set_header X-Real-IP \$\$remote_addr;
            proxy_set_header X-Forwarded-For \$\$proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto \$\$scheme;

            # WebSocket support for hot reloading
            proxy_http_version 1.1;
            proxy_set_header Upgrade \$\$http_upgrade;
            proxy_set_header Connection \"upgrade\";
        }"
else
    echo "   DASHBOARD_PORT: not set (disabled)"
    echo "🚫 Dashboard: Disabled - root path will show 'not available' page"
    
    # No dashboard upstream needed
    export DASHBOARD_UPSTREAM="# Dashboard not configured"
    
    # Set up root location for disabled dashboard
    export ROOT_LOCATION="location / {
            return 200 '<!DOCTYPE html>
<html>
<head>
    <title>Dashboard Not Configured</title>
    <style>
        body { font-family: Arial, sans-serif; text-align: center; padding: 50px; background: #f5f5f5; }
        .container { max-width: 600px; margin: 0 auto; background: white; padding: 40px; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }
        h1 { color: #e74c3c; margin-bottom: 20px; }
        p { color: #666; line-height: 1.6; margin-bottom: 15px; }
        .code { background: #f8f9fa; padding: 2px 6px; border-radius: 3px; font-family: monospace; }
        .endpoint-list { text-align: left; display: inline-block; margin: 20px 0; }
        .endpoint-list li { margin: 8px 0; }
    </style>
</head>
<body>
    <div class=\"container\">
        <h1>Dashboard Not Configured</h1>
        <p>The blockchain explorer dashboard is not enabled for this deployment.</p>
        <p>You can access the following endpoints:</p>
        <ul class=\"endpoint-list\">
            <li>API endpoints: <span class=\"code\">/api/*</span></li>
            <li>Chain RPC: <span class=\"code\">/chain-rpc/*</span></li>
            <li>Chain REST API: <span class=\"code\">/chain-api/*</span></li>
            <li>Chain gRPC: <span class=\"code\">/chain-grpc/*</span></li>
            <li>Health check: <span class=\"code\">/health</span></li>
        </ul>
        <p>To enable the dashboard, set the <span class=\"code\">DASHBOARD_PORT</span> environment variable and include the explorer service in your deployment.</p>
    </div>
</body>
</html>';
            add_header Content-Type text/html;
        }"
fi

# Generate nginx configuration from template
envsubst '$KEY_NAME,$KEY_NAME_PREFIX,$API_PORT,$CHAIN_RPC_PORT,$CHAIN_API_PORT,$CHAIN_GRPC_PORT,$DASHBOARD_PORT,$DASHBOARD_UPSTREAM,$ROOT_LOCATION,$FINAL_API_SERVICE,$FINAL_NODE_SERVICE,$FINAL_EXPLORER_SERVICE' < /etc/nginx/nginx.conf.template | sed 's/\$\$/$/g' > /etc/nginx/nginx.conf

# Validate nginx configuration
nginx -t

if [ $? -eq 0 ]; then
    echo "✅ Nginx configuration is valid"
    echo "🌐 Available endpoints:"
    if [ "$DASHBOARD_ENABLED" = "true" ]; then
        echo "   / (root)       -> Explorer dashboard"
    else
        echo "   / (root)       -> Dashboard not configured page"
    fi
    echo "   /api/*         -> API backend"
    echo "   /chain-rpc/*   -> Chain RPC"
    echo "   /chain-api/*   -> Chain REST API"
    echo "   /chain-grpc/*  -> Chain gRPC"
    echo "   /health        -> Health check"
else
    echo "❌ Nginx configuration is invalid"
    exit 1
fi

# Execute the command passed to the container
exec "$@" 