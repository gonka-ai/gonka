package process

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"versioned/internal/config"
	"versioned/internal/download"
	"versioned/internal/health"
	"versioned/internal/oracle"
)

const (
	statusStarting = "starting"
	statusRunning  = "running"
	statusStopped  = "stopped"
)

type child struct {
	version oracle.Version
	port    int
	cancel  context.CancelFunc
	done    chan struct{} // closed when runChild exits
	status  string
}

type Manager struct {
	cfg           config.Config
	processes     map[string]*child
	downloading   map[string]struct{}
	assignedPorts map[string]int // version name -> assigned port (persists for manager lifetime)
	nextPort      int
	mu            sync.Mutex
	routes        atomic.Value // map[string]string
}

func NewManager(cfg config.Config) *Manager {
	m := &Manager{
		cfg:           cfg,
		processes:     make(map[string]*child),
		downloading:   make(map[string]struct{}),
		assignedPorts: make(map[string]int),
		nextPort:      cfg.BasePort,
	}
	m.routes.Store(map[string]string{})
	return m
}

// assignPort returns a stable port for the given version name.
// Once assigned, the same name always gets the same port.
// Must be called with m.mu held.
func (m *Manager) assignPort(name string) int {
	if port, ok := m.assignedPorts[name]; ok {
		return port
	}
	port := m.nextPort
	m.nextPort++
	m.assignedPorts[name] = port
	return port
}

func (m *Manager) RouteTable() *atomic.Value {
	return &m.routes
}

func (m *Manager) Status() []health.StatusEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]health.StatusEntry, 0, len(m.processes))
	for _, c := range m.processes {
		out = append(out, health.StatusEntry{
			Name:   c.version.Name,
			Port:   c.port,
			Status: c.status,
		})
	}
	return out
}

