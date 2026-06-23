package broker

import "decentralized-api/mlnode"

// Endpoint describes how to reach this node's MLNode (addressing + auth). It is
// the single seam all URL construction and client creation should go through.
// Total: it assembles strings and never fails; validity is checked at the
// registration/config boundary.
func (n *Node) Endpoint() mlnode.Endpoint {
	return mlnode.New(mlnode.Spec{
		Host:             n.Host,
		InferencePort:    n.InferencePort,
		InferenceSegment: n.InferenceSegment,
		PoCPort:          n.PoCPort,
		PoCSegment:       n.PoCSegment,
		BaseURL:          n.BaseURL,
		AuthToken:        n.AuthToken,
	})
}
