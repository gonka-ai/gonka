package main

import "net/http"

const openapiSpec = `{
  "openapi": "3.0.3",
  "info": {
    "title": "Devshard Proxy API",
    "description": "OpenAI-compatible proxy backed by a Gonka devshard session.",
    "version": "0.1.0"
  },
  "paths": {
    "/v1/chat/completions": {
      "post": {
        "summary": "Chat completion (OpenAI-compatible)",
        "description": "Sends a chat completion request through the devshard. Supports streaming via SSE.",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "properties": {
                  "model": { "type": "string", "description": "Model name. Falls back to server default." },
                  "stream": { "type": "boolean", "default": false },
                  "max_tokens": { "type": "integer", "default": 2048 },
                  "messages": {
                    "type": "array",
                    "items": {
                      "type": "object",
                      "properties": {
                        "role": { "type": "string" },
                        "content": { "type": "string" }
                      }
                    }
                  }
                }
              }
            }
          }
        },
        "responses": {
          "200": { "description": "Completion response (JSON or SSE stream)" },
          "502": { "description": "Inference failed" }
        }
      }
    },
    "/v1/status": {
      "get": {
        "summary": "Session status",
        "description": "Returns escrow ID, current nonce, phase, and balance.",
        "responses": {
          "200": {
            "description": "Status",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "escrow_id": { "type": "string" },
                    "nonce": { "type": "integer" },
                    "phase": { "type": "string", "enum": ["active", "finalizing", "settlement"] },
                    "balance": { "type": "integer" }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/v1/state": {
      "get": {
        "summary": "Full session state",
        "description": "Returns complete session state: config, group, all inferences with per-inference detail, host stats, revealed seeds, and warm keys.",
        "responses": {
          "200": {
            "description": "Full state snapshot",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "session": {
                      "type": "object",
                      "properties": {
                        "escrow_id": { "type": "string" },
                        "phase": { "type": "string" },
                        "balance": { "type": "integer" },
                        "latest_nonce": { "type": "integer" },
                        "finalize_nonce": { "type": "integer" }
                      }
                    },
                    "group": {
                      "type": "array",
                      "items": {
                        "type": "object",
                        "properties": {
                          "slot_id": { "type": "integer" },
                          "validator_address": { "type": "string" }
                        }
                      }
                    },
                    "inferences": { "type": "object" },
                    "host_stats": { "type": "object" },
                    "revealed_seeds": { "type": "object" },
                    "warm_keys": { "type": "object" }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/v1/finalize": {
      "post": {
        "summary": "Finalize session",
        "description": "Finalizes the devshard session and returns the settlement payload for on-chain submission.",
        "responses": {
          "200": { "description": "Settlement payload JSON" },
          "500": { "description": "Finalization failed" }
        }
      },
      "get": {
        "summary": "Retrieve settlement",
        "description": "Returns the settlement payload after POST /v1/finalize has succeeded. Only available in the settlement phase.",
        "responses": {
          "200": { "description": "Settlement payload JSON" },
          "409": { "description": "Session not yet finalized" }
        }
      }
    },
    "/v1/debug/pending": {
      "get": {
        "summary": "Pending transactions",
        "description": "Lists pending devshard transactions and warm keys.",
        "responses": {
          "200": { "description": "Pending tx list" }
        }
      }
    },
    "/v1/debug/state": {
      "get": {
        "summary": "Debug state summary",
        "description": "Returns nonce, balance, total inferences, and status counts.",
        "responses": {
          "200": { "description": "Debug state summary" }
        }
      }
    },
    "/v1/debug/signatures": {
      "get": {
        "summary": "Signature accumulation status",
        "description": "Returns per-nonce signature weight and the highest nonce that reached 2/3+1 quorum. Useful for monitoring finalization progress.",
        "responses": {
          "200": {
            "description": "Signature status",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "current_nonce": { "type": "integer" },
                    "total_slots": { "type": "integer" },
                    "quorum_threshold": { "type": "integer", "description": "2*total_slots/3 + 1" },
                    "highest_quorum_nonce": { "type": "integer", "description": "Highest nonce with >= quorum_threshold signatures" },
                    "has_quorum": { "type": "boolean", "description": "Whether any nonce has reached quorum" },
                    "nonces": {
                      "type": "array",
                      "items": {
                        "type": "object",
                        "properties": {
                          "nonce": { "type": "integer" },
                          "sig_weight": { "type": "integer", "description": "Slot-weighted signature count" },
                          "total_slots": { "type": "integer" },
                          "has_quorum": { "type": "boolean" }
                        }
                      }
                    }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/v1/debug/signatures/collect": {
      "post": {
        "summary": "Collect signatures at nonce",
        "description": "Actively polls all hosts to collect signatures for the given nonce. Tries fetching existing signatures first (cheap GET), then falls back to sending catch-up diffs.",
        "parameters": [
          {
            "name": "nonce",
            "in": "query",
            "required": true,
            "schema": { "type": "integer" },
            "description": "The nonce to collect signatures for"
          }
        ],
        "responses": {
          "200": {
            "description": "Signature collection result",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "nonce": { "type": "integer" },
                    "sig_weight": { "type": "integer", "description": "Slot-weighted signature count" },
                    "quorum_threshold": { "type": "integer" },
                    "total_slots": { "type": "integer" },
                    "has_quorum": { "type": "boolean" }
                  }
                }
              }
            }
          },
          "400": { "description": "Missing or invalid nonce, or nonce ahead of current state" }
        }
      }
    }
  }
}`

const swaggerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Devshard Proxy API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({ url: "/openapi.json", dom_id: "#swagger-ui" });
  </script>
</body>
</html>`

func (p *Proxy) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(swaggerHTML))
}

func (p *Proxy) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(openapiSpec))
}
