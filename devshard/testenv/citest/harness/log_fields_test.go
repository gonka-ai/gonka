package harness

import "testing"

func TestLogsContainFieldsJSONAndText(t *testing.T) {
	jsonLine := `{"msg":"proxy_request_completed","aggregate_spilled":true,"inference_id":8,"mode":"anchor","delta":-20,"request":"req-1","stage":"gateway_runtime_selected","escrow":"2"}`
	textLine := `request=req-1 stage=gateway_runtime_selected escrow=2 aggregate_spilled=true inference_id=8 mode=anchor delta=-20`

	for _, logs := range []string{jsonLine, textLine} {
		if !LogsContainFields(logs, LogBoolField("aggregate_spilled", true)) {
			t.Fatalf("bool spill missing in %s", logs)
		}
		if !LogsContainFields(logs, LogNumberField("inference_id", "8")) {
			t.Fatalf("inference id missing in %s", logs)
		}
		if !LogsContainFields(logs, LogStringField("mode", "anchor")) {
			t.Fatalf("mode missing in %s", logs)
		}
		if !LogsContainFields(logs, LogNumberPrefixField("delta", "-")) {
			t.Fatalf("negative delta missing in %s", logs)
		}
		if !LogsContainFields(logs,
			LogStringField("request", "req-1"),
			LogStringField("stage", "gateway_runtime_selected"),
			LogStringField("escrow", "2"),
		) {
			t.Fatalf("picker fields not on one line in %s", logs)
		}
	}

	quoted := `{"aggregate_spilled":"true","inference_id":"8","delta":"-20","mode":"anchor_extra"}`
	if LogsContainFields(quoted, LogBoolField("aggregate_spilled", true)) {
		t.Fatal("quoted bool must not count as a typed bool")
	}
	if LogsContainFields(quoted, LogNumberField("inference_id", "8")) {
		t.Fatal("quoted number must not count as a typed number")
	}
	if LogsContainFields(quoted, LogNumberPrefixField("delta", "-")) {
		t.Fatal("quoted negative must not count as a typed number")
	}
	if LogsContainFields(quoted, LogStringField("mode", "anchor")) {
		t.Fatal("anchor_extra must not match mode anchor")
	}

	wider := `{"inference_id":80,"aggregate_spilled":false}`
	if LogsContainFields(wider, LogNumberField("inference_id", "8")) {
		t.Fatal("inference 80 must not match 8")
	}
	if LogsContainFields(wider, LogBoolField("aggregate_spilled", true)) {
		t.Fatal("false must not match true")
	}

	split := "{\"request\":\"req-1\"}\n{\"stage\":\"gateway_runtime_selected\",\"escrow\":\"2\"}"
	if LogsContainFields(split,
		LogStringField("request", "req-1"),
		LogStringField("stage", "gateway_runtime_selected"),
		LogStringField("escrow", "2"),
	) {
		t.Fatal("fields on different lines are not one selection")
	}
}
