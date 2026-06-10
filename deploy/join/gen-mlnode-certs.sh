#!/usr/bin/env bash
set -euo pipefail

CERTS_DIR="${CERTS_DIR:-$(dirname "$0")/mtls-certs}"
DAPI_SANS="${DAPI_SANS:-DNS:api,DNS:localhost,IP:127.0.0.1}"
MLNODE_SANS="${MLNODE_SANS:-DNS:inference,DNS:localhost,IP:127.0.0.1}"
DAYS="${DAYS:-36500}"

gen_pair() {
    local name="$1" sans="$2"
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
        -days "$DAYS" \
        -keyout "$CERTS_DIR/$name.key" \
        -out "$CERTS_DIR/$name.crt" \
        -subj "/CN=gonka-$name" \
        -addext "subjectAltName=$sans" \
        -addext "basicConstraints=critical,CA:TRUE" \
        -addext "keyUsage=critical,digitalSignature,keyCertSign" \
        -addext "extendedKeyUsage=serverAuth,clientAuth" \
        2>/dev/null
    chmod 600 "$CERTS_DIR/$name.key"
    chmod 644 "$CERTS_DIR/$name.crt"
}

main() {
    if [ -f "$CERTS_DIR/dapi.crt" ] || [ -f "$CERTS_DIR/mlnode.crt" ]; then
        echo "Certificates already exist in $CERTS_DIR."
        echo "Remove the directory first if you want to rotate them:"
        echo "  rm -rf $CERTS_DIR && $0"
        exit 1
    fi

    mkdir -p "$CERTS_DIR"

    echo "Generating DAPI certificate   (SANs: $DAPI_SANS)"
    gen_pair dapi "$DAPI_SANS"

    echo "Generating MLNode certificate (SANs: $MLNODE_SANS)"
    gen_pair mlnode "$MLNODE_SANS"

    echo
    echo "Done. Files in $CERTS_DIR:"
    ls -l "$CERTS_DIR"
    echo
    echo "Next step: start the stack with the mTLS override, e.g."
    echo "  docker compose -f docker-compose.yml -f docker-compose.mlnode.yml -f docker-compose.mtls.yml up -d"
    echo "See MTLS.md for details and verification steps."
}

main "$@"
