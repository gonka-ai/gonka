#!/usr/bin/env bash
set -euo pipefail

pull_and_tag() {
  local source_image="$1"
  local target_image="$2"

  if [[ -z "${source_image}" || -z "${target_image}" ]]; then
    return 0
  fi

  echo "Pulling ${source_image}"
  docker pull "${source_image}"
  echo "Tagging ${source_image} as ${target_image}"
  docker tag "${source_image}" "${target_image}"
}

pull_and_tag "${PREVIOUS_NODE_IMAGE:-}" "${TARGET_NODE_IMAGE:-ghcr.io/product-science/inferenced}"
pull_and_tag "${PREVIOUS_API_IMAGE:-}" "${TARGET_API_IMAGE:-ghcr.io/product-science/api}"
pull_and_tag "${PREVIOUS_PROXY_IMAGE:-}" "${TARGET_PROXY_IMAGE:-ghcr.io/product-science/proxy:latest}"
pull_and_tag "${PREVIOUS_VERSIOND_IMAGE:-}" "${TARGET_VERSIOND_IMAGE:-versiond:latest}"

echo "Prepared previous release images:"
docker images | grep -E 'product-science/(inferenced|api|proxy)|versiond' || true
