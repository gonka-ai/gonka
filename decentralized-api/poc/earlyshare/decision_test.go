package earlyshare

import "testing"

func TestWeightedMedianShare(t *testing.T) {
	tests := []struct {
		name    string
		points  []SharePoint
		want    float64
		wantOK  bool
	}{
		{
			name:   "empty",
			points: nil,
			wantOK: false,
		},
		{
			name:   "all non-positive weight",
			points: []SharePoint{{Share: 0.5, Weight: 0}, {Share: 0.9, Weight: -3}},
			wantOK: false,
		},
		{
			name:   "single point",
			points: []SharePoint{{Share: 0.42, Weight: 10}},
			want:   0.42,
			wantOK: true,
		},
		{
			name: "weighted crossing favors heavy low share",
			// Cumulative: 0.2(w=10) -> 10; 0.8(w=5) -> 15; total=15, half=7.5
			// 2*10=20 >= 15 at first point.
			points: []SharePoint{{Share: 0.8, Weight: 5}, {Share: 0.2, Weight: 10}},
			want:   0.2,
			wantOK: true,
		},
		{
			name: "even split picks lower middle deterministically",
			// shares 0.1,0.9 each weight 5; total=10, half=5; 2*5=10>=10 at 0.1.
			points: []SharePoint{{Share: 0.9, Weight: 5}, {Share: 0.1, Weight: 5}},
			want:   0.1,
			wantOK: true,
		},
		{
			name: "zero-weight point ignored",
			points: []SharePoint{
				{Share: 0.0, Weight: 0},
				{Share: 0.5, Weight: 3},
				{Share: 0.6, Weight: 3},
			},
			// total=6 half=3; 2*3=6>=6 at 0.5
			want:   0.5,
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := WeightedMedianShare(tt.points)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Fatalf("median = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyMissStreak(t *testing.T) {
	const stage = int64(100)
	const cpoc = true
	const poc = false

	t.Run("CPoC pass resets and sets passed_once", func(t *testing.T) {
		out := ApplyMissStreak(GuardState{ConsecutiveMisses: 1}, true, cpoc, stage)
		if out.VoteNo {
			t.Fatal("pass should not vote no")
		}
		if !out.NewState.PassedOnce {
			t.Fatal("CPoC pass should set PassedOnce")
		}
		if out.NewState.ConsecutiveMisses != 0 {
			t.Fatalf("misses = %d, want 0", out.NewState.ConsecutiveMisses)
		}
		if out.NewState.UpdatedStageHeight != stage {
			t.Fatalf("stage = %d, want %d", out.NewState.UpdatedStageHeight, stage)
		}
	})

	t.Run("PoC pass does NOT reset streak or set passed_once", func(t *testing.T) {
		out := ApplyMissStreak(GuardState{PassedOnce: false, ConsecutiveMisses: 1}, true, poc, stage)
		if out.VoteNo {
			t.Fatal("pass should not vote no")
		}
		if out.NewState.PassedOnce {
			t.Fatal("PoC pass must not set PassedOnce")
		}
		if out.NewState.ConsecutiveMisses != 1 {
			t.Fatalf("PoC pass must not reset misses; got %d, want 1", out.NewState.ConsecutiveMisses)
		}
	})

	t.Run("PoC pass does not rescue an established miss streak", func(t *testing.T) {
		// passed_once already true (from a prior CPoC), one grace miss used.
		out := ApplyMissStreak(GuardState{PassedOnce: true, ConsecutiveMisses: 1}, true, poc, stage)
		if out.VoteNo {
			t.Fatal("a passing stage never votes no")
		}
		if out.NewState.ConsecutiveMisses != 1 {
			t.Fatalf("PoC pass must leave misses at 1; got %d", out.NewState.ConsecutiveMisses)
		}
		// The very next failure should then vote no (streak not rescued).
		next := ApplyMissStreak(out.NewState, false, poc, stage+1)
		if !next.VoteNo {
			t.Fatal("failure after an unrescued streak should vote no")
		}
	})

	t.Run("fail before first CPoC pass never penalizes but accrues", func(t *testing.T) {
		out := ApplyMissStreak(GuardState{PassedOnce: false, ConsecutiveMisses: 2}, false, poc, stage)
		if out.VoteNo {
			t.Fatal("must not vote no before first CPoC pass")
		}
		if out.NewState.ConsecutiveMisses != 3 {
			t.Fatalf("misses = %d, want 3 (failures still accrue)", out.NewState.ConsecutiveMisses)
		}
	})

	t.Run("first miss after CPoC pass is grace", func(t *testing.T) {
		out := ApplyMissStreak(GuardState{PassedOnce: true, ConsecutiveMisses: 0}, false, poc, stage)
		if out.VoteNo {
			t.Fatal("first miss should be grace, not vote no")
		}
		if out.NewState.ConsecutiveMisses != 1 {
			t.Fatalf("misses = %d, want 1", out.NewState.ConsecutiveMisses)
		}
	})

	t.Run("second consecutive miss votes no (PoC or CPoC failure)", func(t *testing.T) {
		for _, conf := range []bool{poc, cpoc} {
			out := ApplyMissStreak(GuardState{PassedOnce: true, ConsecutiveMisses: 1}, false, conf, stage)
			if !out.VoteNo {
				t.Fatalf("second consecutive miss should vote no (isConfirmation=%v)", conf)
			}
			if out.NewState.ConsecutiveMisses != 2 {
				t.Fatalf("misses = %d, want 2", out.NewState.ConsecutiveMisses)
			}
		}
	})

	t.Run("CPoC pass clears streak after grace miss", func(t *testing.T) {
		out := ApplyMissStreak(GuardState{PassedOnce: true, ConsecutiveMisses: 1}, true, cpoc, stage)
		if out.VoteNo || out.NewState.ConsecutiveMisses != 0 {
			t.Fatalf("CPoC pass should reset; got vote=%v misses=%d", out.VoteNo, out.NewState.ConsecutiveMisses)
		}
	})
}
