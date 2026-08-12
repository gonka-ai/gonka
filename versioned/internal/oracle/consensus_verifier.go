package oracle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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
	catchingUp, latestHeight, err := v.fetchConsensusStatus(ctx)
	if err != nil {
		return err
	}
	if catchingUp {
		return fmt.Errorf("local consensus node is catching up")
	}
	if candidate.Revision <= 0 || candidate.Revision > latestHeight {
		return fmt.Errorf(
			"DAPI catalog revision %d is outside local consensus height 1..%d",
			candidate.Revision,
			latestHeight,
		)
	}
	versions, err := v.fetchApprovedVersions(ctx, candidate.Revision)
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

func (v *ConsensusVerifier) fetchConsensusStatus(ctx context.Context) (bool, int64, error) {
	var response struct {
		Result struct {
			SyncInfo struct {
				CatchingUp        *bool  `json:"catching_up"`
				LatestBlockHeight string `json:"latest_block_height"`
			} `json:"sync_info"`
		} `json:"result"`
	}
	if err := v.getJSON(ctx, v.statusURL, &response); err != nil {
		return false, 0, fmt.Errorf("read consensus sync status: %w", err)
	}
	if response.Result.SyncInfo.CatchingUp == nil {
		return false, 0, fmt.Errorf("consensus status response has no catching_up flag")
	}
	height, err := strconv.ParseInt(response.Result.SyncInfo.LatestBlockHeight, 10, 64)
	if err != nil || height <= 0 {
		return false, 0, fmt.Errorf(
			"consensus status has invalid latest_block_height %q",
			response.Result.SyncInfo.LatestBlockHeight,
		)
	}
	return *response.Result.SyncInfo.CatchingUp, height, nil
}

func (v *ConsensusVerifier) fetchApprovedVersions(ctx context.Context, height int64) ([]Version, error) {
	var response struct {
		Params struct {
			DevshardEscrowParams *struct {
				ApprovedVersions []Version `json:"approved_versions"`
			} `json:"devshard_escrow_params"`
		} `json:"params"`
	}
	if err := v.getJSONAtHeight(ctx, v.paramsURL, height, &response); err != nil {
		return nil, fmt.Errorf("read consensus inference params: %w", err)
	}
	if response.Params.DevshardEscrowParams == nil {
		return nil, fmt.Errorf("consensus response has no devshard escrow params")
	}
	return response.Params.DevshardEscrowParams.ApprovedVersions, nil
}

func (v *ConsensusVerifier) getJSONAtHeight(
	ctx context.Context,
	endpoint string,
	height int64,
	target any,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	wantedHeight := strconv.FormatInt(height, 10)
	req.Header.Set("x-cosmos-block-height", wantedHeight)
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned status %d", endpoint, resp.StatusCode)
	}
	observedHeight := resp.Header.Get("x-cosmos-block-height")
	if observedHeight == "" {
		// grpc-gateway exposes response metadata with this prefix on some SDK
		// versions while others copy the canonical header directly.
		observedHeight = resp.Header.Get("grpc-metadata-x-cosmos-block-height")
	}
	if observedHeight != wantedHeight {
		return fmt.Errorf(
			"%s returned consensus height %q, want %s",
			endpoint,
			observedHeight,
			wantedHeight,
		)
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return nil
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
