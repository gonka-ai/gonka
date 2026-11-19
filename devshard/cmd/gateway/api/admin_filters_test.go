package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/filters"
)

const (
	filtersQwenModel  = "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"
	filtersKimiModel  = "moonshotai/Kimi-K2.6"
	filtersChainOnly  = "deepseek-ai/DeepSeek-V4-Flash-0731"
	filtersOffChain   = "acme/Llama-5-70B"
	filtersRoutePath  = "/v1/admin/filters/"
	filtersListPath   = "/v1/admin/filters"
	filtersPresetPath = "/v1/admin/filters/presets"
)

type filterEntryResponse struct {
	Warnings  []string       `json:"warnings"`
	Problem   string         `json:"problem"`
	Model     string         `json:"model"`
	Source    string         `json:"source"`
	Preset    string         `json:"preset"`
	Effective map[string]any `json:"effective"`
	OnChain   bool           `json:"on_chain"`
	UpdatedAt string         `json:"updated_at"`
}

type errorResponse struct {
	Error struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

func newFiltersHarness(t *testing.T) *harness {
	t.Helper()
	live := newHarness(t)
	live.snapshots.snapshot.Models = map[string]chain.ModelParams{
		filtersQwenModel: {}, filtersKimiModel: {}, filtersChainOnly: {},
	}
	return live
}

func decodeInto[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var decoded T
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decoding %s: %v", recorder.Body.String(), err)
	}
	return decoded
}

func putFilters(t *testing.T, live *harness, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	return live.request(t, http.MethodPut, target, body, adminHeaders())
}

// Test flow:
//  1. PUT the minimax preset for Qwen.
//  2. Assert 200 with a stored entry whose effective thinking is strip, on chain, with a time.
func TestAdminFiltersPutPreset(t *testing.T) {
	live := newFiltersHarness(t)

	recorder := putFilters(t, live, filtersRoutePath+filtersQwenModel, `{"preset":"minimax"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	entry := decodeInto[filterEntryResponse](t, recorder)
	if entry.Model != filtersQwenModel || entry.Source != "stored" || entry.Preset != "minimax" || !entry.OnChain || entry.UpdatedAt == "" {
		t.Errorf("entry = %+v", entry)
	}
	if entry.Effective["thinking"] != "strip" {
		t.Errorf("effective thinking = %v, want strip", entry.Effective["thinking"])
	}
}

// Test flow:
//  1. PUT glm_flash with an output-token floor on Qwen.
//  2. Assert the effective profile forces thinking on and carries the floor.
func TestAdminFiltersPutPresetWithKnobs(t *testing.T) {
	live := newFiltersHarness(t)

	recorder := putFilters(t, live, filtersRoutePath+filtersQwenModel, `{"preset":"glm_flash","filters":{"output_tokens_floor":32}}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	entry := decodeInto[filterEntryResponse](t, recorder)
	if entry.Effective["thinking"] != "force_on" || entry.Effective["output_tokens_floor"] != float64(32) {
		t.Errorf("effective = %v, want force_on with floor 32", entry.Effective)
	}
}

// Test flow:
//  1. PUT a setting for a model the chain does not register.
//  2. Assert 404 with the model_not_on_chain code.
func TestAdminFiltersRefusesModelNotOnChain(t *testing.T) {
	live := newFiltersHarness(t)

	recorder := putFilters(t, live, filtersRoutePath+filtersOffChain, `{"preset":"default"}`)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body %s", recorder.Code, recorder.Body.String())
	}
	if code := decodeInto[errorResponse](t, recorder).Error.Code; code != "model_not_on_chain" {
		t.Errorf("code = %q, want model_not_on_chain", code)
	}
	if _, stored := live.filters.Records()[filtersOffChain]; stored {
		t.Error("a refused setting was stored")
	}
}

