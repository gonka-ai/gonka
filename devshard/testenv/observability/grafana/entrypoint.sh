#!/bin/sh
set -eu

mkdir -p /var/lib/grafana/dashboards
rm -rf /var/lib/grafana/dashboards/*
cp -R /var/lib/grafana/dashboards-src/. /var/lib/grafana/dashboards/

exec /run.sh
