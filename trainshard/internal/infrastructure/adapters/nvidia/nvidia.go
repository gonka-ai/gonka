package nvidia

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/infrastructure/adapters/gpuprofile"
)

type Config struct {
	SMI string

	Timeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.SMI == "" {
		c.SMI = "nvidia-smi"
	}
	if c.Timeout == 0 {
		c.Timeout = 15 * time.Second
	}
	return c
}

type Runs interface {
	ContainerID(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (id string, present bool, err error)
}

type GPUs struct {
	cfg  Config
	runs Runs
	log  *slog.Logger
}

func New(cfg Config, runs Runs, log *slog.Logger) *GPUs {
	return &GPUs{cfg: cfg.withDefaults(), runs: runs, log: log}
}

func (g *GPUs) Inventory(ctx context.Context, _ vo.NodeRef) (vo.GPUInventory, error) {
	lines, err := g.query(ctx, "--query-gpu=name,memory.total")
	if err != nil {
		return vo.GPUInventory{}, err
	}
	cards, err := parseCards(lines)
	if err != nil {
		return vo.GPUInventory{}, err
	}
	return gpuprofile.FromCards(cards), nil
}

func parseCards(lines []string) ([]gpuprofile.Card, error) {
	cards := make([]gpuprofile.Card, 0, len(lines))
	for _, line := range lines {
		name, memory, found := strings.Cut(line, ",")
		if !found {
			return nil, fmt.Errorf("nvidia-smi line %q has no memory column", line)
		}
		mib, err := strconv.Atoi(strings.TrimSpace(memory))
		if err != nil {
			return nil, fmt.Errorf("nvidia-smi line %q: %w", line, err)
		}
		cards = append(cards, gpuprofile.Card{Name: strings.TrimSpace(name), MemoryMiB: mib})
	}
	return cards, nil
}

func (g *GPUs) InUse(ctx context.Context, _ vo.NodeRef) (int, error) {
	apps, err := g.computeApps(ctx)
	if err != nil {
		return 0, err
	}

	cards := make(map[string]struct{}, len(apps))
	for _, app := range apps {
		cards[app.uuid] = struct{}{}
	}
	return len(cards), nil
}

func (g *GPUs) ForeignWork(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (bool, error) {
	apps, err := g.computeApps(ctx)
	if err != nil {
		return false, err
	}
	if len(apps) == 0 {
		return false, nil
	}

	container, present, err := g.runs.ContainerID(ctx, shardID, node)
	if err != nil {
		return false, err
	}

	for _, app := range apps {
		if !present || !g.belongsTo(app.pid, container) {
			return true, nil
		}
	}
	return false, nil
}

func (g *GPUs) TrainingProcesses(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (bool, error) {
	own, err := g.own(ctx, shardID, node)
	return len(own) > 0, err
}

func (g *GPUs) KillTraining(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error {
	own, err := g.own(ctx, shardID, node)
	if err != nil {
		return err
	}

	for _, pid := range own {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !isGone(err) {
			return fmt.Errorf("kill %d on %s: %w", pid, node, err)
		}
		g.log.Warn("killed leftover gpu process", "shard_id", shardID.String(), "node_id", node.NodeID, "pid", pid)
	}
	return nil
}

func (g *GPUs) own(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) ([]int, error) {
	apps, err := g.computeApps(ctx)
	if err != nil || len(apps) == 0 {
		return nil, err
	}
	container, present, err := g.runs.ContainerID(ctx, shardID, node)
	if err != nil || !present {
		return nil, err
	}

	own := make([]int, 0, len(apps))
	for _, app := range apps {
		if g.belongsTo(app.pid, container) {
			own = append(own, app.pid)
		}
	}
	return own, nil
}

type computeApp struct {
	pid  int
	uuid string
}

func (g *GPUs) computeApps(ctx context.Context) ([]computeApp, error) {
	lines, err := g.query(ctx, "--query-compute-apps=pid,gpu_uuid")
	if err != nil {
		return nil, err
	}
	return parseComputeApps(lines), nil
}

func parseComputeApps(lines []string) []computeApp {
	apps := make([]computeApp, 0, len(lines))
	for _, line := range lines {
		raw, uuid, found := strings.Cut(line, ",")
		uuid = strings.TrimSpace(uuid)
		if !found || !namesACard(uuid) {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || pid <= 0 {
			pid = unknownPID
		}
		apps = append(apps, computeApp{pid: pid, uuid: uuid})
	}
	return apps
}

const unknownPID = -1

func namesACard(uuid string) bool {
	return strings.HasPrefix(uuid, "GPU-") || strings.HasPrefix(uuid, "MIG-")
}

func (g *GPUs) query(ctx context.Context, what string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, g.cfg.Timeout)
	defer cancel()

	command := exec.CommandContext(ctx, g.cfg.SMI, what, "--format=csv,noheader,nounits")
	command.WaitDelay = time.Second
	out, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s %s: no answer within %s: %w", g.cfg.SMI, what, g.cfg.Timeout, err)
		}
		return nil, fmt.Errorf("%s %s: %w", g.cfg.SMI, what, failed(err, out))
	}
	return scan(out)
}

func failed(err error, out []byte) error {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return err
	}
	said := strings.TrimSpace(strings.TrimSpace(string(exit.Stderr)) + " " + strings.TrimSpace(string(out)))
	if said == "" {
		said = "no output"
	}
	return fmt.Errorf("%w: %s; if the cards answer on the host, trainshardd lost access to them and needs a restart", err, said)
}

func scan(out []byte) ([]string, error) {
	lines := make([]string, 0, 8)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}

// what belongs to the run gets killed, and kill(-1) is everyone
func (g *GPUs) belongsTo(pid int, container string) bool {
	if pid <= 0 || container == "" {
		return false
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), container)
}

func isGone(err error) bool {
	return errors.Is(err, syscall.ESRCH)
}
