#!/bin/sh
# Start Grafana in background
/run.sh &
GRAFANA_PID=$!

# Wait for Grafana to be ready
echo "Waiting for Grafana to start..."
until wget -qO /dev/null http://localhost:3000/api/health 2>/dev/null; do
    sleep 1
done
echo "Grafana is ready, pushing dashboards..."

PASS="${GF_SECURITY_ADMIN_PASSWORD:-admin}"

for f in /var/lib/grafana/dashboards-src/*.json; do
    name=$(basename "$f" .json)
    dashboard=$(cat "$f")
    payload="{\"dashboard\": $dashboard, \"overwrite\": true}"
    echo "$payload" | wget -qO- --post-data="$(cat -)" \
        --header='Content-Type: application/json' \
        "http://admin:${PASS}@localhost:3000/api/dashboards/db" > /dev/null 2>&1 && echo "$name: ok" || echo "$name: failed"
done

echo "Dashboard setup complete"

# Wait for Grafana process
wait $GRAFANA_PID
