package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"decentralized-api/encclient"
)

func main() {
	pubHex := flag.String("recipient-pub-hex", "",
		"X25519 recipient public key in hex (32 bytes)")
	sessionID := flag.String("session-id", "wirecheck", "envelope session_id")
	nonce := flag.Uint64("nonce", 1, "envelope nonce")
	plaintext := flag.String("plaintext", "hello-from-go", "plaintext to seal")
	flag.Parse()

	pub, err := hex.DecodeString(*pubHex)
	if err != nil || len(pub) != encclient.X25519PublicKeySize {
		fmt.Fprintln(os.Stderr, "wirecheck: invalid -recipient-pub-hex")
		os.Exit(2)
	}

	env, respKey, err := encclient.SealRequest(pub, *sessionID, *nonce, []byte(*plaintext))
	if err != nil {
		fmt.Fprintln(os.Stderr, "wirecheck: seal:", err)
		os.Exit(1)
	}
	out := map[string]any{
		"envelope":         env,
		"response_key_hex": hex.EncodeToString(respKey),
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "wirecheck: encode:", err)
		os.Exit(1)
	}
}
