package completionapi

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

const (
	NO_LOGPROBS = `
{
  "id": "cmpl-2d320c7a47f24b02b48a376b979a9242",
  "object": "chat.completion",
  "created": 1722197559,
  "model": "Qwen/Qwen2.5-7B-Instruct",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "August 21, 1959",
        "tool_calls": []
      },
      "logprobs": null,
      "finish_reason": "stop",
      "stop_reason": null
    }
  ],
  "usage": {
    "prompt_tokens": 47,
    "total_tokens": 55,
    "completion_tokens": 8
  }
}`
	LOGPROBS_NO_TOP_LOGPROBS = `
{
  "id": "cmpl-c5298389c6d34738b1ecb8a557ddfba3",
  "object": "chat.completion",
  "created": 1722197602,
  "model": "Qwen/Qwen2.5-7B-Instruct",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "August 21, 1959",
        "tool_calls": []
      },
      "logprobs": {
        "content": [
          {
            "token": "August",
            "logprob": -2.8729025871143676e-05,
            "bytes": [
              65,
              117,
              103,
              117,
              115,
              116
            ],
            "top_logprobs": []
          },
          {
            "token": " ",
            "logprob": -2.1457441107486375e-05,
            "bytes": [
              32
            ],
            "top_logprobs": []
          },
          {
            "token": "21",
            "logprob": -0.0001250427303602919,
            "bytes": [
              50,
              49
            ],
            "top_logprobs": []
          },
          {
            "token": ",",
            "logprob": -0.0024951535742729902,
            "bytes": [
              44
            ],
            "top_logprobs": []
          },
          {
            "token": " ",
            "logprob": -3.8742269680369645e-05,
            "bytes": [
              32
            ],
            "top_logprobs": []
          },
          {
            "token": "195",
            "logprob": -1.1920928244535389e-07,
            "bytes": [
              49,
              57,
              53
            ],
            "top_logprobs": []
          },
          {
            "token": "9",
            "logprob": 0.0,
            "bytes": [
              57
            ],
            "top_logprobs": []
          },
          {
            "token": "",
            "logprob": -0.6931514739990234,
            "bytes": [],
            "top_logprobs": []
          }
        ]
      },
      "finish_reason": "stop",
      "stop_reason": null
    }
  ],
  "usage": {
    "prompt_tokens": 47,
    "total_tokens": 55,
    "completion_tokens": 8
  }
}
`
	TOP_LOGPROBS = `
{
  "id": "cmpl-7804309bf8404215aefc67fd9275d1ff",
  "object": "chat.completion",
  "created": 1722197678,
  "model": "Qwen/Qwen2.5-7B-Instruct",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "August 21, 1959",
        "tool_calls": []
      },
      "logprobs": {
        "content": [
          {
            "token": "August",
            "logprob": -2.8729025871143676e-05,
            "bytes": [
              65,
              117,
              103,
              117,
              115,
              116
            ],
            "top_logprobs": [
              {
                "token": "August",
                "logprob": -2.8729025871143676e-05,
                "bytes": [
                  65,
                  117,
                  103,
                  117,
                  115,
                  116
                ]
              },
              {
                "token": "H",
                "logprob": -11.875028610229492,
                "bytes": [
                  72
                ]
              },
              {
                "token": "195",
                "logprob": -12.125028610229492,
                "bytes": [
                  49,
                  57,
                  53
                ]
              }
            ]
          },
          {
            "token": " ",
            "logprob": -2.1457441107486375e-05,
            "bytes": [
              32
            ],
            "top_logprobs": [
              {
                "token": " ",
                "logprob": -2.1457441107486375e-05,
                "bytes": [
                  32
                ]
              },
              {
                "token": " Fifty",
                "logprob": -11.750020980834961,
                "bytes": [
                  32,
                  70,
                  105,
                  102,
                  116,
                  121
                ]
              },
              {
                "token": " Nin",
                "logprob": -12.500020980834961,
                "bytes": [
                  32,
                  78,
                  105,
                  110
                ]
              }
            ]
          },
          {
            "token": "21",
            "logprob": -0.0001250427303602919,
            "bytes": [
              50,
              49
            ],
            "top_logprobs": [
              {
                "token": "21",
                "logprob": -0.0001250427303602919,
                "bytes": [
                  50,
                  49
                ]
              },
              {
                "token": "195",
                "logprob": -9.12512493133545,
                "bytes": [
                  49,
                  57,
                  53
                ]
              },
              {
                "token": "196",
                "logprob": -11.25012493133545,
                "bytes": [
                  49,
                  57,
                  54
                ]
              }
            ]
          },
          {
            "token": ",",
            "logprob": -0.0024951535742729902,
            "bytes": [
              44
            ],
            "top_logprobs": [
              {
                "token": ",",
                "logprob": -0.0024951535742729902,
                "bytes": [
                  44
                ]
              },
              {
                "token": " ",
                "logprob": -6.627495288848877,
                "bytes": [
                  32
                ]
              },
              {
                "token": "st",
                "logprob": -6.752495288848877,
                "bytes": [
                  115,
                  116
                ]
              }
            ]
          },
          {
            "token": " ",
            "logprob": -3.8742269680369645e-05,
            "bytes": [
              32
            ],
            "top_logprobs": [
              {
                "token": " ",
                "logprob": -3.8742269680369645e-05,
                "bytes": [
                  32
                ]
              },
              {
                "token": "195",
                "logprob": -10.250039100646973,
                "bytes": [
                  49,
                  57,
                  53
                ]
              },
              {
                "token": "196",
                "logprob": -12.625039100646973,
                "bytes": [
                  49,
                  57,
                  54
                ]
              }
            ]
          },
          {
            "token": "195",
            "logprob": -1.1920928244535389e-07,
            "bytes": [
              49,
              57,
              53
            ],
            "top_logprobs": [
              {
                "token": "195",
                "logprob": -1.1920928244535389e-07,
                "bytes": [
                  49,
                  57,
                  53
                ]
              },
              {
                "token": "196",
                "logprob": -16.25,
                "bytes": [
                  49,
                  57,
                  54
                ]
              },
              {
                "token": "59",
                "logprob": -21.625,
                "bytes": [
                  53,
                  57
                ]
              }
            ]
          },
          {
            "token": "9",
            "logprob": 0.0,
            "bytes": [
              57
            ],
            "top_logprobs": [
              {
                "token": "9",
                "logprob": 0.0,
                "bytes": [
                  57
                ]
              },
              {
                "token": "8",
                "logprob": -17.5,
                "bytes": [
                  56
                ]
              },
              {
                "token": "0",
                "logprob": -19.125,
                "bytes": [
                  48
                ]
              }
            ]
          },
          {
            "token": "",
            "logprob": -0.6931514739990234,
            "bytes": [],
            "top_logprobs": [
              {
                "token": "",
                "logprob": -0.6931514739990234,
                "bytes": []
              },
              {
                "token": ".",
                "logprob": -0.6931514739990234,
                "bytes": [
                  46
                ]
              },
              {
                "token": "!",
                "logprob": -12.443151473999023,
                "bytes": [
                  33
                ]
              }
            ]
          }
        ]
      },
      "finish_reason": "stop",
      "stop_reason": null
    }
  ],
  "usage": {
    "prompt_tokens": 47,
    "total_tokens": 55,
    "completion_tokens": 8
  }
}`
	START_COMPLETION_STREAMED_EVENT = `
{
  "id": "cmpl-3973dab1430143849df83d943ea0c7ac",
  "object": "chat.completion.chunk",
  "created": 1726472629,
  "model": "Qwen/Qwen2.5-7B-Instruct",
  "choices": [
    {
      "index": 0,
      "delta": {
        "role": "assistant"
      },
      "logprobs": null,
      "finish_reason": null
	}
  ]
}
`
	MID_COMPLETION_STREAMED_EVENT = `
{
  "id": "cmpl-3973dab1430143849df83d943ea0c7ac",
  "object": "chat.completion.chunk",
  "created": 1726472629,
  "model": "Qwen/Qwen2.5-7B-Instruct",
  "choices": [
    {
      "index": 0,
      "delta": {
        "content": "."
      },
      "logprobs": {
        "content": [
          {
            "token": ".",
            "logprob": -0.04858766868710518,
            "bytes": [
              46
            ],
            "top_logprobs": [
              {
                "token": ".",
                "logprob": -0.04858766868710518,
                "bytes": [
                  46
                ]
              },
              {
                "token": ",",
                "logprob": -3.0485875606536865,
                "bytes": [
                  44
                ]
              },
              {
                "token": " when",
                "logprob": -15.673587799072266,
                "bytes": [
                  32,
                  119,
                  104,
                  101,
                  110
                ]
              }
            ]
          }
        ]
      },
      "finish_reason": null
    }
  ]
}
`
	END_COMPLETION_STREAMED_EVENT = `
{
  "id": "cmpl-3973dab1430143849df83d943ea0c7ac",
  "object": "chat.completion.chunk",
  "created": 1726472629,
  "model": "Qwen/Qwen2.5-7B-Instruct",
  "choices": [
    {
      "index": 0,
      "delta": {
        "content": ""
      },
      "logprobs": {
        "content": [
          {
            "token": "",
            "logprob": -2.264974000354414e-6,
            "bytes": [],
            "top_logprobs": [
              {
                "token": "",
                "logprob": -2.264974000354414e-6,
                "bytes": []
              },
              {
                "token": " It",
                "logprob": -13.000001907348633,
                "bytes": [
                  32,
                  73,
                  116
                ]
              },
              {
                "token": "",
                "logprob": -19.000001907348633,
                "bytes": []
              }
            ]
          }
        ]
      },
      "finish_reason": "stop",
      "stop_reason": null
    }
  ]
}
`
)

