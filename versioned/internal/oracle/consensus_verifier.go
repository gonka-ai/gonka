package oracle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ConsensusVerifier checks DAPI's projected catalog against the local node's
// consensus state. This gives a fresh versiond store a trust anchor without an
// operator-managed revision floor.
type ConsensusVerifier struct {
	paramsURL  string
	statusURL  string
	httpClient *http.Client
}

func NewConsensusVerifier(paramsURL, statusURL string) *ConsensusVerifier {
	return &ConsensusVerifier{
		paramsURL: paramsURL,
		statusURL: statusURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (v *ConsensusVerifier) Verify(ctx context.Context, candidate VersionConfig) error {
	catchingUp, err := v.fetchCatchingUp(ctx)
	if err != nil {
		return err
	}
	if catchingUp {
		return fmt.Errorf("local consensus node is catching up")
	}
	versions, err := v.fetchApprovedVersions(ctx)
	if err != nil {
		return err
	}
	if err := validateVersions(versions); err != nil {
		return fmt.Errorf("validate consensus approved versions: %w", err)
	}
	if !versionArtifactsEqual(candidate.Versions, versions) {
		return fmt.Errorf("DAPI catalog does not match local consensus state")
	}
	return nil
}

func (v *ConsensusVerifier) fetchCatchingUp(ctx context.Context) (bool, error) {
	var response struct {
		Result struct {
			SyncInfo struct {
				CatchingUp *bool `json:"catching_up"`
			} `json:"sync_info"`
		} `json:"result"`
	}
	if err := v.getJSON(ctx, v.statusURL, &response); err != nil {
		return false, fmt.Errorf("read consensus sync status: %w", err)
	}
	if response.Result.SyncInfo.CatchingUp == nil {
		return false, fmt.Errorf("consensus status response has no catching_up flag")
	}
	return *response.Result.SyncInfo.CatchingUp, nil
}

func (v *ConsensusVerifier) fetchApprovedVersions(ctx context.Context) ([]Version, error) {
	var response struct {
		Params struct {
			DevshardEscrowParams *struct {
				ApprovedVersions []Version `json:"approved_versions"`
			} `json:"devshard_escrow_params"`
		} `json:"params"`
	}
	if err := v.getJSON(ctx, v.paramsURL, &response); err != nil {
		return nil, fmt.Errorf("read consensus inference params: %w", err)
	}
	if response.Params.DevshardEscrowParams == nil {
		return nil, fmt.Errorf("consensus response has no devshard escrow params")
	}
	return response.Params.DevshardEscrowParams.ApprovedVersions, nil
}

func (v *ConsensusVerifier) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned status %d", endpoint, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return nil
}

func versionArtifactsEqual(first, second []Version) bool {
	if len(first) != len(second) {
		return false
	}
	byName := make(map[string]Version, len(first))
	for _, version := range first {
		byName[version.Name] = version
	}
	for _, version := range second {
		candidate, ok := byName[version.Name]
		if !ok || candidate.Binary != version.Binary {
			return false
		}
		candidateSHA, candidateErr := candidate.ResolvedSHA256()
		versionSHA, versionErr := version.ResolvedSHA256()
		if candidateErr != nil || versionErr != nil || candidateSHA != versionSHA {
			return false
		}
	}
	return true
}
