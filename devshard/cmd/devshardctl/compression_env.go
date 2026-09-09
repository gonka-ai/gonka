package main

import (
	"fmt"
	"os"

	"devshard/internal/boolvalue"
)

// envGatewayCompressRequestBodies gzips request bodies on the gateway leg.
const envGatewayCompressRequestBodies = "DEVSHARD_GATEWAY_COMPRESS_REQUEST_BODIES"

// compressRequestBodiesFromEnv reports whether the gateway compresses requests.
func compressRequestBodiesFromEnv() (bool, error) {
	enabled, err := boolvalue.Parse(os.Getenv(envGatewayCompressRequestBodies))
	if err != nil {
		return false, fmt.Errorf("%s: %w", envGatewayCompressRequestBodies, err)
	}
	return enabled, nil
}
