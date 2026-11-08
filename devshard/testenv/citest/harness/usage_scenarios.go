package harness

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"devshard/storage"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"
	"devshard/types"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// UsageScenarioStackOpts selects the recorded answers a usage scenario stack serves.
type UsageScenarioStackOpts struct {
	// ModelID is the seed escrow's model; it must be the model of the recordings.
	ModelID string
	// ReplayFile is the replay JSONL mock-openai serves (one recorded build).
	ReplayFile string
	// ValidationRate is the escrow's sampling rate in basis points. The testenv
	// turns 0 into its 6000 default, so 1 is the lowest rate it can express.
	ValidationRate uint32
	// ChildBinary, when set, is the devshardd every versiond host runs in
	// place of this tree's, as a fleet that has not taken this build yet.
	ChildBinary string
}

// BootUsageScenarioStack boots the error-miss topology (HA pair + two solos,
// three identities, so two non-executor voters can exceed VoteThreshold)
// with mock-openai replaying recorded answers for opts.ModelID, under the
// chain's default logprobs mode the recordings were made with.
//
// With TESTENV_CITEST_ATTACH_STACK set to the workdir of a stack an earlier
// run kept up (TESTENV_CITEST_KEEP_STACK=1), the scenarios run against that
// stack instead and leave it up.
func BootUsageScenarioStack(t *testing.T, prefix string, opts UsageScenarioStackOpts) (*Stack, *config.File, Endpoints) {
	t.Helper()
	require.NotEmpty(t, opts.ModelID)
	require.NotEmpty(t, opts.ReplayFile)
	if workDir := os.Getenv(attachStackEnv); workDir != "" {
		return attachUsageScenarioStack(t, workDir, opts)
	}
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteMultiConfig(t, stack.WorkDir, MultiConfigOpts{
		Hosts:       4,
		EscrowSlots: 3,
		// A recorded request reserves up to its 4096 max_tokens at the
		// default price, close to half the default escrow per request.
		EscrowAmount:   100_000_000,
		ValidationRate: opts.ValidationRate,
		// The Finish of a cut stream reaches the gateway when the execution
		// deadline makes it ask the group: 15 s instead of the 32-minute
		// default keeps a cut row short, and every recording streams well
		// within it.
		ExecutionTimeout: 15,
		ModelID:          opts.ModelID,
		ReplayFile:       opts.ReplayFile,
		LogprobsMode:     "processed_logprobs",
		// Each identity keeps its own store, as on the network: a vote one
		// identity saved must not be what another identity answers with.
		PostgresPerParticipant: true,
	})
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	requireFourVersiondHosts(t, cfg)
	require.Equal(t, opts.ModelID, config.PrimaryModelID(cfg))
	if opts.ChildBinary != "" {
		useChildBinary(t, stack, cfg, opts.ChildBinary)
	}
	stack.Up(t)
	eps := stack.Endpoints(t, cfg)
	client := GatewayChatClient()
	WaitStackHealthy(t, stack, eps)
	WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health via router", stack)
	openUsageScenarioModel(t, client, eps, opts.ModelID)
	return stack, cfg, eps
}

const attachStackEnv = "TESTENV_CITEST_ATTACH_STACK"

// useChildBinary mounts childBinary as the devshardd of every versiond host.
func useChildBinary(t *testing.T, stack *Stack, cfg *config.File, childBinary string) {
	t.Helper()
	data, err := os.ReadFile(childBinary)
	require.NoError(t, err, "child devshardd")
	require.NoError(t, os.WriteFile(filepath.Join(stack.WorkDir, "child-devshardd"), data, 0o755))
	for _, host := range cfg.Hosts {
		PatchVersiondServiceBinaryMount(t, stack.ComposePath, host.ID, "./child-devshardd")
	}
}