// Reconcile compares desired state against local state and converges.
// The desired sha256 is the archive identity from the oracle. Downloaded
// versions also record local install metadata so we can distinguish archive
// identity from the extracted executable bytes on disk.
func (m *Manager) Reconcile(ctx context.Context, desired []oracle.Version) error {
	// Step 0: build desired set, injecting forced versions.
	desiredSet := make(map[string]oracle.Version, len(desired))
	for _, v := range desired {
		desiredSet[v.Name] = v
	}
	for _, name := range m.cfg.ForceVersions {
		if _, exists := desiredSet[name]; exists {
			continue
		}
		if _, hasOverride := m.cfg.Overrides[name]; !hasOverride {
			slog.Warn("forced version skipped: no override configured", "version", name)
			continue
		}
		desiredSet[name] = oracle.Version{Name: name}
	}
	slog.Info(
		"reconcile desired versions resolved",
		"oracle_versions", versionNames(desired),
		"force_versions", m.cfg.ForceVersions,
		"desired_versions", versionNamesMap(desiredSet),
	)

	// Phase A (lock): snapshot state, identify overrides.
	m.mu.Lock()
	type overrideAction struct {
		version     oracle.Version
		overrideSrc string
		binPath     string
	}
	var overrides []overrideAction

	// Snapshot which versions are running and which are downloading.
	type versionSnapshot struct {
		version       oracle.Version
		versionDir    string
		binPath       string
		isRunning     bool
		isDownloading bool
		child         *child
	}
	var snapshots []versionSnapshot

	for _, v := range desiredSet {
		versionDir := filepath.Join(m.cfg.BinDir, v.Name)
		binPath := filepath.Join(versionDir, m.cfg.BinaryName)
		if overrideSrc, isOverride := m.cfg.Overrides[v.Name]; isOverride {
			overrides = append(overrides, overrideAction{v, overrideSrc, binPath})
			continue
		}
		running, isRunning := m.processes[v.Name]
		_, isDownloading := m.downloading[v.Name]
		snapshots = append(snapshots, versionSnapshot{
			version:       v,
			versionDir:    versionDir,
			binPath:       binPath,
			isRunning:     isRunning,
			isDownloading: isDownloading,
			child:         running,
		})
	}
	m.mu.Unlock()

	// Phase B (no lock): resolve hashes, do disk I/O for overrides and hash checks.
	for _, o := range overrides {
		m.reconcileOverride(ctx, o.version, o.overrideSrc, o.binPath)
	}

	var toDownload []versionAction
	var toSwap []versionAction
	var toStart []oracle.Version

	for _, snap := range snapshots {
		if snap.isDownloading {
			continue
		}

		desiredHash, err := snap.version.ResolvedSHA256()
		if err != nil {
			slog.Error("cannot resolve sha256, skipping", "version", snap.version.Name, "error", err)
			continue
		}

		if snap.isRunning {
			matches, metadata, diskBinaryHash, stateErr := installedVersionMatches(snap.versionDir, snap.binPath, desiredHash)
			if stateErr == nil && matches {
				continue
			}
			logInstalledVersionMismatch(
				"running version",
				snap.version.Name,
				desiredHash,
				metadata,
				diskBinaryHash,
				stateErr,
			)
			toSwap = append(toSwap, versionAction{version: snap.version, sha256: desiredHash, child: snap.child})
			continue
		}

		// Not running.
		matches, metadata, diskBinaryHash, stateErr := installedVersionMatches(snap.versionDir, snap.binPath, desiredHash)
		if stateErr == nil && matches {
			toStart = append(toStart, snap.version)
			continue
		}
		if stateErr == nil || !errors.Is(stateErr, os.ErrNotExist) {
			logInstalledVersionMismatch(
				"cached version",
				snap.version.Name,
				desiredHash,
				metadata,
				diskBinaryHash,
				stateErr,
			)
		}
		cleanupInstalledVersionState(snap.versionDir, snap.binPath)
		toDownload = append(toDownload, versionAction{version: snap.version, sha256: desiredHash})
	}

	// Phase C (lock): apply decisions -- start ready children, mark downloads, stop removed.
	m.mu.Lock()
	for _, v := range toStart {
		if _, already := m.processes[v.Name]; already {
			continue // another reconcile started it
		}
		m.startChild(ctx, v)
	}
	for _, a := range toDownload {
		if _, already := m.downloading[a.version.Name]; already {
			continue
		}
		m.downloading[a.version.Name] = struct{}{}
	}
	for _, a := range toSwap {
		if _, already := m.downloading[a.version.Name]; already {
			continue
		}
		m.downloading[a.version.Name] = struct{}{}
	}

	var toStop []*child
	for name, c := range m.processes {
		if _, wanted := desiredSet[name]; !wanted {
			toStop = append(toStop, c)
			delete(m.processes, name)
		}
	}

	changed := len(toDownload) > 0 || len(toSwap) > 0 || len(toStop) > 0 || len(toStart) > 0
	if changed {
		m.rebuildRoutes()
	}
	m.mu.Unlock()

	// Downloads outside the lock (can be slow).
	for _, a := range toDownload {
		if err := m.downloadAndStart(ctx, a.version, a.sha256); err != nil {
			slog.Error("download failed, skipping", "version", a.version.Name, "error", err)
		}
	}

	// Zero-downtime swaps -- download THEN stop old process.
	for _, a := range toSwap {
		if err := m.downloadAndSwap(ctx, a.version, a.sha256, a.child); err != nil {
			slog.Error("swap failed, keeping old version", "version", a.version.Name, "error", err)
		}
	}

	// Stop removed versions outside the lock.
	for _, c := range toStop {
		slog.Info("stopping removed version", "version", c.version.Name)
		c.cancel()
	}
	for _, c := range toStop {
		waitForChild(c, 5*time.Second)
	}

	return nil
}

type versionAction struct {
	version oracle.Version
	sha256  string // pre-resolved hash, avoids double resolution in downloadBinary
	child   *child // non-nil for swap actions
}

