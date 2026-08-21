package xfsquota

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

const shard = vo.ShardID(42)

func ref(id string) vo.NodeRef {
	return vo.NodeRef{Participant: "gonka1abc", NodeID: vo.NodeID(id)}
}

func volumes(t *testing.T) *Volumes {
	t.Helper()

	return New(Config{Root: t.TempDir()}, slog.New(slog.DiscardHandler))
}

func TestParseQuota(t *testing.T) {
	// arrange
	report := "#0                  12          0          0     00 [--------]\n" +
		"#101               512          0       1024     00 [--------]\n" +
		"#202              2048          0       8192     00 [--------]\n"

	t.Run("blocks become bytes", func(t *testing.T) {
		// act
		used, quota, err := parseQuota(report, 202)

		// assert
		if err != nil {
			t.Fatal(err)
		}
		if used != 2048*blockBytes || quota != 8192*blockBytes {
			t.Fatalf("used = %d, quota = %d", used, quota)
		}
	})

	t.Run("a project with no line is not an error", func(t *testing.T) {
		// act
		used, quota, err := parseQuota(report, 999)

		// assert
		if err != nil || used != 0 || quota != 0 {
			t.Fatalf("used = %d, quota = %d, err = %v", used, quota, err)
		}
	})

	t.Run("the project id is matched whole", func(t *testing.T) {
		// act
		used, _, err := parseQuota("#1010  4  0  8  00\n", 101)

		// assert
		if err != nil {
			t.Fatal(err)
		}
		if used != 0 {
			t.Fatal("project 101 matched the line of project 1010")
		}
	})

	t.Run("lines that are not a quota row are skipped", func(t *testing.T) {
		// act
		used, quota, err := parseQuota("Project ID   Used\n#202  2048  0  8192  00\n", 202)

		// assert
		if err != nil {
			t.Fatal(err)
		}
		if used != 2048*blockBytes || quota != 8192*blockBytes {
			t.Fatalf("used = %d, quota = %d", used, quota)
		}
	})

	t.Run("an unreadable number is reported rather than read as zero", func(t *testing.T) {
		// act
		_, _, err := parseQuota("#202  lots  0  8192  00\n", 202)

		// assert
		if err == nil {
			t.Fatal("want an error: a zero here would look like an empty volume")
		}
	})
}

func TestBlocksRoundUp(t *testing.T) {
	// arrange
	cases := []struct {
		bytes int64
		want  int64
	}{
		{bytes: 0, want: 0},
		{bytes: -1, want: 0},
		{bytes: 1, want: 1},
		{bytes: blockBytes, want: 1},
		{bytes: blockBytes + 1, want: 2},
		{bytes: 10 << 30, want: 10 << 20},
	}

	for _, tc := range cases {
		// act
		got := blocks(tc.bytes)

		// assert
		if got != tc.want {
			t.Fatalf("blocks(%d) = %d, want %d", tc.bytes, got, tc.want)
		}
	}
}

// The project id is how a quota is found again after a restart, so it must be stable and never 0,
// which xfs reads as "no project"
func TestProjectID(t *testing.T) {
	// act
	first := projectID(shard, ref("a"))
	again := projectID(shard, ref("a"))

	// assert
	if first != again {
		t.Fatal("project id is not stable across calls")
	}
	if first == 0 || first > 1<<24 {
		t.Fatalf("project id %d is out of range", first)
	}
	if projectID(shard, ref("b")) == first {
		t.Fatal("two nodes share a project id, so they would share a quota")
	}
	if projectID(vo.ShardID(43), ref("a")) == first {
		t.Fatal("two shards share a project id")
	}
}

func TestCappedStopsAtTheQuota(t *testing.T) {
	// arrange
	var out bytes.Buffer
	writer := &capped{out: &out, left: 10}

	// act
	first, err := writer.Write([]byte("12345"))

	// assert
	if err != nil || first != 5 {
		t.Fatalf("wrote %d bytes: %v", first, err)
	}

	// act
	_, err = writer.Write([]byte("123456"))

	// assert
	if !errors.Is(err, run.ErrArtifactsTooBig) {
		t.Fatalf("err = %v, want the artifacts limit", err)
	}
	if out.String() != "12345" {
		t.Fatalf("buffer holds %q, want the refused write left out", out.String())
	}
}

func TestShardsNeedTheNodesOwnVolume(t *testing.T) {
	// arrange
	v := volumes(t)
	for _, path := range []string{"7/a", "9/a", "11/b", "notashard/a"} {
		if err := os.MkdirAll(filepath.Join(v.cfg.Root, path), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// act
	held, err := v.Shards(context.Background(), ref("a"))
	if err != nil {
		t.Fatal(err)
	}

	// assert
	slices.Sort(held)
	if !slices.Equal(held, []vo.ShardID{7, 9}) {
		t.Fatalf("shards = %v, want only the ones holding a volume for this node", held)
	}
}

func TestShardsWithoutARootIsEmpty(t *testing.T) {
	// arrange
	v := New(Config{Root: filepath.Join(t.TempDir(), "never-created")}, slog.New(slog.DiscardHandler))

	// act
	held, err := v.Shards(context.Background(), ref("a"))

	// assert
	if err != nil {
		t.Fatalf("a missing root is a fresh host, not a failure: %v", err)
	}
	if len(held) != 0 {
		t.Fatalf("shards = %v", held)
	}
}

func TestUsageOfAMissingVolume(t *testing.T) {
	// arrange
	v := volumes(t)

	// act
	used, quota, present, err := v.Usage(context.Background(), shard, ref("a"))

	// assert
	if err != nil {
		t.Fatalf("a volume that was never created must not reach xfs_quota: %v", err)
	}
	if present || used != 0 || quota != 0 {
		t.Fatalf("present = %v, used = %d, quota = %d", present, used, quota)
	}
}

func TestArchiveOfAMissingVolume(t *testing.T) {
	// arrange
	v := volumes(t)

	// act
	err := v.Archive(context.Background(), shard, ref("a"), &bytes.Buffer{})

	// assert
	if !errors.Is(err, run.ErrVolumeMissing) {
		t.Fatalf("err = %v, want the missing volume error", err)
	}
}

func TestConfigDefaults(t *testing.T) {
	// act
	cfg := Config{Root: "/data/volumes"}.withDefaults()

	// assert
	if cfg.Mount != "/data/volumes" {
		t.Fatalf("mount = %q, want it to fall back to the root", cfg.Mount)
	}
	if cfg.Tool != "xfs_quota" || cfg.UID != 1000 || cfg.GID != 1000 {
		t.Fatalf("got %+v", cfg)
	}

	// act
	kept := Config{Root: "/data", Mount: "/mnt/xfs"}.withDefaults()

	// assert
	if kept.Mount != "/mnt/xfs" {
		t.Fatalf("mount = %q, want the configured value kept", kept.Mount)
	}
}
