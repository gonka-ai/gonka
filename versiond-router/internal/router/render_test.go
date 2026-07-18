package router

import (
	"strings"
	"testing"
)

const testTemplate = `${HA_UPSTREAM_SERVERS}
${LEGACY_UPSTREAM_SERVERS}
${VERSIOND_BACKEND_MAP}
${DEVSHARD_HA_HEADER_MAP}
`

func TestRenderMarksNonActiveHostDown(t *testing.T) {
	state, err := NewState([]string{"versiond-1", "versiond-2"}, 8080, "versiond-1", []string{"v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := state.Apply(Transition{
		Action: ActionDrain, Host: "versiond-2", OperationID: "drain-2",
	}); err != nil {
		t.Fatal(err)
	}
	config, err := Render([]byte(testTemplate), state)
	if err != nil {
		t.Fatal(err)
	}
	text := string(config)
	if !strings.Contains(text, "server versiond-2:8080 resolve down;") {
		t.Fatalf("rendered config does not mark draining host down:\n%s", text)
	}
	if !strings.Contains(text, "v1 versiond_legacy;") {
		t.Fatalf("rendered config lost non-HA mapping:\n%s", text)
	}
}
