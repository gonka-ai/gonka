package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"decentralized-api/teeverify"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <quote.bin> <pubkey.bin>\n", os.Args[0])
		os.Exit(2)
	}
	quote, err := os.ReadFile(os.Args[1])
	if err != nil {
		fail("read quote: %v", err)
	}
	pubkey, err := os.ReadFile(os.Args[2])
	if err != nil {
		fail("read pubkey: %v", err)
	}
	fmt.Printf("input quote  : %d bytes\n", len(quote))
	fmt.Printf("input pubkey : %d bytes (%s)\n", len(pubkey), hex.EncodeToString(pubkey))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	v := teeverify.IntelTDXLiteVerifier{}
	rep, vErr := v.Verify(ctx, teeverify.VerifyRequest{
		PublicKey:       pubkey,
		Quote:           quote,
		EnvelopeVersion: "v1",
	})
	if vErr != nil {
		fail("Verify: %v", vErr)
	}

	qh := sha256.Sum256(quote)
	fmt.Println("verify       : OK")
	fmt.Printf("provider     : %s\n", v.Provider())
	fmt.Printf("measurement  : %s\n", rep.Measurement)
	fmt.Printf("tcb_status   : %s\n", rep.TcbStatus)
	fmt.Printf("quote_hash   : %s\n", hex.EncodeToString(rep.QuoteHash))
	fmt.Printf("sha256(quote): %s\n", hex.EncodeToString(qh[:]))
	if rep.Measurement == "" {
		fail("verifier returned empty measurement")
	}
	if hex.EncodeToString(rep.QuoteHash) != hex.EncodeToString(qh[:]) {
		fail("verifier QuoteHash does not match sha256(quote)")
	}
	fmt.Println("\nNext step (sanity): compare `measurement` above against the value")
	fmt.Println("printed by the Python smoke (ident.measurements[\"mrtd\"]).")
	fmt.Println("They MUST be byte-equal lowercase hex.")
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
	os.Exit(1)
}