func attachUsageScenarioStack(t *testing.T, workDir string, opts UsageScenarioStackOpts) (*Stack, *config.File, Endpoints) {
	t.Helper()
	testenvDir, err := filepath.Abs("..")
	require.NoError(t, err)
	workDir, err = filepath.Abs(workDir)
	require.NoError(t, err)
	stack := &Stack{
		WorkDir:        workDir,
		TestenvDir:     testenvDir,
		ConfigPath:     filepath.Join(workDir, "config.yaml"),
		ComposePath:    filepath.Join(workDir, "docker-compose.yml"),
		Timeout:        defaultStackTimeout,
		ComposeProject: strings.ToLower(filepath.Base(workDir)),
	}
	cfg := stack.LoadConfig(t)
	requireFourVersiondHosts(t, cfg)
	require.Equal(t, opts.ModelID, config.PrimaryModelID(cfg))
	eps := stack.Endpoints(t, cfg)
	client := GatewayChatClient()
	WaitStackHealthy(t, stack, eps)
	WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	t.Logf("citest: attached to stack %s (%s)", stack.ComposeProject, attachStackEnv)
	openUsageScenarioModel(t, client, eps, opts.ModelID)
	return stack, cfg, eps
}

// openUsageScenarioModel lets an ordinary client use the model. Recorded
// requests come without an admin key, so the gateway caps max_tokens as it
// did for the recording; a model with no limits entry would need the key.
func openUsageScenarioModel(t *testing.T, client *http.Client, eps Endpoints, modelID string) {
	t.Helper()
	PatchGatewayAdminSettings(t, client, eps.GatewayHTTP, map[string]any{
		"model_limits": []map[string]any{{"model_id": modelID, "access_mode": "open"}},
	})
}

// GatewayRequestAttempt is one attempt of GET /v1/requests/{request_id}.
type GatewayRequestAttempt struct {
	Nonce        uint64 `json:"nonce"`
	HostIdx      int    `json:"host_idx"`
	Probe        bool   `json:"probe"`
	Winner       bool   `json:"winner"`
	Status       string `json:"status"`
	ExecutorSlot uint32 `json:"executor_slot"`
}

// GatewayRequestAccounting is the gateway's per-request accounting record.
type GatewayRequestAccounting struct {
	RequestID   string                  `json:"request_id"`
	Outcome     string                  `json:"outcome"`
	WinnerNonce uint64                  `json:"winner_nonce"`
	Attempts    []GatewayRequestAttempt `json:"attempts"`
}

// Nonce is the winning attempt or, for a request that ended without a
// winner, its first non-probe attempt.
func (a GatewayRequestAccounting) Nonce() uint64 {
	if a.WinnerNonce != 0 {
		return a.WinnerNonce
	}
	var first uint64
	for _, attempt := range a.Attempts {
		if !attempt.Probe && (first == 0 || attempt.Nonce < first) {
			first = attempt.Nonce
		}
	}
	return first
}

// WaitGatewayRequestWinner polls the request accounting of escrowID until it
// names the request's attempt (see Nonce).
func WaitGatewayRequestWinner(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, escrowID, requestID string, timeout time.Duration) GatewayRequestAccounting {
	t.Helper()
	require.NotEmpty(t, requestID, "gateway response carries no X-Request-Id")
	if client == nil {
		client = HTTPClient()
	}
	url := strings.TrimRight(gatewayURL, "/") + "/devshard/" + escrowID + "/v1/requests/" + requestID
	var (
		last    GatewayRequestAccounting
		lastErr error
	)
	ok := AssertEventually(t, timeout, 500*time.Millisecond, func() bool {
		last = GatewayRequestAccounting{}
		lastErr = getGatewayJSON(t, client, url, adminAPIKey, &last)
		return lastErr == nil && last.Outcome != "" && last.Nonce() != 0
	})
	require.True(t, ok, "request %s names no attempt after %s: last=%+v err=%v", requestID, timeout, last, lastErr)
	return last
}

// GatewayInferenceRecord is one record of GET /v1/debug/inferences with the
// counts and costs the state machine settled.
type GatewayInferenceRecord struct {
	Status       string   `json:"status"`
	ExecutorSlot uint32   `json:"executor_slot"`
	InputLength  uint64   `json:"input_length"`
	MaxTokens    uint64   `json:"max_tokens"`
	InputTokens  uint64   `json:"input_tokens"`
	OutputTokens uint64   `json:"output_tokens"`
	ReservedCost uint64   `json:"reserved_cost"`
	ActualCost   uint64   `json:"actual_cost"`
	VotesValid   uint32   `json:"votes_valid"`
	VotesInvalid uint32   `json:"votes_invalid"`
	ValidatedBy  []uint32 `json:"validated_by"`
}

