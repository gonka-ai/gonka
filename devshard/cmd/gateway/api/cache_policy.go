package api

import (
	"net/http"
	"strings"
)

func requestedCacheDirective(header http.Header) cacheDirective {
	directive := cacheDirectiveNone
	for _, value := range header.Values(cacheControlHeader) {
		for token := range strings.SplitSeq(value, directiveListSeparator) {
			name, _, _ := strings.Cut(token, directiveValueSeparator)
			switch cacheDirective(strings.ToLower(strings.TrimSpace(name))) {
			case cacheDirectiveNoStore:
				return cacheDirectiveNoStore
			case cacheDirectiveNoCache:
				directive = cacheDirectiveNoCache
			}
		}
	}
	return directive
}

func (d cacheDirective) looksUp() bool { return d == cacheDirectiveNone }

func (d cacheDirective) stores() bool { return d != cacheDirectiveNoStore }
