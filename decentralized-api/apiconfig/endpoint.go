package apiconfig

import (
	"strings"

	"decentralized-api/mlnode"
)

// Normalize canonicalizes operator-entered addressing input so the value that is
// validated, stored, duplicate-checked, and turned into an Endpoint is one and
// the same. It strips surrounding whitespace and any trailing slash from BaseURL
// (Host-Port fields are left untouched, and an empty BaseURL stays empty). Call
// it once at the registration boundary, before validation/storage — that keeps
// mlnode.New and mlnode.Validate operating on identical input instead of each
// having to re-trim, and stops a stray-whitespace base_url from dodging the
// duplicate check or building a space-laced URL.
func (n *InferenceNodeConfig) Normalize() {
	n.BaseURL = strings.TrimRight(strings.TrimSpace(n.BaseURL), "/")
}

// Endpoint describes how to reach this node's MLNode (addressing + auth). It is
// the single seam all URL construction and client creation should go through.
// Total: it assembles strings and never fails; validity is checked at the
// registration/config boundary.
func (n InferenceNodeConfig) Endpoint() mlnode.Endpoint {
	return mlnode.New(n.spec())
}

// spec maps the config's addressing fields onto an mlnode.Spec. Shared by
// Endpoint construction and validation so both see the same fields.
func (n InferenceNodeConfig) spec() mlnode.Spec {
	return mlnode.Spec{
		Host:             n.Host,
		InferencePort:    n.InferencePort,
		InferenceSegment: n.InferenceSegment,
		PoCPort:          n.PoCPort,
		PoCSegment:       n.PoCSegment,
		BaseURL:          n.BaseURL,
		AuthToken:        n.AuthToken,
	}
}