// reconcileOverride handles a version with a local override binary.
// Does disk I/O outside the lock, then takes the lock to update state.
func (m *Manager) reconcileOverride(ctx context.Context, v oracle.Version, overrideSrc, binPath string) {
	if stat, statErr := os.Stat(overrideSrc); statErr != nil {
		slog.Error(
			"override path missing or unreadable",
			"version", v.Name,
			"path", overrideSrc,
			"env_key", fmt.Sprintf("VERSIOND_OVERRIDE_%s", strings.ReplaceAll(v.Name, ".", "_")),
			"error", statErr,
		)
		return
	} else if stat.IsDir() {
		slog.Error(
			"override path points to directory, expected file",
			"version", v.Name,
			"path", overrideSrc,
			"env_key", fmt.Sprintf("VERSIOND_OVERRIDE_%s", strings.ReplaceAll(v.Name, ".", "_")),
		)
		return
	}

	srcHash, err := download.HashFile(overrideSrc)
	if err != nil {
		slog.Error("override source unreadable", "version", v.Name, "path", overrideSrc, "error", err)
		return
	}

	// Check if already running the same binary (lock for snapshot only).
	m.mu.Lock()
	existing, isRunning := m.processes[v.Name]
	m.mu.Unlock()

	if isRunning {
		diskHash, hashErr := download.HashFile(binPath)
		if hashErr == nil && diskHash == srcHash {
			return // already running the same override binary
		}
		// Override source changed: stop old, copy new, start.
		slog.Info("override binary changed, restarting", "version", v.Name)
		existing.cancel()
		waitForChild(existing, 5*time.Second)
	}

	// Disk I/O outside the lock.
	binDir := filepath.Join(m.cfg.BinDir, v.Name)
	if err := os.MkdirAll(binDir, 0755); err != nil {
		slog.Error("override mkdir failed", "version", v.Name, "error", err)
		return
	}

	if err := atomicCopy(overrideSrc, binPath); err != nil {
		slog.Error("override copy failed", "version", v.Name, "error", err)
		return
	}

	slog.Info("using override binary", "version", v.Name, "path", overrideSrc)

	m.mu.Lock()
	// Verify the process is still the one we captured before deleting.
	// A concurrent reconcile could have replaced it.
	if isRunning {
		if current, ok := m.processes[v.Name]; ok && current == existing {
			delete(m.processes, v.Name)
		}
	}
	m.startChild(ctx, v)
	m.rebuildRoutes()
	m.mu.Unlock()
}

func versionNames(vs []oracle.Version) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Name)
	}
	return out
}

func versionNamesMap(vs map[string]oracle.Version) []string {
	out := make([]string, 0, len(vs))
	for name := range vs {
		out = append(out, name)
	}
	return out
}

// atomicCopy copies src to dst via a temp file + rename.
func atomicCopy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return download.AtomicWriteFile(filepath.Dir(dst), filepath.Base(dst), in)
}

// downloadAndStart downloads the binary using the pre-resolved hash, then starts the child.
func (m *Manager) downloadAndStart(ctx context.Context, v oracle.Version, sha string) error {
	dlErr := m.downloadBinary(ctx, v, sha)

	m.mu.Lock()
	delete(m.downloading, v.Name)
	if dlErr == nil && ctx.Err() == nil {
		m.startChild(ctx, v)
	}
	m.mu.Unlock()
	return dlErr
}

