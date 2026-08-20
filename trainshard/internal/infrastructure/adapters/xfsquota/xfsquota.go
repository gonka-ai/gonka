package xfsquota

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

const blockBytes = 1024

type Config struct {
	Root string

	Mount string

	Tool string

	UID int
	GID int

	Timeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.Root == "" {
		c.Root = "/var/lib/trainshardd/volumes"
	}
	if c.Mount == "" {
		c.Mount = c.Root
	}
	if c.Tool == "" {
		c.Tool = "xfs_quota"
	}
	if c.UID == 0 {
		c.UID = 1000
	}
	if c.GID == 0 {
		c.GID = 1000
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	return c
}

type Volumes struct {
	cfg Config
	log *slog.Logger
}

func New(cfg Config, log *slog.Logger) *Volumes {
	return &Volumes{cfg: cfg.withDefaults(), log: log}
}

func (v *Volumes) Ensure(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, quotaBytes int64) error {
	path := v.path(shardID, node)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := os.Chown(path, v.cfg.UID, v.cfg.GID); err != nil {
		return err
	}

	project := projectID(shardID, node)
	if err := v.quota(ctx, fmt.Sprintf("project -s -p %s %d", path, project)); err != nil {
		return err
	}

	if err := v.quota(ctx, fmt.Sprintf("limit -p bhard=%dk %d", blocks(quotaBytes), project)); err != nil {
		return err
	}

	v.log.Info("volume ready", "node_id", node.NodeID, "quota_bytes", quotaBytes)
	return nil
}

func (v *Volumes) Usage(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (int64, int64, bool, error) {
	if _, err := os.Stat(v.path(shardID, node)); errors.Is(err, fs.ErrNotExist) {
		return 0, 0, false, nil
	} else if err != nil {
		return 0, 0, false, err
	}

	project := projectID(shardID, node)
	out, err := v.report(ctx, fmt.Sprintf("quota -p -N -n -b %d", project))
	if err != nil {
		return 0, 0, false, err
	}

	used, quota, err := parseQuota(out, project)
	return used, quota, true, err
}

func (v *Volumes) Wipe(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error {
	if err := os.RemoveAll(v.path(shardID, node)); err != nil {
		return err
	}
	if err := v.quota(ctx, fmt.Sprintf("limit -p bhard=0k %d", projectID(shardID, node))); err != nil {
		return err
	}

	v.log.Info("wiped volume", "node_id", node.NodeID)
	return nil
}

func (v *Volumes) Archive(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, out io.Writer) error {
	root := v.path(shardID, node)
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return run.ErrVolumeMissing
	}

	_, quota, _, err := v.Usage(ctx, shardID, node)
	if err != nil {
		return err
	}
	if quota <= 0 {
		return run.ErrQuotaUnknown
	}

	archive := tar.NewWriter(&capped{out: out, left: quota})
	defer archive.Close()

	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.Type().IsRegular() && !entry.IsDir() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		header.Name = filepath.ToSlash(name)

		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		return copyFile(archive, path)
	})
}

// capped stops the archive at the run's own disk quota, so a volume that outgrew its
// limit cannot hand the coordinator more than the run was allowed to hold
type capped struct {
	out  io.Writer
	left int64
}

func (c *capped) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		return 0, run.ErrArtifactsTooBig
	}
	c.left -= int64(len(p))
	return c.out.Write(p)
}

func copyFile(archive io.Writer, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(archive, file)
	return err
}

func (v *Volumes) Shards(_ context.Context, node vo.NodeRef) ([]vo.ShardID, error) {
	entries, err := os.ReadDir(v.cfg.Root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	shards := make([]vo.ShardID, 0, len(entries))
	for _, entry := range entries {
		shardID, err := vo.ParseShardID(entry.Name())
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(v.cfg.Root, entry.Name(), string(node.NodeID))); err != nil {
			continue
		}
		shards = append(shards, shardID)
	}
	return shards, nil
}

func (v *Volumes) FreeDiskBytes(context.Context) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(v.cfg.Root, &stat); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", v.cfg.Root, err)
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

func (v *Volumes) path(shardID vo.ShardID, node vo.NodeRef) string {
	return filepath.Join(v.cfg.Root, shardID.String(), string(node.NodeID))
}

func (v *Volumes) quota(ctx context.Context, command string) error {
	_, err := v.report(ctx, command)
	return err
}

func (v *Volumes) report(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, v.cfg.Timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, v.cfg.Tool, "-x", "-c", command, v.cfg.Mount).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %q: %w: %s", v.cfg.Tool, command, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func projectID(shardID vo.ShardID, node vo.NodeRef) uint32 {
	sum := fnv.New32a()
	fmt.Fprintf(sum, "%s/%s", shardID, node.NodeID)
	return sum.Sum32()%(1<<24) + 1
}

func parseQuota(out string, project uint32) (used int64, quota int64, err error) {
	for line := range strings.Lines(out) {
		fields := strings.Fields(line)
		if len(fields) < 4 || strings.TrimPrefix(fields[0], "#") != strconv.FormatUint(uint64(project), 10) {
			continue
		}

		used, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("quota of project %d: %q", project, line)
		}
		hard, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("quota of project %d: %q", project, line)
		}
		return used * blockBytes, hard * blockBytes, nil
	}
	return 0, 0, nil
}

func blocks(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	return (bytes + blockBytes - 1) / blockBytes
}