func TestNoLogprobsAnswer(t *testing.T) {
	var r Response
	if err := json.Unmarshal([]byte(NO_LOGPROBS), &r); err != nil {
		t.Fatalf("Failed to unmarshal r: %v", err)
	}
	t.Logf("Response: %v", r)

	if len(r.Choices) != 1 {
		t.Fatalf("Expected 1 choice, got %d", len(r.Choices))
	}

	if r.Choices[0].Logprobs.Content != nil {
		t.Fatalf("Expected nil logprobs, got %v", r.Choices[0].Logprobs.Content)
	}

	if r.Choices[0].StopReason != "" {
		t.Fatalf("Expected stop reason to be nil got %s", r.Choices[0].StopReason)
	}
}

func TestLogprobsNoTopLogprobsAnswer(t *testing.T) {
	var r Response
	if err := json.Unmarshal([]byte(LOGPROBS_NO_TOP_LOGPROBS), &r); err != nil {
		t.Fatalf("Failed to unmarshal r: %v", err)
	}
	t.Logf("Response: %v", r)
}

func TestTopLogprobsAnswer(t *testing.T) {
	var r Response
	if err := json.Unmarshal([]byte(TOP_LOGPROBS), &r); err != nil {
		t.Fatalf("Failed to unmarshal r: %v", err)
	}
	t.Logf("Response: %v", r)
}

