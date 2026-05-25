package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"decentralized-api/broker"
	"decentralized-api/mlnodeclient"
	"decentralized-api/poc"
	"decentralized-api/teeverify"

	"github.com/labstack/echo/v4"
)

const teeStatusIdentityTimeout = 3 * time.Second
const teeStatusMaxConcurrentProbes = 8

type TeeStatusResponse struct {
	Enabled          bool                           `json:"enabled"`
	Reason           string                         `json:"reason,omitempty"`
	Verifiers        []string                       `json:"verifiers"`
	Worker           *poc.TeeWorkerStatus           `json:"worker,omitempty"`
	ValidationWorker *poc.TeeValidationWorkerStatus `json:"validation_worker,omitempty"`
	NodesTotal       int                            `json:"nodes_total"`
	Nodes            []TeeStatusNodeReport          `json:"nodes"`
	AsOf             time.Time                      `json:"as_of"`
}

type TeeStatusNodeReport struct {
	NodeID           string `json:"node_id"`
	Host             string `json:"host"`
	Reachable        bool   `json:"reachable"`
	IdentityDisabled bool   `json:"identity_disabled,omitempty"`
	ProbeError       string `json:"probe_error,omitempty"`

	Provider        string `json:"provider,omitempty"`
	EnvelopeVersion string `json:"envelope_version,omitempty"`
	PublicKeyHex    string `json:"public_key_hex,omitempty"`

	KeyID        string `json:"key_id,omitempty"`
	QuoteSize    int    `json:"quote_size,omitempty"`
	QuoteHashHex string `json:"quote_hash_hex,omitempty"`
	Measurement  string `json:"measurement,omitempty"`

	VerifierFound bool   `json:"verifier_found"`
	VerifierOK    bool   `json:"verifier_ok"`
	VerifierError string `json:"verifier_error,omitempty"`
	VerifierTcb   string `json:"verifier_tcb,omitempty"`
	WouldSubmit   bool   `json:"would_submit"`
}

func (s *Server) getTeeStatus(ctx echo.Context) error {
	resp := TeeStatusResponse{
		AsOf: time.Now().UTC(),
	}

	if s.teeWorker == nil {
		resp.Enabled = false
		resp.Reason = "tee attestation worker is not enabled in dapi config"
		return ctx.JSONPretty(http.StatusOK, resp, "  ")
	}
	status := s.teeWorker.Status()
	resp.Worker = &status
	if !status.Enabled {
		resp.Enabled = false
		if status.DisabledReason == "" {
			resp.Reason = "tee attestation worker is disabled"
		} else {
			resp.Reason = "tee attestation worker is disabled: " + status.DisabledReason
		}
		return ctx.JSONPretty(http.StatusOK, resp, "  ")
	}
	if s.teeVerifiers == nil {

		resp.Enabled = false
		resp.Reason = "tee attestation worker has no verifier registry"
		return ctx.JSONPretty(http.StatusOK, resp, "  ")
	}
	resp.Enabled = true
	resp.Verifiers = s.teeVerifiers.Providers()

	if s.teeValidationWorker != nil {
		vstatus := s.teeValidationWorker.Status()
		resp.ValidationWorker = &vstatus
		if !vstatus.Enabled {
			resp.Enabled = false
			if vstatus.DisabledReason == "" {
				resp.Reason = "tee validation worker is disabled"
			} else {
				resp.Reason = "tee validation worker is disabled: " + vstatus.DisabledReason
			}
			return ctx.JSONPretty(http.StatusOK, resp, "  ")
		}
	}

	nodes, err := s.nodeBroker.GetNodes()
	if err != nil {
		resp.Reason = "failed to list broker nodes: " + err.Error()
		return ctx.JSONPretty(http.StatusOK, resp, "  ")
	}
	resp.NodesTotal = len(nodes)
	resp.Nodes = make([]TeeStatusNodeReport, len(nodes))
	var wg sync.WaitGroup
	sem := make(chan struct{}, teeStatusMaxConcurrentProbes)
	for i := range nodes {
		node := nodes[i].Node
		wg.Add(1)
		go func(idx int, n broker.Node) {
			defer wg.Done()
			sem <- struct{}{}
			resp.Nodes[idx] = s.buildTeeNodeReport(&n)
			<-sem
		}(i, node)
	}
	wg.Wait()
	return ctx.JSONPretty(http.StatusOK, resp, "  ")
}

func (s *Server) buildTeeNodeReport(node *broker.Node) TeeStatusNodeReport {
	report := TeeStatusNodeReport{
		NodeID: node.Id,
		Host:   node.Host,
	}

	ctxCall, cancel := context.WithTimeout(context.Background(), teeStatusIdentityTimeout)
	defer cancel()
	client := s.nodeBroker.NewNodeClient(node)
	identity, err := client.GetEncryptedIdentity(ctxCall)
	if err != nil {
		if errors.Is(err, mlnodeclient.ErrEncryptedIdentityDisabled) {

			report.Reachable = true
			report.IdentityDisabled = true
			return report
		}
		report.Reachable = false
		report.ProbeError = err.Error()
		return report
	}
	report.Reachable = true
	report.Provider = identity.Provider
	report.EnvelopeVersion = identity.EnvelopeVersion
	report.PublicKeyHex = hex.EncodeToString(identity.PublicKey)
	report.KeyID = identity.KeyID
	if len(identity.Quote) > 0 {
		report.QuoteSize = len(identity.Quote)
		h := sha256.Sum256(identity.Quote)
		report.QuoteHashHex = hex.EncodeToString(h[:])
	}
	report.Measurement = identity.Measurement

	verifier, ok := s.teeVerifiers.Lookup(identity.Provider)
	report.VerifierFound = ok
	if !ok {
		return report
	}
	vCtx, vCancel := context.WithTimeout(context.Background(), teeStatusIdentityTimeout)
	defer vCancel()
	rep, vErr := verifier.Verify(vCtx, teeverify.VerifyRequest{
		PublicKey:       identity.PublicKey,
		Quote:           identity.Quote,
		EnvelopeVersion: identity.EnvelopeVersion,
	})
	if vErr != nil {
		report.VerifierOK = false
		report.VerifierError = vErr.Error()
		return report
	}
	report.VerifierOK = true
	report.WouldSubmit = true
	if rep != nil {
		report.VerifierTcb = rep.TcbStatus

		if rep.Measurement != "" {
			report.Measurement = rep.Measurement
		}
	}
	return report
}
