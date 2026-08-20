package vo_test

import (
	"errors"
	"strings"
	"testing"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

const participant = "gonka1alice"

func TestParseNodeRefKeepsANodeIDToOneNameOnDisk(t *testing.T) {
	cases := map[string]struct {
		id string
		ok bool
	}{
		"a plain name":         {id: "node1", ok: true},
		"dashes and dots":      {id: "gpu-node.1_a", ok: true},
		"surrounding spaces":   {id: "  node1 ", ok: true},
		"empty":                {id: ""},
		"longer than we allow": {id: strings.Repeat("n", 65)},
		"a path out":           {id: "../../etc/cron.d"},
		"a separator":          {id: "node1/sub"},
		"the current dir":      {id: "."},
		"the parent dir":       {id: ".."},
		"a glob":               {id: "node*"},
		"a space inside":       {id: "node 1"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {

			got, err := vo.ParseNodeRef(participant, tc.id)

			if tc.ok && (err != nil || got.NodeID != vo.NodeID(strings.TrimSpace(tc.id))) {
				t.Fatalf("got %+v %v, want the id kept as it is", got, err)
			}
			if !tc.ok && !errors.Is(err, shared.ErrValidation) {
				t.Fatalf("got %v, want a validation error", err)
			}
		})
	}
}
