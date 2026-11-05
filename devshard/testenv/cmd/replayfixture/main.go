// Binary replayfixture turns recorded vLLM answers into replay JSONL for the mock ML node.
//
// Each input is one JSON recording of a chat request made by running vLLM's chat serving
// code on CPU: "request" (the body the ML node got), "stream" (the SSE data payloads in
// order, without [DONE]), "response_nonstream" (the same answer served without streaming),
// "served" (the concatenated stream deltas), "case", "build" and, when present,
// "notes.request.client_request" (the body the client sent before the gateway and the
// executor rewrote it into "request"), and "host.rendered_input", "host.rendered_output"
// and "gateway_bytes.total", which become the sample's render counts when both rendered
// counts are present. Every other key is dropped,
// and of "build" only the model, revision, variant and vLLM commit are kept; -variant names
// the build for recordings that carry no variant. The request and
// the recorded payloads are written compact but otherwise byte for byte: key order, number
// spelling and string escapes stay as recorded. An -out name ending in .gz is written
// gzip-compressed.
//
//	go run ./testenv/cmd/replayfixture -variant "mlnode-3.1.0 (vLLM fork 71ee49c4d)" \
//	  -out testenv/mockopenai/testdata/vllm_recorded_mlnode310.jsonl.gz rec1.json rec2.json
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"devshard/testenv/replay"
)

func main() {
	outPath := flag.String("out", "", "output JSONL file (default: stdout)")
	variant := flag.String("variant", "", "build variant for recordings that name none")
	flag.Parse()
	if flag.NArg() == 0 {
		log.Fatal("provide one or more recording files")
	}
	if *outPath == "" {
		buffered := bufio.NewWriter(os.Stdout)
		if err := convertFiles(buffered, flag.Args(), *variant); err != nil {
			log.Fatal(err)
		}
		if err := buffered.Flush(); err != nil {
			log.Fatalf("write output: %v", err)
		}
		return
	}
	if err := writeFile(*outPath, flag.Args(), *variant); err != nil {
		log.Fatal(err)
	}
}

// writeFile converts the recordings into outPath, gzip-compressed when the name ends in .gz.
func writeFile(outPath string, paths []string, variant string) error {
	file, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer file.Close()
	out := io.Writer(file)
	var compressor *gzip.Writer
	if strings.HasSuffix(outPath, ".gz") {
		compressor, err = gzip.NewWriterLevel(file, gzip.BestCompression)
		if err != nil {
			return err
		}
		out = compressor
	}
	buffered := bufio.NewWriter(out)
	if err := convertFiles(buffered, paths, variant); err != nil {
		return err
	}
	if err := buffered.Flush(); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if compressor != nil {
		if err := compressor.Close(); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
	}
	return file.Close()
}

func convertFiles(out io.Writer, paths []string, variant string) error {
	encoder := json.NewEncoder(out)
	// Recorded strings keep <, > and & as the ML node sent them.
	encoder.SetEscapeHTML(false)
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sample, err := convert(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if sample.Build.Variant == "" {
			sample.Build.Variant = variant
		}
		if err := encoder.Encode(sample); err != nil {
			return fmt.Errorf("%s: encode: %w", path, err)
		}
	}
	return nil
}

type recording struct {
	Case              string            `json:"case"`
	Request           json.RawMessage   `json:"request"`
	Stream            []json.RawMessage `json:"stream"`
	ResponseNonStream json.RawMessage   `json:"response_nonstream"`
	Served            struct {
		Content string `json:"content"`
	} `json:"served"`
	Host struct {
		RenderedInput  *uint64 `json:"rendered_input"`
		RenderedOutput *uint64 `json:"rendered_output"`
	} `json:"host"`
	GatewayBytes struct {
		Total uint64 `json:"total"`
	} `json:"gateway_bytes"`
	Notes struct {
		Request struct {
			ClientRequest json.RawMessage `json:"client_request"`
		} `json:"request"`
	} `json:"notes"`
	Build struct {
		Model       string       `json:"model"`
		Revision    string       `json:"revision"`
		Variant     string       `json:"variant"`
		VLLMVersion string       `json:"vllm_version"`
		VLLMOverlay *vllmOverlay `json:"vllm_overlay"`
	} `json:"build"`
}

