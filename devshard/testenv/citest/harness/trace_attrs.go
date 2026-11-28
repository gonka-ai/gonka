package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type otlpAttrValue struct {
	StringValue string   `json:"stringValue"`
	IntValue    string   `json:"intValue"`
	BoolValue   *bool    `json:"boolValue"`
	DoubleValue *float64 `json:"doubleValue"`
}

func (v otlpAttrValue) asString() string {
	if v.StringValue != "" {
		return v.StringValue
	}
	if v.IntValue != "" {
		return v.IntValue
	}
	if v.BoolValue != nil {
		return strconv.FormatBool(*v.BoolValue)
	}
	if v.DoubleValue != nil {
		return strconv.FormatFloat(*v.DoubleValue, 'f', -1, 64)
	}
	return ""
}

func tempoTraceSpanAttrs(client *http.Client, baseURL, traceID string) ([]map[string]string, bool) {
	u := strings.TrimRight(baseURL, "/") + "/api/traces/" + url.PathEscape(traceID)
	resp, err := client.Get(u)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false
	}

	var otlp struct {
		Batches []struct {
			ScopeSpans []struct {
				Spans []struct {
					Name       string `json:"name"`
					Attributes []struct {
						Key   string        `json:"key"`
						Value otlpAttrValue `json:"value"`
					} `json:"attributes"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"batches"`
	}
	if err := json.Unmarshal(body, &otlp); err == nil && len(otlp.Batches) > 0 {
		var out []map[string]string
		for _, b := range otlp.Batches {
			for _, ss := range b.ScopeSpans {
				for _, sp := range ss.Spans {
					attrs := make(map[string]string)
					if sp.Name != "" {
						attrs["name"] = sp.Name
					}
					for _, a := range sp.Attributes {
						if a.Key == "" {
							continue
						}
						if s := a.Value.asString(); s != "" {
							attrs[a.Key] = s
						}
					}
					out = append(out, attrs)
				}
			}
		}
		return out, len(out) > 0
	}

	return jaegerTraceSpanAttrsFromBody(body)
}

func jaegerTraceSpanAttrs(client *http.Client, baseURL, traceID string) ([]map[string]string, bool) {
	u := baseURL + "/jaeger/api/traces/" + url.PathEscape(traceID)
	resp, err := client.Get(u)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false
	}
	return jaegerTraceSpanAttrsFromBody(body)
}

func jaegerTraceSpanAttrsFromBody(body []byte) ([]map[string]string, bool) {
	var parsed struct {
		Data []struct {
			Spans []struct {
				OperationName string `json:"operationName"`
				Tags          []struct {
					Key   string `json:"key"`
					Type  string `json:"type"`
					Value any    `json:"value"`
				} `json:"tags"`
			} `json:"spans"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, false
	}
	var out []map[string]string
	for _, tr := range parsed.Data {
		for _, sp := range tr.Spans {
			attrs := make(map[string]string)
			if sp.OperationName != "" {
				attrs["name"] = sp.OperationName
			}
			for _, tag := range sp.Tags {
				if tag.Key == "" {
					continue
				}
				attrs[tag.Key] = fmt.Sprint(tag.Value)
			}
			out = append(out, attrs)
		}
	}
	return out, len(out) > 0
}