// GetGatewayInferenceRecord reads one inference from GET /v1/debug/inferences.
func GetGatewayInferenceRecord(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string, nonce uint64) (GatewayInferenceRecord, bool) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	var body struct {
		Inferences map[string]GatewayInferenceRecord `json:"inferences"`
	}
	require.NoError(t, getGatewayJSON(t, client, gatewayURL+"/v1/debug/inferences", adminAPIKey, &body))
	rec, ok := body.Inferences[strconv.FormatUint(nonce, 10)]
	return rec, ok
}

// DiffTx is one transaction of a host's diff log, decoded with protojson:
// Kind is the DevshardTx oneof member and Body its message.
type DiffTx struct {
	Nonce uint64
	Kind  string
	Body  map[string]any
}

// InferenceID returns the inference the transaction is about, if it names one.
func (tx DiffTx) InferenceID() (uint64, bool) {
	raw, ok := tx.Body["inferenceId"]
	if !ok {
		return 0, false
	}
	switch v := raw.(type) {
	case string:
		id, err := strconv.ParseUint(v, 10, 64)
		return id, err == nil
	case float64:
		return uint64(v), true
	}
	return 0, false
}

// HostDiffTxs reads the diff log of escrowID from hostID for nonces from..to,
// through GET /sessions/{id}/diffs from inside the compose network, and
// decodes every transaction.
func HostDiffTxs(t *testing.T, stack *Stack, cfg *config.File, hostID, escrowID string, from, to uint64) []DiffTx {
	t.Helper()
	require.LessOrEqual(t, from, to)
	ver := cfg.Versiond.VersionName
	var txs []DiffTx
	for start := from; start <= to; start += uint64(storage.DiffPageMaxNonces) {
		end := min(start+uint64(storage.DiffPageMaxNonces)-1, to)
		url := fmt.Sprintf("http://%s:8080/%s/sessions/%s/diffs?from=%d&to=%d", hostID, ver, escrowID, start, end)
		out, err := stack.ComposeExecOutput("mock-chain", "wget", "-q", "-O", "-", "-T", "15", url)
		require.NoError(t, err)
		txs = append(txs, decodeDiffTxs(t, out)...)
	}
	return txs
}

