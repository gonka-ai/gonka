package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServer(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		modifiedRequest, err := modifyRequest(r)
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

	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

	jsonBody := `{
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
	_, err := http.Post(server.URL, "application/json", strings.NewReader(jsonBody))
	if err != nil {
		t.Fatalf("error making request: %v", err)
	}
}
