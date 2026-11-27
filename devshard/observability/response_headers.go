package observability

import "strings"

var responseHeaderAllowlist = map[string]struct{}{
	"Content-Type":                  {},
	"Content-Length":                {},
	"X-Request-Id":                  {},
	"X-Request-ID":                  {},
	"Server":                        {},
	"X-Devshard-Error":              {},
	"Openai-Processing-Ms":          {},
	"X-Envoy-Upstream-Service-Time": {},
}

// FilterResponseHeaders keeps the first value of each allowlisted header, matched as given or in canonical MIME form.
func FilterResponseHeaders(headers map[string][]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	kept := make(map[string]string)
	for name, values := range headers {
		if _, allowed := responseHeaderAllowlist[name]; !allowed {
			canonicalName := canonicalHeaderName(name)
			if _, allowed := responseHeaderAllowlist[canonicalName]; !allowed {
				continue
			}
			name = canonicalName
		}
		if len(values) == 0 {
			continue
		}
		kept[name] = values[0]
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func canonicalHeaderName(name string) string {
	words := strings.Split(name, "-")
	for index, word := range words {
		if word == "" {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
	}
	return strings.Join(words, "-")
}
