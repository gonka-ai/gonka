package filtercore

import "testing"

func TestMatchesModel(t *testing.T) {
	cases := []struct {
		name        string
		routed      string
		models      []string
		want        bool
	}{
		{name: "empty list", routed: "model-a", models: nil, want: false},
		{name: "single match", routed: "model-a", models: []string{"model-a"}, want: true},
		{name: "no match", routed: "model-a", models: []string{"model-b"}, want: false},
		{name: "match in multi", routed: "model-b", models: []string{"model-a", "model-b", "model-c"}, want: true},
		{name: "case sensitive", routed: "Model-A", models: []string{"model-a"}, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MatchesModel(c.routed, c.models); got != c.want {
				t.Fatalf("MatchesModel(%q, %v) = %v, want %v", c.routed, c.models, got, c.want)
			}
		})
	}
}
