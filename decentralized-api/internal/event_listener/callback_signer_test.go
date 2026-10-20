package event_listener

import (
	"bytes"

	"decentralized-api/internal/callbackauth"
)

var testCallbackSigner = mustTestCallbackSigner()

func mustTestCallbackSigner() *callbackauth.Signer {
	signer, err := callbackauth.NewSigner(bytes.Repeat([]byte{7}, callbackauth.SecretSize))
	if err != nil {
		panic(err)
	}
	return signer
}