// downloadAndSwap downloads the new binary, then atomically replaces the old one.
// The old process is stopped only after the new binary is on disk.
func (m *Manager) downloadAndSwap(ctx context.Context, v oracle.Version, sha string, old *child) error {
	dlErr := m.downloadBinary(ctx, v, sha)
	if dlErr != nil || ctx.Err() != nil {
		m.mu.Lock()
		delete(m.downloading, v.Name)
		m.mu.Unlock()
		if dlErr != nil {
			return dlErr
		}
		return ctx.Err()
	}

	// Stop old process after new binary is on disk.
	slog.Info("stopping old process for swap", "version", v.Name)
	old.cancel()
	waitForChild(old, 5*time.Second)

	// Single lock section: clear downloading, remove old, start new.
	m.mu.Lock()
	delete(m.downloading, v.Name)
	delete(m.processes, v.Name)
	m.startChild(ctx, v)
	m.mu.Unlock()

	return nil
}

func (m *Manager) downloadBinary(ctx context.Context, v oracle.Version, sha string) error {
	binDir := filepath.Join(m.cfg.BinDir, v.Name)
	if err := download.Download(ctx, v.Binary, sha, binDir, m.cfg.BinaryName); err != nil {
		return err
	}
	slog.Info("downloaded binary", "version", v.Name)
	return nil
}

func installedVersionMatches(versionDir, binPath, desiredArchiveHash string) (bool, download.InstallMetadata, string, error) {
	metadata, err := download.ReadInstallMetadata(versionDir)
	if err != nil {
		return false, download.InstallMetadata{}, "", err
	}

	diskBinaryHash, err := download.HashFile(binPath)
	if err != nil {
		return false, metadata, "", err
	}

	if !strings.EqualFold(metadata.ArchiveSHA256, desiredArchiveHash) {
		return false, metadata, diskBinaryHash, nil
	}
	if !strings.EqualFold(metadata.BinarySHA256, diskBinaryHash) {
		return false, metadata, diskBinaryHash, nil
	}
	return true, metadata, diskBinaryHash, nil
}

func cleanupInstalledVersionState(versionDir, binPath string) {
	_ = os.Remove(binPath)
	_ = os.Remove(filepath.Join(versionDir, download.InstallMetadataFilename))
}

func logInstalledVersionMismatch(scope, versionName, desiredArchiveHash string, metadata download.InstallMetadata, diskBinaryHash string, stateErr error) {
	if stateErr != nil {
		slog.Info("installed version state unreadable, scheduling download",
			"scope", scope,
			"version", versionName,
			"error", stateErr)
		return
	}
	if !strings.EqualFold(metadata.ArchiveSHA256, desiredArchiveHash) {
		slog.Info("installed archive hash mismatch, scheduling download",
			"scope", scope,
			"version", versionName,
			"installed_archive", metadata.ArchiveSHA256,
			"desired_archive", desiredArchiveHash)
		return
	}
	slog.Info("installed binary hash mismatch, scheduling download",
		"scope", scope,
		"version", versionName,
		"recorded_binary", metadata.BinarySHA256,
		"disk_binary", diskBinaryHash)
}

// startChild must be called with m.mu held.
func (m *Manager) startChild(ctx context.Context, v oracle.Version) {
	childCtx, childCancel := context.WithCancel(ctx)
	c := &child{
		version: v,
		port:    m.assignPort(v.Name),
		cancel:  childCancel,
		done:    make(chan struct{}),
		status:  statusStarting,
	}
	m.processes[v.Name] = c
	go m.runChild(childCtx, c)
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	children := make([]*child, 0, len(m.processes))
	for _, c := range m.processes {
		children = append(children, c)
		c.cancel()
	}
	m.processes = make(map[string]*child)
	m.downloading = make(map[string]struct{})
	m.routes.Store(map[string]string{})
	m.mu.Unlock()

	for _, c := range children {
		slog.Info("shutting down", "version", c.version.Name)
		waitForChild(c, 10*time.Second)
	}
	return nil
}

// waitForChild waits for a child's goroutine to exit within the timeout.
// The child should already have been cancelled via c.cancel().
// exec.CommandContext sends SIGKILL when the context is cancelled,
// so the process will be killed. We just wait for runChild to finish.
func waitForChild(c *child, timeout time.Duration) {
	select {
	case <-c.done:
	case <-time.After(timeout):
		slog.Warn("child goroutine did not exit in time", "version", c.version.Name)
	}
}

