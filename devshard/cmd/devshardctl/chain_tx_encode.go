package main

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"devshard/signing"
)

func txSettingDurationMS(raw string, fallback time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func normalizePrivateKeyHex(key string) string {
	return strings.TrimPrefix(strings.TrimSpace(key), "0x")
}

func signerFromRequestKey(privateKey, privateKeyEnv string) (*signing.Secp256k1Signer, string, error) {
	keyHex := normalizePrivateKeyHex(privateKey)
	envName := strings.TrimSpace(privateKeyEnv)
	if keyHex == "" && envName != "" {
		keyHex = normalizePrivateKeyHex(os.Getenv(envName))
	}
	if keyHex == "" {
		return nil, "", errors.New("private_key or private_key_env is required")
	}
	signer, err := signing.SignerFromHex(keyHex)
	if err != nil {
		return nil, "", err
	}
	return signer, keyHex, nil
}
