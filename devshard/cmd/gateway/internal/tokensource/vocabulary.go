// Package tokensource names where a prompt token count came from, for the packages that carry the count without reading a request.
package tokensource

// Source names where the gateway's count of a request's prompt tokens came from. See ../../docs/request.md, "The prompt token count".
type Source string

const (
	TokenizerExact   Source = "tokenizer_exact"
	TokenizerSampled Source = "tokenizer_sampled"
	BytesEstimate    Source = "bytes_estimate"
)

// Tokenized reports whether the count came from the model's tokenizer; any other source, the unset one included, is the byte estimate.
func (s Source) Tokenized() bool {
	return s == TokenizerExact || s == TokenizerSampled
}