func TestStartCompletionStreamedEvent(t *testing.T) {
	var r Response
	if err := json.Unmarshal([]byte(START_COMPLETION_STREAMED_EVENT), &r); err != nil {
		t.Fatalf("Failed to unmarshal r: %v", err)
	}
	t.Logf("Response: %v", r)

	delta := assertStreamedEventChoices(t, r)

	if *delta.Role != "assistant" {
		t.Fatalf("Expected role to be assistant, got %s", *delta.Role)
	}
}

func TestMidCompletionStreamedEvent(t *testing.T) {
	var r Response
	if err := json.Unmarshal([]byte(MID_COMPLETION_STREAMED_EVENT), &r); err != nil {
		t.Fatalf("Failed to unmarshal r: %v", err)
	}
	t.Logf("Response: %v", r)

	delta := assertStreamedEventChoices(t, r)

	if delta.Content == nil {
		t.Fatalf("Expected content to be non-empty, got empty")
	}
}

func TestEndCompletionStreamedEvent(t *testing.T) {
	var r Response
	if err := json.Unmarshal([]byte(END_COMPLETION_STREAMED_EVENT), &r); err != nil {
		t.Fatalf("Failed to unmarshal r: %v", err)
	}
	t.Logf("Response: %v", r)

	delta := assertStreamedEventChoices(t, r)

	if delta.Content == nil {
		t.Fatalf("Expected content to be non-empty, got empty")
	}
}

func assertStreamedEventChoices(t *testing.T, r Response) *Delta {
	if len(r.Choices) != 1 {
		t.Fatalf("Expected 1 choices, got %d", len(r.Choices))
	}

	if r.Choices[0].Delta == nil {
		t.Fatalf("Expected delta to be non-nil, got nil")
	}

	return r.Choices[0].Delta
}

func TestStreamedResponseSerialization(t *testing.T) {
	lines := readLines(t, "test_data/response_streamed.txt")
	require.NotEmpty(t, lines, "Read 0 events from responseprocessor_test_data.txt")

	resp, err := NewCompletionResponseFromLines(lines)
	require.NoError(t, err)
	require.IsType(t, &StreamedCompletionResponse{}, resp)

	bytes, err := resp.GetBodyBytes()
	bytesString := string(bytes)

	resp2, err := NewCompletionResponseFromLinesFromResponsePayload(bytesString)
	require.NoError(t, err)
	require.IsType(t, &StreamedCompletionResponse{}, resp2)

	require.Equal(t, len(resp.(*StreamedCompletionResponse).Lines), len(resp2.(*StreamedCompletionResponse).Lines))
	require.Equal(t, len(resp.(*StreamedCompletionResponse).Resp.Data), len(resp2.(*StreamedCompletionResponse).Resp.Data))
}
