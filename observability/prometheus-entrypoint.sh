#!/bin/sh
NODE="${CHAIN_NODE:-node}"
sed "s/__NODE_HOST__/${NODE}/g" /etc/prometheus/prometheus.template.yml > /tmp/prometheus.yml
exec prometheus --config.file=/tmp/prometheus.yml --storage.tsdb.path=/prometheus --storage.tsdb.retention.time="${PROMETHEUS_RETENTION:-30d}"