type vllmOverlay struct {
	SHA string `json:"sha"`
}

type recordedRequest struct {
	Model               string            `json:"model"`
	Stream              bool              `json:"stream"`
	MaxTokens           *int64            `json:"max_tokens"`
	MaxCompletionTokens *int64            `json:"max_completion_tokens"`
	Messages            []json.RawMessage `json:"messages"`
}

func convert(raw []byte) (replay.Sample, error) {
	var rec recording
	if err := json.Unmarshal(raw, &rec); err != nil {
		return replay.Sample{}, err
	}
	if len(rec.Request) == 0 || len(rec.Stream) == 0 || len(rec.ResponseNonStream) == 0 {
		return replay.Sample{}, fmt.Errorf("recording needs request, stream and response_nonstream")
	}
	var request recordedRequest
	if err := json.Unmarshal(rec.Request, &request); err != nil {
		return replay.Sample{}, fmt.Errorf("request: %w", err)
	}
	messages, err := plainMessages(request.Messages)
	if err != nil {
		return replay.Sample{}, err
	}
	var maxTokens int64
	switch {
	case request.MaxTokens != nil:
		maxTokens = *request.MaxTokens
	case request.MaxCompletionTokens != nil:
		maxTokens = *request.MaxCompletionTokens
	}
	sample := replay.Sample{
		Name:          rec.Case,
		Model:         request.Model,
		Stream:        request.Stream,
		MaxTokens:     maxTokens,
		Messages:      messages,
		Response:      replay.Response{Role: "assistant", Content: rec.Served.Content},
		Request:       rec.Request,
		Recorded:      &replay.Recorded{Stream: rec.Stream, NonStream: rec.ResponseNonStream},
		ClientRequest: rec.Notes.Request.ClientRequest,
		Render:        renderCounts(rec),
		Build: &replay.Build{
			Model:    rec.Build.Model,
			Revision: rec.Build.Revision,
			Variant:  rec.Build.Variant,
			Commit:   vllmCommit(rec.Build.VLLMVersion, rec.Build.VLLMOverlay),
		},
	}
	return sample, nil
}

// plainMessages keeps the role and string content of each message, and an empty content for a
// null one (an assistant turn that only calls tools). The other fields of a message, such as
// reasoning, tool_calls and tool_call_id, are left to the sample's request, which carries them in
// full: the mock matches a recording by its request, never by this form. Content that is not a
// string has no plain form and is refused.
func plainMessages(raw []json.RawMessage) ([]replay.Message, error) {
	messages := make([]replay.Message, len(raw))
	for i, item := range raw {
		var message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(item, &message); err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		messages[i].Role = message.Role
		if len(message.Content) == 0 || string(message.Content) == "null" {
			continue
		}
		if err := json.Unmarshal(message.Content, &messages[i].Content); err != nil {
			return nil, fmt.Errorf("messages[%d]: content must be a string: %w", i, err)
		}
	}
	return messages, nil
}

// renderCounts keeps the build's render counts of a recording that has both.
func renderCounts(rec recording) *replay.RenderCounts {
	if rec.Host.RenderedInput == nil || rec.Host.RenderedOutput == nil {
		return nil
	}
	return &replay.RenderCounts{
		Input:     *rec.Host.RenderedInput,
		Output:    *rec.Host.RenderedOutput,
		TurnBytes: rec.GatewayBytes.Total,
	}
}

// vllmCommit is the fork commit an overlay build names, else the one in a dev version
// string such as 0.1.dev1+g71ee49c4d.
func vllmCommit(version string, overlay *vllmOverlay) string {
	if overlay != nil && overlay.SHA != "" {
		return overlay.SHA
	}
	if _, commit, ok := strings.Cut(version, "+g"); ok {
		return commit
	}
	return ""
}
