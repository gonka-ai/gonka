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
    "/v1/models": {
      "get": {
        "summary": "List models (OpenAI/OpenRouter-compatible)",
        "description": "Returns models currently advertised by this devshard gateway. The response uses the OpenAI list envelope and includes OpenRouter-style model metadata when available.",
        "responses": {
          "200": {
            "description": "Model list",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "object": { "type": "string", "example": "list" },
                    "data": {
                      "type": "array",
                      "items": {
                        "type": "object",
                        "properties": {
                          "id": { "type": "string" },
                          "object": { "type": "string", "example": "model" },
                          "created": { "type": "integer" },
                          "owned_by": { "type": "string", "example": "gonka" },
                          "name": { "type": "string" },
                          "description": { "type": "string" },
                          "context_length": { "type": "integer" },
                          "max_completion_tokens": { "type": "integer" },
                          "architecture": { "type": "object" },
                          "pricing": { "type": "object" },
                          "top_provider": { "type": "object" },
                          "supported_parameters": {
                            "type": "array",
                            "items": { "type": "string" }
                          }
                        }
                      }
                    }
                  }
                }
              }
            }
          },
          "405": { "description": "Method not allowed" }
        }
      }
    },
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
                  "max_tokens": { "type": "integer", "default": 3072, "maximum": 4096 },
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
        "description": "Admin endpoint. Returns complete session state: config, group, all inferences with per-inference detail, host stats, revealed seeds, and warm keys.",
        "security": [{ "AdminBearerAuth": [] }],
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
        "description": "Admin endpoint. Finalizes the devshard session and returns the settlement payload for on-chain submission.",
        "security": [{ "AdminBearerAuth": [] }],
        "responses": {
          "200": { "description": "Settlement payload JSON" },
          "500": { "description": "Finalization failed" }
        }
      },
      "get": {
        "summary": "Retrieve settlement",
        "description": "Admin endpoint. Returns the settlement payload after POST /v1/finalize has succeeded. Only available in the settlement phase.",
        "security": [{ "AdminBearerAuth": [] }],
        "responses": {
          "200": { "description": "Settlement payload JSON" },
          "409": { "description": "Session not yet finalized" }
        }
      }
    },
    "/v1/admin/devshards/import": {
      "post": {
        "summary": "Import existing devshard state",
        "description": "Admin endpoint. Loads an existing devshard state database into this gateway, inactive by default, so it can be inspected, finalized, settled, or later reactivated without entering pooled /v1/chat/completions routing immediately.",
        "security": [{ "AdminBearerAuth": [] }],
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "required": ["id", "storage_path"],
                "properties": {
                  "id": { "type": "string", "description": "Devshard escrow ID." },
                  "private_key": { "type": "string", "description": "Hex-encoded private key. Prefer private_key_env for operational use." },
                  "private_key_env": { "type": "string", "description": "Environment variable containing the private key, for example DEVSHARD_PRIVATE_KEY." },
                  "model": { "type": "string", "description": "Model ID associated with the escrow." },
                  "storage_path": { "type": "string", "description": "Path to the existing escrow storage directory as seen by this gateway container." },
                  "route_prefix": { "type": "string", "description": "Optional devshard HTTP route prefix (for example /devshard/v2)." },
                  "active": { "type": "boolean", "default": false, "description": "Whether to make the imported escrow routable immediately. Defaults to false." },
                  "perf_path": { "type": "string", "description": "Optional path to a source gateway perf.db. Request accounting rows for this escrow ID are copied into the current gateway perf.db." }
                }
              }
            }
          }
        },
        "responses": {
          "200": { "description": "Devshard imported" },
          "400": { "description": "Invalid request or runtime could not be loaded" },
          "409": { "description": "Devshard already exists" },
          "500": { "description": "Persist or accounting import failed" }
        }
      }
    },
    "/v1/admin/devshards/{id}/settle": {
      "post": {
        "summary": "Settle devshard escrow",
        "description": "Admin endpoint. Locally deactivates the devshard, finalizes it if needed, signs MsgSettleDevshardEscrow, and broadcasts the settlement transaction on-chain.",
        "security": [{ "AdminBearerAuth": [] }],
        "parameters": [
          {
            "name": "id",
            "in": "path",
            "required": true,
            "schema": { "type": "string" },
            "description": "Devshard escrow ID"
          }
        ],
        "requestBody": {
          "required": false,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "properties": {
                  "private_key": { "type": "string", "description": "Hex-encoded private key. Prefer private_key_env for operational use." },
                  "private_key_env": { "type": "string", "description": "Environment variable containing the private key, for example DEVSHARD_PRIVATE_KEY." },
                  "chain_id": { "type": "string", "description": "Optional chain ID override." },
                  "fee_denom": { "type": "string", "description": "Optional fee denomination override." },
                  "fee_amount": { "type": "integer", "description": "Optional fee amount override." },
                  "gas_limit": { "type": "integer", "description": "Optional gas limit override." }
                }
              }
            }
          }
        },
        "responses": {
          "200": {
            "description": "Settlement transaction submitted",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "id": { "type": "string" },
                    "escrow_id": { "type": "integer" },
                    "active": { "type": "boolean" },
                    "tx_hash": { "type": "string" },
                    "settler": { "type": "string" }
                  }
                }
              }
            }
          },
          "400": { "description": "Invalid request or missing settlement key" },
          "404": { "description": "Devshard not found" },
          "409": { "description": "Devshard has active requests" },
          "502": { "description": "Finalize or settlement broadcast failed" }
        }
      }
    },
    "/v1/debug/pending": {
      "get": {
        "summary": "Pending transactions",
        "description": "Admin endpoint. Lists pending devshard transactions and warm keys.",
        "security": [{ "AdminBearerAuth": [] }],
        "responses": {
          "200": { "description": "Pending tx list" }
        }
      }
    },
    "/v1/debug/state": {
      "get": {
        "summary": "Debug state summary",
        "description": "Admin endpoint. Returns nonce, balance, total inferences, and status counts.",
        "security": [{ "AdminBearerAuth": [] }],
        "responses": {
          "200": { "description": "Debug state summary" }
        }
      }
    },
    "/v1/debug/rotation": {
      "get": {
        "summary": "Escrow rotation debug status",
        "description": "Admin endpoint. Returns current escrow rotation settings, chain countdown to the next rotation window, and persisted latest rotation results per model.",
        "security": [{ "AdminBearerAuth": [] }],
        "responses": {
          "200": { "description": "Escrow rotation debug status" }
        }
      }
    },
    "/v1/debug/signatures": {
      "get": {
        "summary": "Signature accumulation status",
        "description": "Admin endpoint. Returns per-nonce signature weight and the highest nonce that reached 2/3+1 quorum. Useful for monitoring finalization progress.",
        "security": [{ "AdminBearerAuth": [] }],
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
        "description": "Admin endpoint. Actively polls all hosts to collect signatures for the given nonce. Tries fetching existing signatures first (cheap GET), then falls back to sending catch-up diffs.",
        "security": [{ "AdminBearerAuth": [] }],
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
  },
  "components": {
    "securitySchemes": {
      "AdminBearerAuth": {
        "type": "http",
        "scheme": "bearer",
        "description": "Admin bearer token from DEVSHARD_ADMIN_API_KEY."
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
