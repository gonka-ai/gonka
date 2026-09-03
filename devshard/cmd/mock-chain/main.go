package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"devshard/bridge"
	"devshard/types"
)

const defaultPort = "8090"

type mockConfig struct {
	EscrowID            string              `json:"escrow_id"`
	CreatorAddress      string              `json:"creator_address"`
	Amount              uint64              `json:"amount"`
	EpochID             uint64              `json:"epoch_id"`
	AppHash             string              `json:"app_hash"`
	TokenPrice          uint64              `json:"token_price"`
	Hosts               []hostConfig        `json:"hosts"`
	ValidationThreshold bridge.Decimal      `json:"validation_threshold"`
	WarmGrants          map[string][]string `json:"warm_grants"`
}

type hostConfig struct {
	Address string `json:"address"`
	URL     string `json:"url"`
}

type escrowResponse struct {
	Escrow escrowJSON `json:"escrow"`
	Found  bool       `json:"found"`
}

type escrowJSON struct {
	ID         string   `json:"id"`
	Creator    string   `json:"creator"`
	Amount     string   `json:"amount"`
	Slots      []string `json:"slots"`
	EpochIndex string   `json:"epoch_index"`
	AppHash    string   `json:"app_hash"`
	Settled    bool     `json:"settled"`
	TokenPrice string   `json:"token_price"`
}

type participantResponse struct {
	Participant participantJSON `json:"participant"`
}

type participantJSON struct {
	Index        string `json:"index"`
	Address      string `json:"address"`
	InferenceURL string `json:"inference_url"`
	ValidatorKey string `json:"validator_key"`
}

type granteesResponse struct {
	Grantees []granteeJSON `json:"grantees"`
}

type granteeJSON struct {
	Address string `json:"address"`
	PubKey  string `json:"pub_key"`
}

type epochGroupDataResponse struct {
	EpochGroupData epochGroupDataJSON `json:"epoch_group_data"`
}

type epochGroupDataJSON struct {
	ModelSnapshot *modelSnapshotJSON `json:"model_snapshot,omitempty"`
}

type modelSnapshotJSON struct {
	ValidationThreshold bridge.Decimal `json:"validation_threshold"`
}

type paramsResponse struct {
	Params paramsJSON `json:"params"`
}

type paramsJSON struct {
	DevshardEscrowParams devshardEscrowParamsJSON `json:"devshard_escrow_params"`
}

type devshardEscrowParamsJSON struct {
	RefusalTimeout      string `json:"refusal_timeout"`
	ExecutionTimeout    string `json:"execution_timeout"`
	ValidationRate      uint32 `json:"validation_rate"`
	VoteThresholdFactor uint32 `json:"vote_threshold_factor"`
}

func main() {
	port := flag.String("port", envDefault("MOCK_CHAIN_PORT", defaultPort), "listen port")
	configPath := flag.String("config", os.Getenv("MOCK_CHAIN_CONFIG"), "optional JSON config path")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	addr := ":" + *port
	log.Printf("mock-chain listening on %s", addr)
	if err := http.ListenAndServe(addr, newHandler(cfg)); err != nil {
		log.Fatal(err)
	}
}

func newHandler(cfg mockConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/productscience/inference/inference/devshard_escrow/", func(w http.ResponseWriter, r *http.Request) {
		escrowID := strings.TrimPrefix(r.URL.Path, "/productscience/inference/inference/devshard_escrow/")
		if escrowID != cfg.EscrowID {
			writeJSON(w, http.StatusOK, map[string]any{
				"escrow": map[string]any{},
				"found":  false,
			})
			return
		}
		writeJSON(w, http.StatusOK, escrowResponse{
			Escrow: escrowJSON{
				ID:         cfg.EscrowID,
				Creator:    cfg.CreatorAddress,
				Amount:     strconv.FormatUint(cfg.Amount, 10),
				Slots:      cfg.slotAddresses(),
				EpochIndex: strconv.FormatUint(cfg.EpochID, 10),
				AppHash:    cfg.AppHash,
				Settled:    false,
				TokenPrice: strconv.FormatUint(cfg.TokenPrice, 10),
			},
			Found: true,
		})
	})
	mux.HandleFunc("/productscience/inference/inference/participant/", func(w http.ResponseWriter, r *http.Request) {
		address, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/productscience/inference/inference/participant/"))
		if err != nil {
			http.Error(w, "bad participant address", http.StatusBadRequest)
			return
		}
		host, ok := cfg.hostByAddress(address)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, participantResponse{Participant: participantJSON{
			Index:        host.Address,
			Address:      host.Address,
			InferenceURL: host.URL,
			ValidatorKey: "",
		}})
	})
	mux.HandleFunc("/productscience/inference/inference/epoch_group_data/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, epochGroupDataResponse{
			EpochGroupData: epochGroupDataJSON{
				ModelSnapshot: &modelSnapshotJSON{ValidationThreshold: cfg.ValidationThreshold},
			},
		})
	})
	mux.HandleFunc("/productscience/inference/inference/params", func(w http.ResponseWriter, _ *http.Request) {
		defaults := types.DefaultSessionConfig(len(cfg.Hosts))
		writeJSON(w, http.StatusOK, paramsResponse{
			Params: paramsJSON{
				DevshardEscrowParams: devshardEscrowParamsJSON{
					RefusalTimeout:      strconv.FormatInt(defaults.RefusalTimeout, 10),
					ExecutionTimeout:    strconv.FormatInt(defaults.ExecutionTimeout, 10),
					ValidationRate:      defaults.ValidationRate,
					VoteThresholdFactor: 50,
				},
			},
		})
	})
	mux.HandleFunc("/productscience/inference/inference/grantees_by_message_type/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/productscience/inference/inference/grantees_by_message_type/")
		parts := strings.SplitN(rest, "/", 2)
		validatorAddress, err := url.PathUnescape(parts[0])
		if err != nil {
			http.Error(w, "bad validator address", http.StatusBadRequest)
			return
		}
		grantees := make([]granteeJSON, 0, len(cfg.WarmGrants[validatorAddress]))
		for _, address := range cfg.WarmGrants[validatorAddress] {
			grantees = append(grantees, granteeJSON{Address: address, PubKey: ""})
		}
		writeJSON(w, http.StatusOK, granteesResponse{Grantees: grantees})
	})
	return mux
}

