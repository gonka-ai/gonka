package completionapi

import (
	"log"
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

func Test(t *testing.T) {
	r, err := ModifyRequestBody([]byte(jsonBodyNullLogprobs), 7)
	if err != nil {
		panic(err)
	}
	if r.OriginalLogprobsValue != nil {
		t.Fatalf("expected nil, got %v", r.OriginalLogprobsValue)
	}
	if r.OriginalTopLogprobsValue != nil {
		t.Fatalf("expected nil, got %v", r.OriginalTopLogprobsValue)
	}
	log.Printf(string(r.NewBody))
}