func (m *Manager) runChild(ctx context.Context, c *child) {
	defer close(c.done)
	defer func() {
		m.mu.Lock()
		if current, ok := m.processes[c.version.Name]; ok && current == c {
			delete(m.processes, c.version.Name)
			m.rebuildRoutes()
		}
		m.mu.Unlock()
	}()

	binPath := filepath.Join(m.cfg.BinDir, c.version.Name, m.cfg.BinaryName)
	dataDir := filepath.Join(m.cfg.DataDir, c.version.Name)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		slog.Error("create data dir failed", "version", c.version.Name, "error", err)
		return
	}

	preflight, err := preflightChild(binPath, c.version.Name)
	if err != nil {
		slog.Error("child preflight failed", "version", c.version.Name, "bin", binPath, "error", err)
		return
	}

	backoff := time.Second
	lastStart := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		cmd := exec.CommandContext(ctx, binPath,
			"--data-dir", dataDir,
			"--port", fmt.Sprintf("%d", c.port),
		)
		cmd.Env = childEnv(preflight.binaryLogVersion)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		// Cancel sends SIGKILL by default. Override to send SIGTERM for graceful shutdown.
		cmd.Cancel = func() error {
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		cmd.WaitDelay = 5 * time.Second // SIGKILL after 5s if SIGTERM didn't work

		lastStart = time.Now()
		slog.Info("starting child", "version", c.version.Name, "port", c.port)

		if err := cmd.Start(); err != nil {
			slog.Error("child start failed", "version", c.version.Name, "error", err)
			m.mu.Lock()
			c.status = statusStopped
			m.mu.Unlock()
			return
		}

		// Wait for the child to start accepting connections before routing traffic.
		if !waitForPort(ctx, c.port, 10*time.Second) {
			slog.Warn("child did not start listening in time, routing anyway", "version", c.version.Name)
		}
		m.mu.Lock()
		c.status = statusRunning
		m.rebuildRoutes()
		m.mu.Unlock()

		err := cmd.Wait()

		select {
		case <-ctx.Done():
			return
		default:
		}

		slog.Error("child exited", "version", c.version.Name, "error", err)

		m.mu.Lock()
		c.status = statusStopped
		m.rebuildRoutes()
		m.mu.Unlock()

		if time.Since(lastStart) > 60*time.Second {
			backoff = time.Second
		}

		slog.Info("restarting child after backoff", "version", c.version.Name, "backoff", backoff)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
	}
}

// childEnv sets per-child env vars for devshardd (and testapp in e2e).
// binaryLogVersion is normally the link-time build id from --print-binary-version
// (e.g. 0.2.13-v2-r2). Legacy binaries without that flag use the governance
// slot name (e.g. v2) instead.
func childEnv(binaryLogVersion string) []string {
	if binaryLogVersion == "" {
		return os.Environ()
	}
	return append(
		os.Environ(),
		fmt.Sprintf("DEVSHARD_BINARY_LOG_VERSION=%s", binaryLogVersion),
	)
}

// waitForPort polls until a TCP connection succeeds on the given port.
// Returns true if the port is reachable before the timeout or context cancellation.
func waitForPort(ctx context.Context, port int, timeout time.Duration) bool {
	deadline := time.After(timeout)
	addr := fmt.Sprintf("localhost:%d", port)
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline:
			return false
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// rebuildRoutes rebuilds the atomic route map. Only includes running children.
// Must be called with m.mu held.
func (m *Manager) rebuildRoutes() {
	routes := make(map[string]string)
	for _, c := range m.processes {
		if c.status == statusRunning {
			routes[c.version.Name] = fmt.Sprintf("localhost:%d", c.port)
		}
	}
	m.routes.Store(routes)
}