func loadConfig(path string) (mockConfig, error) {
	cfg := defaultConfig()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return mockConfig{}, err
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return mockConfig{}, err
		}
	}
	applyEnv(&cfg)
	if len(cfg.Hosts) == 0 {
		return mockConfig{}, fmt.Errorf("at least one host is required")
	}
	return cfg, nil
}

func defaultConfig() mockConfig {
	return mockConfig{
		EscrowID:            "1",
		CreatorAddress:      "inference1owner",
		Amount:              1_000_000,
		EpochID:             1,
		AppHash:             "deadbeef",
		TokenPrice:          1,
		ValidationThreshold: bridge.Decimal{Value: 1, Exponent: 0},
		Hosts: []hostConfig{
			{Address: "val0", URL: "http://devshard-host-0:8080"},
			{Address: "val1", URL: "http://devshard-host-1:8080"},
			{Address: "val2", URL: "http://devshard-host-2:8080"},
		},
		WarmGrants: map[string][]string{},
	}
}

func applyEnv(cfg *mockConfig) {
	cfg.EscrowID = envDefault("DEVSHARD_E2E_ESCROW_ID", cfg.EscrowID)
	cfg.CreatorAddress = envDefault("DEVSHARD_E2E_CREATOR_ADDRESS", cfg.CreatorAddress)
	cfg.AppHash = envDefault("DEVSHARD_E2E_APP_HASH", cfg.AppHash)
	cfg.Amount = uintEnv("DEVSHARD_E2E_AMOUNT", cfg.Amount)
	cfg.EpochID = uintEnv("DEVSHARD_E2E_EPOCH_ID", cfg.EpochID)
	cfg.TokenPrice = uintEnv("DEVSHARD_E2E_TOKEN_PRICE", cfg.TokenPrice)
	cfg.ValidationThreshold.Value = intEnv("DEVSHARD_E2E_VALIDATION_THRESHOLD_VALUE", cfg.ValidationThreshold.Value)
	cfg.ValidationThreshold.Exponent = int32(intEnv("DEVSHARD_E2E_VALIDATION_THRESHOLD_EXPONENT", int64(cfg.ValidationThreshold.Exponent)))
	if hosts := strings.TrimSpace(os.Getenv("DEVSHARD_E2E_HOSTS")); hosts != "" {
		cfg.Hosts = parseHosts(hosts)
	}
	if grants := strings.TrimSpace(os.Getenv("DEVSHARD_E2E_WARM_GRANTS")); grants != "" {
		cfg.WarmGrants = parseWarmGrants(grants)
	}
}

func parseHosts(raw string) []hostConfig {
	parts := strings.Split(raw, ",")
	hosts := make([]hostConfig, 0, len(parts))
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		address := fmt.Sprintf("val%d", i)
		hostURL := part
		if strings.Contains(part, "=") {
			pair := strings.SplitN(part, "=", 2)
			address = strings.TrimSpace(pair[0])
			hostURL = strings.TrimSpace(pair[1])
		}
		hosts = append(hosts, hostConfig{Address: address, URL: normalizeHTTPURL(hostURL)})
	}
	return hosts
}

func parseWarmGrants(raw string) map[string][]string {
	result := make(map[string][]string)
	for entry := range strings.SplitSeq(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" || !strings.Contains(entry, "=") {
			continue
		}
		pair := strings.SplitN(entry, "=", 2)
		validator := strings.TrimSpace(pair[0])
		for warm := range strings.SplitSeq(pair[1], "|") {
			warm = strings.TrimSpace(warm)
			if warm != "" {
				result[validator] = append(result[validator], warm)
			}
		}
	}
	return result
}

func normalizeHTTPURL(raw string) string {
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return "http://" + raw
}

func (c mockConfig) slotAddresses() []string {
	slots := make([]string, len(c.Hosts))
	for i, host := range c.Hosts {
		slots[i] = host.Address
	}
	return slots
}

func (c mockConfig) hostByAddress(address string) (hostConfig, bool) {
	for _, host := range c.Hosts {
		if host.Address == address {
			return host, true
		}
	}
	return hostConfig{}, false
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func uintEnv(key string, fallback uint64) uint64 {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		log.Fatalf("invalid %s: %v", key, err)
	}
	return value
}

func intEnv(key string, fallback int64) int64 {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		log.Fatalf("invalid %s: %v", key, err)
	}
	return value
}
