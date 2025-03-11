package server

import (
	"decentralized-api/completionapi"
	"encoding/json"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	jsonBody = `{
        "temperature": 0.8,
        "model": "unsloth/llama-3-8b-Instruct",
        "messages": [{
            "role": "system",
            "content": "Regardless of the language of the question, answer in english"
        },
        {
            "role": "user",
            "content": "When did Hawaii become a state?"
        }]
    }`

	jsonBodyNullLogprobs = `{
        "temperature": 0.8,
        "model": "unsloth/llama-3-8b-Instruct",
        "messages": [{
            "role": "system",
            "content": "Regardless of the language of the question, answer in english"
        },
        {
            "role": "user",
            "content": "When did Hawaii become a state?"
        }],
		"logprobs": null
    }`
)

func startTestServer() *httptest.Server {
	handler := func(w http.ResponseWriter, r *http.Request) {
		requestBodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}

		modifiedRequest, err := completionapi.ModifyRequestBody(requestBodyBytes, rand.Int31())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			var requestMap map[string]interface{}
			if err := json.Unmarshal(modifiedRequest.NewBody, &requestMap); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			log.Printf("modifiedRequestBody = %v", requestMap)
			w.WriteHeader(http.StatusOK)
		}
	}

	return httptest.NewServer(http.HandlerFunc(handler))
}

func TestServer(t *testing.T) {
	server := startTestServer()
	defer server.Close()

	_, err := http.Post(server.URL, "application/json", strings.NewReader(jsonBodyNullLogprobs))
	if err != nil {
		t.Fatalf("error making request: %v", err)
	}
}

/*func TestValidate(t *testing.T) {
	const payload = `{"message": "Hello!"}`
	privKeyBytes, err := hex.DecodeString("92026dba822692f8f842a6d5a241ab6c6ef4e2e980cd7364ee206cbc3c3bf750")
	assert.NoError(t, err)

	privkey := secp256k1.PrivKey{Key: privKeyBytes}

	signature, err := privkey.Sign([]byte(payload))
	assert.NoError(t, err)

	fmt.Printf("signature = %v \n", base64.StdEncoding.EncodeToString(signature))
	fmt.Printf("signature = %v \n", hex.EncodeToString(signature))

	pubkey := privkey.PubKey()
	signature, err = base64.StdEncoding.DecodeString("rJyhH0n8haixo7hUVkY2MqDuchEihUqd5CPwAEHE5fYakLqIpY8fkEa1oVsMUoFNIYiqS/mc55lGYub8K3PFjg==")
	assert.NoError(t, err)

	assert.True(t, pubkey.VerifySignature([]byte(payload), signature))
}
*/