// Test flow:
//  1. PUT the same off-chain setting with allow_off_chain=true.
//  2. Assert 200, stored, and on_chain false.
func TestAdminFiltersAllowOffChain(t *testing.T) {
	live := newFiltersHarness(t)

	recorder := putFilters(t, live, filtersRoutePath+filtersOffChain+"?allow_off_chain=true", `{"preset":"default"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if entry := decodeInto[filterEntryResponse](t, recorder); entry.OnChain || entry.Source != "stored" {
		t.Errorf("entry = %+v, want stored and off chain", entry)
	}
}

// Test flow:
//  1. Empty the chain model list, as at boot before the first poll.
//  2. Assert a PUT is 503 chain_models_unavailable, and the same PUT with allow_off_chain=true is 200.
func TestAdminFiltersEmptySnapshot(t *testing.T) {
	live := newFiltersHarness(t)
	live.snapshots.snapshot.Models = nil

	refused := putFilters(t, live, filtersRoutePath+filtersQwenModel, `{"preset":"minimax"}`)
	allowed := putFilters(t, live, filtersRoutePath+filtersQwenModel+"?allow_off_chain=true", `{"preset":"minimax"}`)

	if refused.Code != http.StatusServiceUnavailable || decodeInto[errorResponse](t, refused).Error.Code != "chain_models_unavailable" {
		t.Errorf("without the flag: status %d body %s, want 503 chain_models_unavailable", refused.Code, refused.Body.String())
	}
	if allowed.Code != http.StatusOK {
		t.Errorf("with the flag: status %d body %s, want 200", allowed.Code, allowed.Body.String())
	}
}

// Test flow:
//  1. PUT the kimi preset on Qwen, which the chain registers.
//  2. Assert 200 and an effective budget with Kimi's absolute max.
func TestAdminFiltersAllowsBudgetOnChain(t *testing.T) {
	live := newFiltersHarness(t)

	recorder := putFilters(t, live, filtersRoutePath+filtersQwenModel, `{"preset":"kimi"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	budget, _ := decodeInto[filterEntryResponse](t, recorder).Effective["thinking_token_budget"].(map[string]any)
	if budget["absolute_max"] != float64(96000) {
		t.Errorf("effective budget = %v, want absolute_max 96000", budget)
	}
}

// Test flow:
//  1. PUT a budget, then the empty-answer exemption alone, for an off-chain model with allow_off_chain=true.
//  2. Assert both are 404 model_not_on_chain and nothing is stored: these two knobs need a model the chain registers.
func TestAdminFiltersRefusesBudgetOffChain(t *testing.T) {
	live := newFiltersHarness(t)
	for _, body := range []string{`{"preset":"kimi"}`, `{"filters":{"empty_answer_counts_tokens":true}}`} {
		t.Run(body, func(t *testing.T) {
			recorder := putFilters(t, live, filtersRoutePath+filtersOffChain+"?allow_off_chain=true", body)
			if recorder.Code != http.StatusNotFound || decodeInto[errorResponse](t, recorder).Error.Code != "model_not_on_chain" {
				t.Errorf("status %d body %s, want 404 model_not_on_chain", recorder.Code, recorder.Body.String())
			}
		})
	}
	if len(live.filters.Records()) != 0 {
		t.Error("a refused setting was stored")
	}
}

// Test flow:
//  1. PUT bodies with a budget of the wrong shape, an unknown top-level key, an unknown key inside filters, an unknown thinking value, and a second JSON value after the first.
//  2. Assert each is 400.
func TestAdminFiltersRejectsUnknownFilter(t *testing.T) {
	live := newFiltersHarness(t)
	for _, body := range []string{
		`{"filters":{"thinking_token_budget":true}}`,
		`{"preset":"minimax","temperature_max":1}`,
		`{"filters":{"thinking":"maybe"}}`,
		`{"filters":{"temperature_max":1}}`,
		`{"preset":"minimax"}{"preset":"kimi"}`,
	} {
		t.Run(body, func(t *testing.T) {
			if recorder := putFilters(t, live, filtersRoutePath+filtersQwenModel, body); recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// Test flow:
//  1. PUT an empty body, an empty object, and an empty preset with no filters.
//  2. Assert each is 400 and nothing is stored.
func TestAdminFiltersRejectsEmptySetting(t *testing.T) {
	live := newFiltersHarness(t)
	for _, body := range []string{``, `{}`, `{"preset":""}`, `{"filters":{}}`} {
		t.Run("body="+body, func(t *testing.T) {
			if recorder := putFilters(t, live, filtersRoutePath+filtersQwenModel, body); recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body %s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if len(live.filters.Records()) != 0 {
		t.Error("an empty setting was stored")
	}
}

// Test flow:
//  1. PUT with the model's slash percent-encoded, then GET it with a plain slash.
//  2. Assert the GET finds the stored entry.
func TestAdminFiltersPercentEncodedModel(t *testing.T) {
	live := newFiltersHarness(t)

	put := putFilters(t, live, filtersRoutePath+"moonshotai%2FKimi-K2.6", `{"preset":"default"}`)
	got := live.request(t, http.MethodGet, filtersRoutePath+filtersKimiModel, "", adminHeaders())

	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body %s", put.Code, put.Body.String())
	}
	if entry := decodeInto[filterEntryResponse](t, got); entry.Source != "stored" || entry.Model != filtersKimiModel {
		t.Errorf("GET entry = %+v, want the stored Kimi entry", entry)
	}
}

// Test flow:
//  1. Reject a model path that breaks the model-name rule.
//  2. Assert 400.
func TestAdminFiltersRejectsInvalidModelName(t *testing.T) {
	live := newFiltersHarness(t)

	recorder := putFilters(t, live, filtersRoutePath+"bad%20model", `{"preset":"default"}`)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body %s", recorder.Code, recorder.Body.String())
	}
}

// Test flow:
//  1. Store a setting for Qwen, then list all models.
//  2. Assert Qwen is stored, Kimi is built-in, a chain-only model is default, and an off-chain built-in is listed off chain.
func TestAdminFiltersListShowsSources(t *testing.T) {
	live := newFiltersHarness(t)
	putFilters(t, live, filtersRoutePath+filtersQwenModel, `{"preset":"minimax"}`)

	recorder := live.request(t, http.MethodGet, filtersListPath, "", adminHeaders())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	listed := decodeInto[struct {
		Models []filterEntryResponse `json:"models"`
	}](t, recorder)
	sources := map[string]filterEntryResponse{}
	for _, entry := range listed.Models {
		sources[entry.Model] = entry
	}
	if sources[filtersQwenModel].Source != "stored" || sources[filtersKimiModel].Source != "built_in" || sources[filtersChainOnly].Source != "default" {
		t.Errorf("sources = %+v", sources)
	}
	if glm, listedGLM := sources["zai-org/GLM-5.3-Flash"]; !listedGLM || glm.OnChain || glm.Source != "built_in" {
		t.Errorf("GLM entry = %+v, want built-in and off chain", glm)
	}
}

// Test flow:
//  1. Store a default setting for Kimi, then DELETE it.
//  2. Assert 200 with the built-in entry it reverted to, and a GET shows the same.
func TestAdminFiltersDeleteReverts(t *testing.T) {
	live := newFiltersHarness(t)
	putFilters(t, live, filtersRoutePath+filtersKimiModel, `{"preset":"default"}`)

	deleted := live.request(t, http.MethodDelete, filtersRoutePath+filtersKimiModel, "", adminHeaders())

	if deleted.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", deleted.Code, deleted.Body.String())
	}
	entry := decodeInto[filterEntryResponse](t, deleted)
	if entry.Source != "built_in" || entry.Preset != "kimi" || entry.Effective["thinking_token_budget"] == nil {
		t.Errorf("entry = %+v, want Kimi back on its built-in preset", entry)
	}
}

// Test flow:
//  1. GET the preset catalog.
//  2. Assert four presets and that kimi carries the thinking budget.
func TestAdminFilterPresetsListsCatalog(t *testing.T) {
	live := newFiltersHarness(t)

	recorder := live.request(t, http.MethodGet, filtersPresetPath, "", adminHeaders())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	catalog := decodeInto[struct {
		Presets map[string]map[string]any `json:"presets"`
	}](t, recorder)
	if len(catalog.Presets) != 4 || catalog.Presets["kimi"]["thinking_token_budget"] == nil {
		t.Errorf("presets = %v", catalog.Presets)
	}
}

// Test flow:
//  1. Call a filter route with no key, then with the admin surface disabled.
//  2. Assert 401, then 404.
func TestAdminFiltersRequiresAdmin(t *testing.T) {
	live := newFiltersHarness(t)

	anonymous := live.request(t, http.MethodGet, filtersListPath, "", nil)
	live.swapConfig(func(next *config.Config) { next.Server.AdminAPIKey = "" })
	disabled := live.request(t, http.MethodGet, filtersListPath, "", adminHeaders())

	if anonymous.Code != http.StatusUnauthorized {
		t.Errorf("no key: status %d, want 401", anonymous.Code)
	}
	if disabled.Code != http.StatusNotFound {
		t.Errorf("admin disabled: status %d, want 404", disabled.Code)
	}
}

// Test flow:
//  1. PUT with allow_off_chain set to a word that is not a boolean.
//  2. Assert 400.
func TestAdminFiltersRejectsBadAllowOffChain(t *testing.T) {
	live := newFiltersHarness(t)

	recorder := putFilters(t, live, filtersRoutePath+filtersQwenModel+"?allow_off_chain=sure", `{"preset":"minimax"}`)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body %s", recorder.Code, recorder.Body.String())
	}
}

// Test flow:
//  1. Give Qwen model_limits in the settings and register it on chain with a governed context length.
//  2. GET its filter entry, and PUT a body that tries to set limits.
//  3. Assert the entry shows the settings' limits marked read-only, and the PUT is a 400.
func TestAdminFilterEntryShowsLimitsReadOnly(t *testing.T) {
	live := newFiltersHarness(t)
	live.swapConfig(func(next *config.Config) {
		next.Limits.ModelLimits = map[string]config.ModelLimits{filtersQwenModel: {DefaultMaxTokens: 1000, MaxTokensCap: 2000}}
	})
	live.snapshots.snapshot.Models[filtersQwenModel] = chain.ModelParams{MaxModelLen: 32768}

	recorder := live.request(t, http.MethodGet, filtersRoutePath+filtersQwenModel, "", adminHeaders())
	refused := putFilters(t, live, filtersRoutePath+filtersQwenModel, `{"preset":"minimax","limits":{"max_tokens_cap":9}}`)

	limits := decodeInto[struct {
		Limits map[string]any `json:"limits"`
	}](t, recorder).Limits
	want := map[string]any{"default_max_tokens": float64(1000), "max_tokens_cap": float64(2000), "max_model_len": float64(32768), "source": "model_limits"}
	for key, value := range want {
		if limits[key] != value {
			t.Errorf("limits[%s] = %v, want %v (limits %v)", key, limits[key], value, limits)
		}
	}
	if refused.Code != http.StatusBadRequest {
		t.Errorf("PUT with limits: status %d, want 400", refused.Code)
	}
}

// Test flow:
//  1. PUT only an output-token floor for Kimi, with no preset named.
//  2. Assert the setting composes over Kimi's own built-in preset: the floor changes, the budget and the empty-answer exemption stay.
func TestAdminFiltersKnobsOnlyKeepTheBuiltInPreset(t *testing.T) {
	live := newFiltersHarness(t)

	recorder := putFilters(t, live, filtersRoutePath+filtersKimiModel, `{"filters":{"output_tokens_floor":32}}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	entry := decodeInto[filterEntryResponse](t, recorder)
	if entry.Preset != "kimi" || entry.Effective["empty_answer_counts_tokens"] != true || entry.Effective["thinking_token_budget"] == nil || entry.Effective["output_tokens_floor"] != float64(32) {
		t.Errorf("entry = %+v, want Kimi's preset with only the floor changed", entry)
	}
}

// Test flow:
//  1. Hold stored rows the gateway could not read at boot: one for Kimi naming a retired preset, one for GLM naming none.
//  2. GET Kimi's entry.
//  3. Assert it shows the row as unreadable with its problem, while the effective filters are Kimi's built-in preset.
func TestAdminFilterEntryShowsAnUnreadableRow(t *testing.T) {
	live := newFiltersHarness(t)
	live.filters.records[filtersKimiModel] = ModelFilterRecord{Setting: filters.ModelSetting{Preset: "retired"}, Problem: "unknown preset \"retired\""}
	live.filters.records["zai-org/GLM-5.3-Flash"] = ModelFilterRecord{Problem: "decoding filters: invalid JSON"}

	entry := decodeInto[filterEntryResponse](t, live.request(t, http.MethodGet, filtersRoutePath+filtersKimiModel, "", adminHeaders()))
	unnamed := decodeInto[filterEntryResponse](t, live.request(t, http.MethodGet, filtersRoutePath+"zai-org/GLM-5.3-Flash", "", adminHeaders()))

	if entry.Source != "unreadable" || entry.Problem == "" || entry.Preset != "retired" || entry.Effective["empty_answer_counts_tokens"] != true {
		t.Errorf("entry = %+v, want the unreadable row shown over Kimi's built-in filters", entry)
	}
	if unnamed.Source != "unreadable" || unnamed.Preset != "glm_flash" {
		t.Errorf("entry = %+v, want an unreadable row that named no preset shown with the model's built-in preset", unnamed)
	}
}

// Test flow:
//  1. Cap Kimi's output at 8 tokens in the settings, below its floor of 16 and its budget's force-zero threshold of 256.
//  2. GET Kimi's entry.
//  3. Assert it warns about each override: the cap wins over the floor, and thinking is silenced both for requests that name a budget and for those that take the default.
func TestAdminFilterEntryWarnsWhenSettingsLimitsOverrideFilters(t *testing.T) {
	live := newFiltersHarness(t)
	live.swapConfig(func(next *config.Config) {
		next.Limits.ModelLimits = map[string]config.ModelLimits{filtersKimiModel: {DefaultMaxTokens: 8, MaxTokensCap: 8}}
	})

	entry := decodeInto[filterEntryResponse](t, live.request(t, http.MethodGet, filtersRoutePath+filtersKimiModel, "", adminHeaders()))

	wanted := []string{"output_tokens_floor 16 is above max_tokens_cap 8", "names a budget", "names no budget"}
	if len(entry.Warnings) != len(wanted) {
		t.Fatalf("warnings = %q, want %d", entry.Warnings, len(wanted))
	}
	for index, fragment := range wanted {
		if !strings.Contains(entry.Warnings[index], fragment) {
			t.Errorf("warning %d = %q, want it to mention %q", index, entry.Warnings[index], fragment)
		}
	}
	clean := decodeInto[filterEntryResponse](t, live.request(t, http.MethodGet, filtersRoutePath+filtersQwenModel, "", adminHeaders()))
	if len(clean.Warnings) != 0 {
		t.Errorf("Qwen warnings = %q, want none", clean.Warnings)
	}
}

// Test flow:
//  1. Call every model-filter route and method with no key and with a client API key.
//  2. Assert each is 401: only the admin key reaches the filter API.
func TestEveryFilterRouteIsAdminOnly(t *testing.T) {
	live := newFiltersHarness(t)
	calls := []struct{ method, target, body string }{
		{http.MethodGet, filtersListPath, ""},
		{http.MethodGet, filtersPresetPath, ""},
		{http.MethodGet, filtersRoutePath + filtersQwenModel, ""},
		{http.MethodPut, filtersRoutePath + filtersQwenModel, `{"preset":"minimax"}`},
		{http.MethodDelete, filtersRoutePath + filtersQwenModel, ""},
	}
	for _, call := range calls {
		for name, headers := range map[string]map[string]string{"no_key": nil, "client_key": {"Authorization": "Bearer " + clientKey}} {
			if recorder := live.request(t, call.method, call.target, call.body, headers); recorder.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with %s: status %d, want 401", call.method, call.target, name, recorder.Code)
			}
		}
	}
	if len(live.filters.Records()) != 0 {
		t.Error("a non-admin call stored a setting")
	}
}