func decodeDiffTxs(t *testing.T, raw string) []DiffTx {
	t.Helper()
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil
	}
	var records []struct {
		Diff struct {
			Nonce uint64 `json:"nonce"`
			Txs   []byte `json:"txs"`
		} `json:"diff"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &records), "decode diffs %s", truncateForLog(raw, 200))
	var txs []DiffTx
	for _, rec := range records {
		var content types.DiffContent
		require.NoError(t, proto.Unmarshal(rec.Diff.Txs, &content), "diff %d", rec.Diff.Nonce)
		for _, tx := range content.Txs {
			encoded, err := protojson.Marshal(tx)
			require.NoError(t, err)
			var oneof map[string]map[string]any
			require.NoError(t, json.Unmarshal(encoded, &oneof))
			for kind, body := range oneof {
				txs = append(txs, DiffTx{Nonce: rec.Diff.Nonce, Kind: kind, Body: body})
			}
		}
	}
	return txs
}

// sockCutImage carries ss, which the gateway image lacks.
const sockCutImage = "testenv-sockcut:local"

// CutGatewayStreams resets every TCP connection the gateway holds to a host's
// inference port, as a network failure between the gateway and the hosts
// would: versiond-router and the solo hosts the gateway dials directly alike.
// ss runs in the gateway's network namespace and the kernel aborts each
// socket. The hosts keep running and later connections succeed. It returns
// the peers cut.
func CutGatewayStreams(t *testing.T, stack *Stack) string {
	t.Helper()
	if exec.Command("docker", "image", "inspect", sockCutImage).Run() != nil {
		build := exec.Command("docker", "build", "-q", "-t", sockCutImage, "-")
		build.Stdin = strings.NewReader("FROM alpine:3.23\nRUN apk add --no-cache iproute2-ss\n")
		out, err := build.CombinedOutput()
		require.NoError(t, err, "build %s: %s", sockCutImage, out)
	}
	ps := exec.Command("docker", append(append([]string{"compose"}, stack.composeFileArgs()...), "ps", "-q", "devshardctl")...)
	ps.Dir, ps.Env = stack.WorkDir, stack.composeEnv()
	id, err := ps.Output()
	require.NoError(t, err)
	require.NotEmpty(t, strings.TrimSpace(string(id)), "no devshardctl container")
	out, err := exec.Command("docker", "run", "--rm", "--network", "container:"+strings.TrimSpace(string(id)),
		"--cap-add", "NET_ADMIN", sockCutImage, "ss", "-K", "-tnH", "state", "established", "( dport = :8080 )").CombinedOutput()
	require.NoError(t, err, "ss -K: %s", out)
	var peers []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if fields := strings.Fields(line); len(fields) >= 4 {
			peers = append(peers, fields[3])
		}
	}
	require.NotEmpty(t, peers, "the gateway held no connection to a host")
	return strings.Join(peers, " ")
}

// ArmStoredPayloadTamper makes the hosts' store of every answer whose prompt
// carries seed go wrong from the start, as an executor operator who controls
// the storage would. With replace false the write succeeds and the row is
// gone right after: the host signs its Finish and has nothing to serve. The
// prompt is kept in usage_withheld for StoredInferenceIDs. With replace true
// the response written is the lowest other answer of escrowID in the same
// store, so every store must already hold one. The tamper acts before the
// Finish is signed, so no voter can fetch the true body first; it is armed in
// every store, since any host may be the executor. DisarmStoredPayloadTamper
// removes it.
func ArmStoredPayloadTamper(t *testing.T, stack *Stack, cfg *config.File, escrowID string, seed int, replace bool) {
	t.Helper()
	if replace {
		for _, service := range config.PostgresServices(cfg) {
			out := payloadSQL(t, stack, cfg, service, fmt.Sprintf("SELECT count(*) FROM payload_storage WHERE escrow_id = '%s'", escrowID))
			require.NotEqual(t, "0", strings.TrimSpace(out), "%s holds no answer of escrow %s to replace with", service, escrowID)
		}
	}
	timing, action := "AFTER", `INSERT INTO usage_withheld VALUES (NEW.escrow_id, NEW.inference_id, NEW.prompt_payload);
    DELETE FROM payload_storage WHERE escrow_id = NEW.escrow_id AND inference_id = NEW.inference_id AND epoch_id = NEW.epoch_id;`
	if replace {
		timing, action = "BEFORE", `NEW.response_payload := (SELECT response_payload FROM payload_storage
      WHERE escrow_id = NEW.escrow_id AND inference_id <> NEW.inference_id ORDER BY inference_id LIMIT 1);`
	}
	query := fmt.Sprintf(`%[4]s
CREATE OR REPLACE FUNCTION usage_tamper_%[1]d() RETURNS trigger AS $$
BEGIN
  IF convert_from(NEW.prompt_payload, 'UTF8') ~ '"seed": *%[1]d[,} ]' THEN
    %[2]s
  END IF;
  RETURN NEW;
END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS usage_tamper_%[1]d ON payload_storage;
CREATE TRIGGER usage_tamper_%[1]d %[3]s INSERT ON payload_storage FOR EACH ROW EXECUTE FUNCTION usage_tamper_%[1]d();`, seed, action, timing, withheldTable)
	runPayloadSQL(t, stack, cfg, query)
}

const withheldTable = "CREATE TABLE IF NOT EXISTS usage_withheld (escrow_id TEXT, inference_id INT, prompt_payload BYTEA);"

// DisarmStoredPayloadTamper removes what ArmStoredPayloadTamper set for seed.
func DisarmStoredPayloadTamper(t *testing.T, stack *Stack, cfg *config.File, seed int) {
	t.Helper()
	runPayloadSQL(t, stack, cfg, fmt.Sprintf("DROP TRIGGER IF EXISTS usage_tamper_%[1]d ON payload_storage; DROP FUNCTION IF EXISTS usage_tamper_%[1]d();", seed))
}

// runPayloadSQL runs query in every store of the stack.
func runPayloadSQL(t *testing.T, stack *Stack, cfg *config.File, query string) {
	t.Helper()
	for _, service := range config.PostgresServices(cfg) {
		payloadSQL(t, stack, cfg, service, query)
	}
}

// payloadSQL runs query in one store and returns psql's unaligned output.
func payloadSQL(t *testing.T, stack *Stack, cfg *config.File, service, query string) string {
	t.Helper()
	user, db, pass := postgresCreds(cfg)
	out, err := stack.ComposeExecOutput(service, "env", "PGPASSWORD="+pass, "psql", "-v", "ON_ERROR_STOP=1", "-U", user, "-d", db, "-At", "-c", query)
	require.NoError(t, err, "psql %s: %s", service, out)
	return out
}

// ChallengeVoteRows counts the rows of devshard_challenge_votes for the
// inference over every store of the stack, and how many of them are settled.
// A voter keeps one row per challenged inference it voted on; the executor
// keeps none.
func ChallengeVoteRows(t *testing.T, stack *Stack, cfg *config.File, escrowID string, nonce uint64) (rows, settled int) {
	t.Helper()
	for _, service := range config.PostgresServices(cfg) {
		out := payloadSQL(t, stack, cfg, service, fmt.Sprintf(
			"SELECT count(*), count(*) FILTER (WHERE settled) FROM devshard_challenge_votes WHERE escrow_id = '%s' AND inference_id = %d", escrowID, nonce))
		fields := strings.Split(strings.TrimSpace(out), "|")
		require.Len(t, fields, 2, "psql %s: %s", service, out)
		n, err := strconv.Atoi(fields[0])
		require.NoError(t, err, "psql %s: %s", service, out)
		s, err := strconv.Atoi(fields[1])
		require.NoError(t, err, "psql %s: %s", service, out)
		rows += n
		settled += s
	}
	return rows, settled
}

// StoredInferenceIDs lists, lowest first, the inferences of escrowID whose
// stored prompt carries the client seed in any store of the stack: the
// attempts of one request when every request has a seed of its own.
func StoredInferenceIDs(t *testing.T, stack *Stack, cfg *config.File, escrowID string, seed int) []uint64 {
	t.Helper()
	_, err := strconv.ParseUint(escrowID, 10, 64)
	require.NoError(t, err, "escrow id %q is not numeric", escrowID)
	// The prompts ArmStoredPayloadTamper withheld count too.
	runPayloadSQL(t, stack, cfg, withheldTable)
	query := fmt.Sprintf(`SELECT inference_id FROM (SELECT escrow_id, inference_id, prompt_payload FROM payload_storage
UNION SELECT escrow_id, inference_id, prompt_payload FROM usage_withheld) p WHERE escrow_id = '%s'
AND convert_from(prompt_payload, 'UTF8') ~ '"seed": *%d[,} ]'`, escrowID, seed)
	var ids []uint64
	for _, service := range config.PostgresServices(cfg) {
		out := payloadSQL(t, stack, cfg, service, query)
		for _, field := range strings.Fields(out) {
			id, err := strconv.ParseUint(field, 10, 64)
			require.NoError(t, err, "psql %s: %s", service, out)
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// GatewayPendingRaceCleanup is the background work the gateway still runs
// for escrowID (timeout votes, challenges, refunds). The escrow cannot be
// finalized or settled while it is above zero.
func GatewayPendingRaceCleanup(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, escrowID string) int64 {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	var body struct {
		Devshards []struct {
			ID      string `json:"id"`
			Runtime *struct {
				PendingRaceCleanup int64 `json:"pending_race_cleanup"`
			} `json:"runtime"`
		} `json:"devshards"`
	}
	require.NoError(t, getGatewayJSON(t, client, gatewayURL+"/v1/admin/devshards", adminAPIKey, &body))
	for _, devshard := range body.Devshards {
		if devshard.ID == escrowID && devshard.Runtime != nil {
			return devshard.Runtime.PendingRaceCleanup
		}
	}
	t.Fatalf("gateway has no runtime for escrow %s", escrowID)
	return 0
}

// GetMockOpenAIStats reads mock-openai's request outcome counters.
func GetMockOpenAIStats(t *testing.T, client *http.Client, mockOpenAIURL string) mockopenai.Stats {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	var stats mockopenai.Stats
	require.NoError(t, GetJSON(client, mockOpenAIURL+"/testenv/stats", &stats))
	return stats
}

// MetricDelta is after minus before for one metric series, matching labels.
func MetricDelta(before, after []ParsedMetric, name string, labels map[string]string) float64 {
	sum := func(metrics []ParsedMetric) float64 {
		var total float64
		for _, m := range metrics {
			if m.Name == name && labelsContain(m.Labels, labels) {
				total += m.Value
			}
		}
		return total
	}
	return sum(after) - sum(before)
}
