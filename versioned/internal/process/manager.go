package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"versioned/internal/config"
	"versioned/internal/download"
	"versioned/internal/health"
	"versioned/internal/oracle"
	"versioned/internal/proxy"
)

const (
	childLoopbackHost      = "127.0.0.1"
	maxChildPort           = 65535
	storageModePostgres    = "postgres"
	installedVersionRetain = 3
)

var (
	errChildPortPoolExhausted = errors.New("child port pool exhausted")
	errLegacyDrainStatus      = errors.New("drain status endpoint unavailable")
	ErrHostDraining           = errors.New("versiond host is draining")
)

type child struct {
	version       oracle.Version
	archiveSHA256 string
	binaryVersion string
	storageMode   string
	binPath       string
	port          int
	adminPort     atomic.Int64
	stop          context.CancelFunc
	forceStopCh   chan struct{}
	forceStopOnce sync.Once
	done          chan struct{} // closed when runChild exits
	ready         chan struct{} // closed after readiness succeeds
	readyOnce     sync.Once
	// serving is this generation's own live readiness, refreshed by a monitor
	// started when it begins running. It is per generation on purpose: a probe
	// answered by a child that has since been swapped out must not decide
	// anything about its replacement.
	serving   atomic.Bool
	servingAt atomic.Int64
	// legacyWarned rate-limits the legacy-fallback warning to once per
	// generation: the monitor re-probes every second, and a legacy child
	// without /ready would otherwise write the same line forever.
	legacyWarned atomic.Bool
	proxyTarget  *proxy.Target
	status       generationState
	restart      bool
}

// servingFresh reports whether this generation's readiness monitor currently
// vouches for it. An answer older than childReadyStale is not an answer: a
// monitor that has stopped must not freeze the child at "ready".
func (c *child) servingFresh() bool {
	if !c.serving.Load() {
		return false
	}
	return time.Since(time.Unix(0, c.servingAt.Load())) < childReadyStale
}

// noteLegacyFallback records, once per generation, that readiness came through
// the legacy /healthz fallback rather than /ready.
func (c *child) noteLegacyFallback(path string) {
	if c.legacyWarned.CompareAndSwap(false, true) {
		slog.Warn("ready path unavailable; using legacy readiness fallback",
			"version", c.version.Name, "port", c.port, "ready_path", path)
	}
}

func (c *child) Stop() {
	if c.stop != nil {
		c.stop()
	}
}

func (c *child) ForceStop() {
	c.Stop()
	if c.forceStopCh != nil {
		c.forceStopOnce.Do(func() { close(c.forceStopCh) })
	}
}

func (c *child) Done() <-chan struct{} {
	return c.done
}

type Manager struct {
	cfg             config.Config
	processes       map[string]*child
	draining        map[string][]*child
	children        map[*child]struct{}
	downloading     map[string]struct{}
	allocatedPorts  map[int]struct{}
	reservedPorts   map[int]struct{}
	operations      map[uint64]controlOperation
	nextOperationID uint64
	conditions      Conditions
	everConverged   bool
	available       chan struct{}
	childCtx        context.Context
	cancelChildren  context.CancelFunc
	hostDraining    bool
	mu              sync.Mutex
	routes          atomic.Value // proxy.RouteTable
}

func NewManager(cfg config.Config) *Manager {
	cfg = normalizeConfig(cfg)
	childCtx, cancelChildren := context.WithCancel(context.Background())
	m := &Manager{
		cfg:            cfg,
		processes:      make(map[string]*child),
		draining:       make(map[string][]*child),
		children:       make(map[*child]struct{}),
		downloading:    make(map[string]struct{}),
		allocatedPorts: make(map[int]struct{}),
		reservedPorts:  reservedChildPorts(),
		operations:     make(map[uint64]controlOperation),
		childCtx:       childCtx,
		cancelChildren: cancelChildren,
		available:      make(chan struct{}, 1),
	}
	m.routes.Store(proxy.RouteTable{})
	return m
}

func normalizeConfig(cfg config.Config) config.Config {
	if cfg.BasePort <= 0 || cfg.BasePort > maxChildPort {
		cfg.BasePort = 5000
	}
	if cfg.ReadyPath == "" {
		cfg.ReadyPath = "/ready"
	}
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = 60 * time.Second
	}
	if cfg.DrainPath == "" {
		cfg.DrainPath = "/drain"
	}
	if cfg.DrainStatusPath == "" {
		cfg.DrainStatusPath = "/drain/status"
	}
	if cfg.DrainTimeout <= 0 {
		cfg.DrainTimeout = 15 * time.Minute
	}
	if cfg.DrainPollInterval <= 0 {
		cfg.DrainPollInterval = time.Second
	}
	if cfg.DrainKillGrace <= 0 {
		cfg.DrainKillGrace = config.DefaultDrainKillGrace
	}
	if cfg.ChildShutdownGrace <= 0 {
		cfg.ChildShutdownGrace = cfg.DrainKillGrace
		name := strings.ToLower(strings.TrimSpace(cfg.BinaryName))
		if (name == "devshard" || name == "devshardd") &&
			config.DefaultDevshardShutdownGrace > cfg.ChildShutdownGrace {
			cfg.ChildShutdownGrace = config.DefaultDevshardShutdownGrace
		}
	}
	if cfg.ChildShutdownGrace < cfg.DrainKillGrace {
		cfg.ChildShutdownGrace = cfg.DrainKillGrace
	}
	return cfg
}

// assignPort returns a currently-free child port.
// Must be called with m.mu held.
func (m *Manager) assignPort() (int, error) {
	for port := m.cfg.BasePort; port <= maxChildPort; port++ {
		if _, used := m.allocatedPorts[port]; used {
			continue
		}
		if _, reserved := m.reservedPorts[port]; reserved {
			continue
		}
		m.allocatedPorts[port] = struct{}{}
		return port, nil
	}
	return 0, fmt.Errorf(
		"%w in range %d-%d",
		errChildPortPoolExhausted,
		m.cfg.BasePort,
		maxChildPort,
	)
}

func reservedChildPorts() map[int]struct{} {
	ports := make(map[int]struct{})
	if port, ok := parseListenPort(config.ListenAddr()); ok {
		ports[port] = struct{}{}
	}
	return ports
}

func parseListenPort(addr string) (int, bool) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		if !strings.HasPrefix(addr, ":") {
			slog.Warn("cannot parse versiond listen address for child port reservation", "addr", addr, "error", err)
			return 0, false
		}
		portStr = strings.TrimPrefix(addr, ":")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > maxChildPort {
		slog.Warn("cannot parse versiond listen port for child port reservation", "addr", addr, "port", portStr, "error", err)
		return 0, false
	}
	return port, true
}

// releasePort releases a child port after the child process exits.
// Must be called with m.mu held.
func (m *Manager) releasePort(port int) {
	if port > 0 {
		delete(m.allocatedPorts, port)
	}
}

func (m *Manager) devshardAdminEligible() bool {
	name := strings.ToLower(m.cfg.BinaryName)
	return name == "devshard" || name == "devshardd"
}

func (m *Manager) RouteTable() *atomic.Value {
	return &m.routes
}

// childReadyInterval matches the balancer's own cadence: readiness that is
// refreshed less often than it is asked about would report a stale answer, and
// refreshing it more often only adds load. childReadyStale is the point past
// which a missing refresh is treated as unready — a monitor that has stopped
// must not freeze the answer at "ready" forever.
const (
	childReadyInterval = time.Second
	childReadyTimeout  = 2 * time.Second
	childReadyStale    = 5 * time.Second
)

// ServesVersion reports whether this host can serve the named version right now.
// It is a pure read of state a monitor keeps current, so a balancer checking
// every second cannot turn into a probe of the child every second, and no number
// of concurrent callers can fan out into concurrent probes.
//
// Two things have to hold, and neither implies the other. The route table — the
// same one the proxy uses, so the answer cannot disagree with what a request
// would get — must have a target for the version. And the generation behind it
// must still be reporting itself ready: readiness is checked once when a child
// starts, but a child can lose it afterwards, and a route held open to a child
// that has gone unready is a route to a host that cannot serve.
func (m *Manager) ServesVersion(name string) bool {
	routes, ok := m.routes.Load().(proxy.RouteTable)
	if !ok {
		return false
	}
	if _, served := routes[name]; !served {
		return false
	}

	m.mu.Lock()
	c := m.processes[name]
	running := c != nil && c.status == statusRunning
	m.mu.Unlock()
	return running && c.servingFresh()
}

// watchChildReadiness keeps one generation's serving flag current. It is bound to
// the generation, so a swap simply ends this monitor and starts the next one; a
// late answer can only ever be written to the child that was asked.
func (m *Manager) watchChildReadiness(ctx context.Context, c *child) {
	ticker := time.NewTicker(childReadyInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		m.mu.Lock()
		stillRunning := c.status == statusRunning
		port := c.lifecyclePort()
		publicPort := c.port
		allowLegacy := int(c.adminPort.Load()) == 0
		path := m.cfg.ReadyPath
		m.mu.Unlock()
		if !stillRunning {
			return
		}

		probeCtx, cancel := context.WithTimeout(ctx, childReadyTimeout)
		client := &http.Client{Timeout: childReadyTimeout}
		ready, viaLegacy := readyEndpointReady(probeCtx, client, port, path, allowLegacy)
		if viaLegacy {
			c.noteLegacyFallback(path)
		}
		if ready && !allowLegacy {
			ready = publicEndpointReady(probeCtx, client, publicPort)
		}
		cancel()

		c.serving.Store(ready)
		c.servingAt.Store(time.Now().UnixNano())
	}
}

func (m *Manager) Available() <-chan struct{} {
	return m.available
}

func (m *Manager) Status() []health.StatusEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *Manager) statusLocked() []health.StatusEntry {
	out := make([]health.StatusEntry, 0, len(m.processes)+len(m.draining))
	for _, c := range m.processes {
		out = append(out, statusEntry(c))
	}
	for _, children := range m.draining {
		for _, c := range children {
			out = append(out, statusEntry(c))
		}
	}
	sortStatusEntries(out)
	return out
}

func statusEntry(c *child) health.StatusEntry {
	return health.StatusEntry{
		Name:          c.version.Name,
		Port:          c.port,
		Status:        healthGenerationStatus(c.status),
		SHA256:        c.archiveSHA256,
		BinaryVersion: c.binaryVersion,
	}
}

func sortStatusEntries(entries []health.StatusEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		if entries[i].Status != entries[j].Status {
			return entries[i].Status < entries[j].Status
		}
		return entries[i].Port < entries[j].Port
	})
}

// Reconcile compares desired state against local state and converges.
// The desired sha256 is the archive identity from the oracle. Downloaded
// versions also record local install metadata so we can distinguish archive
// identity from the extracted executable bytes on disk.
func (m *Manager) Reconcile(parent context.Context, desired []oracle.Version) error {
	ctx, operationID, err := m.beginOperation(parent, operationReconcile)
	if err != nil {
		return err
	}
	defer m.endOperation(operationID)
	desiredCount, err := m.reconcile(ctx, desired)
	m.recordReconcileResult(desiredCount, err)
	return err
}

func (m *Manager) reconcile(ctx context.Context, desired []oracle.Version) (int, error) {

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
		version        oracle.Version
		overrideSrc    string
		binPath        string
		blockedByDrain bool
	}
	var overrides []overrideAction

	// Snapshot which versions are running and which are downloading.
	type versionSnapshot struct {
		version       oracle.Version
		isRunning     bool
		isDraining    bool
		isDownloading bool
		child         *child
	}
	var snapshots []versionSnapshot

	for _, v := range desiredSet {
		running, isRunning := m.processes[v.Name]
		isDraining := len(m.draining[v.Name]) > 0
		if overrideSrc, isOverride := m.cfg.Overrides[v.Name]; isOverride {
			binPath := filepath.Join(m.cfg.BinDir, v.Name, m.cfg.BinaryName)
			overrides = append(overrides, overrideAction{
				version:        v,
				overrideSrc:    overrideSrc,
				binPath:        binPath,
				blockedByDrain: !isRunning && isDraining,
			})
			continue
		}
		_, isDownloading := m.downloading[v.Name]
		snapshots = append(snapshots, versionSnapshot{
			version:       v,
			isRunning:     isRunning,
			isDraining:    isDraining,
			isDownloading: isDownloading,
			child:         running,
		})
	}
	m.mu.Unlock()

	// Phase B (no lock): resolve hashes, do disk I/O for overrides and hash checks.
	var actionErrs []error
	for _, o := range overrides {
		if o.blockedByDrain {
			slog.Info("version start deferred while previous child is draining", "version", o.version.Name)
			continue
		}
		if err := m.reconcileOverride(ctx, o.version, o.overrideSrc, o.binPath); err != nil {
			slog.Error("override reconcile failed", "version", o.version.Name, "error", err)
			actionErrs = append(actionErrs, fmt.Errorf("override %s: %w", o.version.Name, err))
		}
	}

	var toDownload []versionAction
	var toSwap []versionAction
	var toStart []versionAction
	desiredHashes := make(map[string]string)

	for _, snap := range snapshots {
		if snap.isDownloading {
			continue
		}

		desiredHash, err := snap.version.ResolvedSHA256()
		if err != nil {
			slog.Error("cannot resolve sha256, skipping", "version", snap.version.Name, "error", err)
			actionErrs = append(
				actionErrs,
				fmt.Errorf("resolve sha256 for %s: %w", snap.version.Name, err),
			)
			continue
		}
		desiredHashes[snap.version.Name] = desiredHash
		if !snap.isRunning && snap.isDraining {
			slog.Info("version start deferred while previous child is draining", "version", snap.version.Name)
			continue
		}

		if snap.isRunning {
			matches, metadata, diskBinaryHash, stateErr := installedVersionMatches(filepath.Dir(snap.child.binPath), snap.child.binPath, desiredHash)
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
		if artifact, ok := m.resolveInstalledArtifact(snap.version.Name, desiredHash); ok {
			toStart = append(toStart, versionAction{
				version: snap.version,
				sha256:  desiredHash,
				binPath: artifact.binPath,
			})
			continue
		}
		toDownload = append(toDownload, versionAction{version: snap.version, sha256: desiredHash})
	}

	// Phase C (lock): apply decisions -- start ready children, mark downloads, stop removed.
	m.mu.Lock()
	if m.hostDraining {
		m.mu.Unlock()
		return len(desiredSet), ErrHostDraining
	}
	var startErrs []error
	started := 0
	for _, a := range toStart {
		if _, already := m.processes[a.version.Name]; already {
			continue // another reconcile started it
		}
		if m.versionStartBlockedLocked(a.version.Name) {
			continue
		}
		if err := m.startChild(ctx, a.version, a.sha256, a.binPath, true); err != nil {
			startErrs = append(startErrs, fmt.Errorf("start cached version %s: %w", a.version.Name, err))
			continue
		}
		started++
	}
	scheduledDownloads := make([]versionAction, 0, len(toDownload))
	for _, a := range toDownload {
		if _, already := m.downloading[a.version.Name]; already {
			continue
		}
		if _, running := m.processes[a.version.Name]; running || m.versionStartBlockedLocked(a.version.Name) {
			continue
		}
		m.downloading[a.version.Name] = struct{}{}
		scheduledDownloads = append(scheduledDownloads, a)
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
			transitionGenerationLocked(c, statusRetiring)
			c.restart = false
			delete(m.processes, name)
			m.draining[name] = append(m.draining[name], c)
		}
	}

	changed := len(scheduledDownloads) > 0 || len(toSwap) > 0 || len(toStop) > 0 || started > 0
	if changed {
		m.rebuildRoutes()
	}
	proxyDrained := make(map[*child]<-chan struct{}, len(toStop))
	for _, c := range toStop {
		proxyDrained[c] = retireProxyTarget(c)
	}
	m.mu.Unlock()

	// Removed versions leave the route table immediately, then drain
	// asynchronously so reconcile can continue handling other versions.
	for _, c := range toStop {
		slog.Info("draining removed version", "version", c.version.Name)
		go m.drainAfterProxy(c, proxyDrained[c])
	}

	// Downloads outside the lock (can be slow).
	for _, a := range scheduledDownloads {
		if err := m.downloadAndStart(ctx, a.version, a.sha256); err != nil {
			slog.Error("download or start failed, skipping", "version", a.version.Name, "error", err)
			actionErrs = append(actionErrs, fmt.Errorf("download or start %s: %w", a.version.Name, err))
		}
	}

	// Download first, then overlap generations where their storage permits it;
	// otherwise the stop/start path withdraws the old route before stopping it.
	for _, a := range toSwap {
		if err := m.downloadAndSwap(ctx, a.version, a.sha256, a.child); err != nil {
			slog.Error("swap failed, keeping old version", "version", a.version.Name, "error", err)
			actionErrs = append(actionErrs, fmt.Errorf("swap %s: %w", a.version.Name, err))
		}
	}

	m.gcInstalledVersions(desiredHashes)
	return len(desiredSet), errors.Join(append(startErrs, actionErrs...)...)
}

type versionAction struct {
	version oracle.Version
	sha256  string // pre-resolved hash, avoids double resolution in downloadBinary
	binPath string // non-empty for cached start actions
	child   *child // non-nil for swap actions
}

// versionStartBlockedLocked reports whether a retired generation with the same
// version name still owns the version's data directory.
func (m *Manager) versionStartBlockedLocked(name string) bool {
	return len(m.draining[name]) > 0
}

// reconcileOverride handles a version with a local override binary.
// Does disk I/O outside the lock, then takes the lock to update state.
func (m *Manager) reconcileOverride(ctx context.Context, v oracle.Version, overrideSrc, binPath string) error {
	m.mu.Lock()
	if m.hostDraining {
		m.mu.Unlock()
		return ErrHostDraining
	}
	m.mu.Unlock()

	if stat, statErr := os.Stat(overrideSrc); statErr != nil {
		slog.Error(
			"override path missing or unreadable",
			"version", v.Name,
			"path", overrideSrc,
			"env_key", fmt.Sprintf("VERSIOND_OVERRIDE_%s", strings.ReplaceAll(v.Name, ".", "_")),
			"error", statErr,
		)
		return statErr
	} else if stat.IsDir() {
		slog.Error(
			"override path points to directory, expected file",
			"version", v.Name,
			"path", overrideSrc,
			"env_key", fmt.Sprintf("VERSIOND_OVERRIDE_%s", strings.ReplaceAll(v.Name, ".", "_")),
		)
		return fmt.Errorf("override path %s is a directory", overrideSrc)
	}

	srcHash, err := download.HashFile(overrideSrc)
	if err != nil {
		slog.Error("override source unreadable", "version", v.Name, "path", overrideSrc, "error", err)
		return err
	}
	overrideID := "override:" + srcHash

	// Check if already running the same binary (lock for snapshot only).
	m.mu.Lock()
	existing, isRunning := m.processes[v.Name]
	m.mu.Unlock()

	if isRunning && existing.binPath == binPath && existing.archiveSHA256 == overrideID {
		diskHash, hashErr := download.HashFile(binPath)
		if hashErr == nil && diskHash == srcHash {
			return nil // already running the same override binary
		}
	}
	if isRunning {
		m.mu.Lock()
		if m.hostDraining {
			m.mu.Unlock()
			return ErrHostDraining
		}
		proxyDrained, retired := m.retireChildForStopStartLocked(existing)
		m.mu.Unlock()
		if !retired {
			return nil
		}

		// Override source changed: withdraw the route, let requests that already
		// own its target lease finish, then stop, copy and start.
		slog.Info("override binary changed, restarting", "version", v.Name)
		if err := m.stopRetiredChild(ctx, existing, proxyDrained); err != nil {
			return err
		}
	}

	// Disk I/O outside the lock.
	binDir := filepath.Join(m.cfg.BinDir, v.Name)
	if err := os.MkdirAll(binDir, 0755); err != nil {
		slog.Error("override mkdir failed", "version", v.Name, "error", err)
		return err
	}

	if err := atomicCopy(overrideSrc, binPath); err != nil {
		slog.Error("override copy failed", "version", v.Name, "error", err)
		return err
	}

	slog.Info("using override binary", "version", v.Name, "path", overrideSrc)

	m.mu.Lock()
	if m.hostDraining {
		m.mu.Unlock()
		return ErrHostDraining
	}
	if _, running := m.processes[v.Name]; running || m.versionStartBlockedLocked(v.Name) {
		m.mu.Unlock()
		return nil
	}
	if err := m.startChild(ctx, v, overrideID, binPath, true); err != nil {
		m.mu.Unlock()
		slog.Error("override start failed", "version", v.Name, "error", err)
		return err
	}
	m.rebuildRoutes()
	m.mu.Unlock()
	return nil
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

func (m *Manager) installDir(versionName, sha string) string {
	return filepath.Join(m.cfg.BinDir, versionName, sha)
}

func (m *Manager) installBinPath(versionName, sha string) string {
	return filepath.Join(m.installDir(versionName, sha), m.cfg.BinaryName)
}

type installedArtifact struct {
	dir     string
	binPath string
}

func (m *Manager) resolveInstalledArtifact(versionName, desiredHash string) (installedArtifact, bool) {
	canonical := installedArtifact{
		dir:     m.installDir(versionName, desiredHash),
		binPath: m.installBinPath(versionName, desiredHash),
	}
	matches, metadata, diskBinaryHash, stateErr := installedVersionMatches(
		canonical.dir,
		canonical.binPath,
		desiredHash,
	)
	if stateErr == nil && matches {
		return canonical, true
	}
	if stateErr == nil || !errors.Is(stateErr, os.ErrNotExist) {
		logInstalledVersionMismatch(
			"cached version",
			versionName,
			desiredHash,
			metadata,
			diskBinaryHash,
			stateErr,
		)
	}
	// Do not clean an unreadable canonical install here. Another versiond
	// sharing BinDir may still be publishing it; promotion and downloads use
	// atomic replacement for ordinary files.

	legacy := installedArtifact{
		dir:     filepath.Join(m.cfg.BinDir, versionName),
		binPath: filepath.Join(m.cfg.BinDir, versionName, m.cfg.BinaryName),
	}
	matches, metadata, diskBinaryHash, stateErr = installedVersionMatches(
		legacy.dir,
		legacy.binPath,
		desiredHash,
	)
	if stateErr != nil || !matches {
		if stateErr == nil || !errors.Is(stateErr, os.ErrNotExist) {
			logInstalledVersionMismatch(
				"legacy cached version",
				versionName,
				desiredHash,
				metadata,
				diskBinaryHash,
				stateErr,
			)
		}
		return installedArtifact{}, false
	}

	if err := m.promoteLegacyInstall(legacy, canonical, metadata, desiredHash); err == nil {
		slog.Info(
			"promoted legacy cached install",
			"version", versionName,
			"sha256", desiredHash,
			"source", legacy.dir,
			"destination", canonical.dir,
		)
		return canonical, true
	} else {
		slog.Warn(
			"legacy install promotion failed; using verified flat install",
			"version", versionName,
			"sha256", desiredHash,
			"source", legacy.dir,
			"destination", canonical.dir,
			"error", err,
		)
	}

	// The source may have changed while promotion copied it. Verify it again
	// before falling back to the legacy path.
	matches, _, _, stateErr = installedVersionMatches(legacy.dir, legacy.binPath, desiredHash)
	if stateErr != nil || !matches {
		return installedArtifact{}, false
	}
	return legacy, true
}

func (m *Manager) promoteLegacyInstall(
	legacy installedArtifact,
	canonical installedArtifact,
	metadata download.InstallMetadata,
	desiredHash string,
) error {
	if err := os.MkdirAll(canonical.dir, 0o755); err != nil {
		return fmt.Errorf("create per-sha install dir: %w", err)
	}
	if err := atomicCopy(legacy.binPath, canonical.binPath); err != nil {
		return fmt.Errorf("copy legacy binary: %w", err)
	}
	promotedBinaryHash, err := download.HashFile(canonical.binPath)
	if err != nil {
		return fmt.Errorf("hash promoted binary: %w", err)
	}
	if !strings.EqualFold(promotedBinaryHash, metadata.BinarySHA256) {
		return fmt.Errorf(
			"verify promoted binary: got %s, want %s",
			promotedBinaryHash,
			metadata.BinarySHA256,
		)
	}
	if err := download.WriteInstallMetadata(canonical.dir, metadata); err != nil {
		return fmt.Errorf("write per-sha install metadata: %w", err)
	}

	matches, promotedMetadata, diskBinaryHash, err := installedVersionMatches(
		canonical.dir,
		canonical.binPath,
		desiredHash,
	)
	if err != nil {
		return fmt.Errorf("verify promoted install: %w", err)
	}
	if !matches {
		return fmt.Errorf(
			"verify promoted install: archive=%s binary=%s expected_archive=%s expected_binary=%s",
			promotedMetadata.ArchiveSHA256,
			diskBinaryHash,
			desiredHash,
			promotedMetadata.BinarySHA256,
		)
	}
	return nil
}

// downloadAndStart downloads the binary using the pre-resolved hash, then starts the child.
func (m *Manager) downloadAndStart(ctx context.Context, v oracle.Version, sha string) error {
	dlErr := m.downloadBinary(ctx, v, sha)

	m.mu.Lock()
	delete(m.downloading, v.Name)
	_, running := m.processes[v.Name]
	var startErr error
	if dlErr == nil && ctx.Err() == nil && !m.hostDraining && !running && !m.versionStartBlockedLocked(v.Name) {
		startErr = m.startChild(ctx, v, sha, m.installBinPath(v.Name, sha), true)
	}
	m.mu.Unlock()
	if dlErr != nil {
		return dlErr
	}
	if startErr != nil {
		return fmt.Errorf("start downloaded version %s: %w", v.Name, startErr)
	}
	return nil
}

// downloadAndSwap downloads the new binary. Shared-storage generations overlap:
// the replacement starts on a fresh port before the route swap. Other storage
// modes withdraw and drain the old route before a stop/start replacement.
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

	newBinPath := m.installBinPath(v.Name, sha)
	m.mu.Lock()
	if m.hostDraining {
		delete(m.downloading, v.Name)
		m.mu.Unlock()
		return ErrHostDraining
	}
	m.mu.Unlock()
	preflight, err := preflightChildWithAdminProbeContext(
		ctx,
		newBinPath,
		v.Name,
		m.devshardAdminEligible(),
	)
	if err != nil {
		m.mu.Lock()
		delete(m.downloading, v.Name)
		m.mu.Unlock()
		return fmt.Errorf("preflight replacement version %s: %w", v.Name, err)
	}
	if !m.rollingOverlapAllowed(v.Name, old, preflight.storageMode) {
		slog.Warn("rolling overlap disabled without shared storage; falling back to stop/start swap", "version", v.Name)
		m.mu.Lock()
		if m.hostDraining {
			delete(m.downloading, v.Name)
			m.mu.Unlock()
			return ErrHostDraining
		}
		proxyDrained, retired := m.retireChildForStopStartLocked(old)
		m.mu.Unlock()
		if !retired {
			m.mu.Lock()
			delete(m.downloading, v.Name)
			m.mu.Unlock()
			return fmt.Errorf("current child changed before stop/start swap")
		}
		if err := m.stopRetiredChild(ctx, old, proxyDrained); err != nil {
			m.mu.Lock()
			delete(m.downloading, v.Name)
			m.mu.Unlock()
			return err
		}

		m.mu.Lock()
		delete(m.downloading, v.Name)
		if m.hostDraining {
			m.mu.Unlock()
			return ErrHostDraining
		}
		if err := ctx.Err(); err != nil {
			m.mu.Unlock()
			return err
		}
		if _, running := m.processes[v.Name]; running || m.versionStartBlockedLocked(v.Name) {
			m.mu.Unlock()
			return fmt.Errorf("version %s became active while stop/start swap was draining", v.Name)
		}
		startErr := m.startChild(ctx, v, sha, newBinPath, true)
		m.mu.Unlock()
		if startErr != nil {
			return fmt.Errorf("start replacement version %s: %w", v.Name, startErr)
		}
		return nil
	}

	m.mu.Lock()
	if m.hostDraining {
		delete(m.downloading, v.Name)
		m.mu.Unlock()
		return ErrHostDraining
	}
	newChild, startErr := m.newChild(ctx, v, sha, newBinPath, false)
	if startErr != nil {
		delete(m.downloading, v.Name)
		m.mu.Unlock()
		return fmt.Errorf("create replacement version %s: %w", v.Name, startErr)
	}
	m.mu.Unlock()
	go m.runChild(newChild.ctx, newChild.child)
	if err := waitForChildReady(ctx, newChild.child); err != nil {
		newChild.child.Stop()
		_ = waitForChildContext(ctx, newChild.child)
		m.mu.Lock()
		delete(m.downloading, v.Name)
		m.mu.Unlock()
		return err
	}

	m.mu.Lock()
	delete(m.downloading, v.Name)
	if m.hostDraining {
		m.mu.Unlock()
		newChild.child.Stop()
		_ = waitForChildContext(ctx, newChild.child)
		return ErrHostDraining
	}
	if current, ok := m.processes[v.Name]; !ok || current != old {
		m.mu.Unlock()
		newChild.child.Stop()
		_ = waitForChildContext(ctx, newChild.child)
		return fmt.Errorf("current child changed during swap")
	}
	if newChild.child.status != statusRunning || childDone(newChild.child) {
		m.mu.Unlock()
		newChild.child.Stop()
		_ = waitForChildContext(ctx, newChild.child)
		return fmt.Errorf("new child stopped before swap")
	}
	transitionGenerationLocked(old, statusRetiring)
	old.restart = false
	delete(m.processes, v.Name)
	m.draining[v.Name] = append(m.draining[v.Name], old)
	newChild.child.restart = true
	m.processes[v.Name] = newChild.child
	m.rebuildRoutes()
	proxyDrained := retireProxyTarget(old)
	m.mu.Unlock()

	slog.Info("swapped child route; old child draining", "version", v.Name, "old_port", old.port, "new_port", newChild.child.port)
	go m.drainAfterProxy(old, proxyDrained)
	return nil
}

func (m *Manager) downloadBinary(ctx context.Context, v oracle.Version, sha string) error {
	binDir := m.installDir(v.Name, sha)
	if err := download.Download(ctx, v.Binary, sha, binDir, m.cfg.BinaryName); err != nil {
		return err
	}
	slog.Info("downloaded binary", "version", v.Name, "sha256", sha)
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
	_ = os.Remove(versionDir)
}

type installedVersionDir struct {
	path    string
	sha     string
	modTime time.Time
}

func (m *Manager) gcInstalledVersions(desiredHashes map[string]string) {
	keep := make(map[string]map[string]struct{})
	addKeep := func(versionName, sha string) {
		if versionName == "" || sha == "" || strings.HasPrefix(sha, "override:") {
			return
		}
		if keep[versionName] == nil {
			keep[versionName] = make(map[string]struct{})
		}
		keep[versionName][sha] = struct{}{}
	}
	for versionName, sha := range desiredHashes {
		addKeep(versionName, sha)
	}

	m.mu.Lock()
	for _, c := range m.processes {
		addKeep(c.version.Name, c.archiveSHA256)
	}
	for _, children := range m.draining {
		for _, c := range children {
			addKeep(c.version.Name, c.archiveSHA256)
		}
	}
	m.mu.Unlock()

	gcInstalledVersionDirs(m.cfg.BinDir, m.cfg.BinaryName, keep, installedVersionRetain)
}

func gcInstalledVersionDirs(binDir, binaryName string, keep map[string]map[string]struct{}, retain int) {
	if retain < 0 {
		retain = 0
	}
	versionDirs, err := os.ReadDir(binDir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("installed version gc: read bin dir failed", "dir", binDir, "error", err)
		}
		return
	}
	for _, versionEntry := range versionDirs {
		if !versionEntry.IsDir() {
			continue
		}
		versionName := versionEntry.Name()
		versionDir := filepath.Join(binDir, versionName)
		shaDirs, err := os.ReadDir(versionDir)
		if err != nil {
			slog.Warn("installed version gc: read version dir failed", "version", versionName, "dir", versionDir, "error", err)
			continue
		}
		var stale []installedVersionDir
		for _, shaEntry := range shaDirs {
			if !shaEntry.IsDir() {
				continue
			}
			sha := shaEntry.Name()
			dir := filepath.Join(versionDir, sha)
			metadata, err := download.ReadInstallMetadata(dir)
			if err != nil {
				continue
			}
			if keepInstalledVersion(keep, versionName, sha, metadata.ArchiveSHA256) {
				continue
			}
			stale = append(stale, installedVersionDir{
				path:    dir,
				sha:     sha,
				modTime: installedVersionModTime(dir),
			})
		}
		sort.Slice(stale, func(i, j int) bool {
			if stale[i].modTime.Equal(stale[j].modTime) {
				return stale[i].sha > stale[j].sha
			}
			return stale[i].modTime.After(stale[j].modTime)
		})
		for i := retain; i < len(stale); i++ {
			slog.Info("installed version gc: removing stale install", "version", versionName, "sha256", stale[i].sha, "dir", stale[i].path)
			cleanupInstalledVersionState(stale[i].path, filepath.Join(stale[i].path, binaryName))
		}
	}
}

func keepInstalledVersion(keep map[string]map[string]struct{}, versionName, dirSHA, archiveSHA string) bool {
	versionKeep := keep[versionName]
	if versionKeep == nil {
		return false
	}
	if _, ok := versionKeep[dirSHA]; ok {
		return true
	}
	_, ok := versionKeep[archiveSHA]
	return ok
}

func installedVersionModTime(dir string) time.Time {
	if info, err := os.Stat(filepath.Join(dir, download.InstallMetadataFilename)); err == nil {
		return info.ModTime()
	}
	if info, err := os.Stat(dir); err == nil {
		return info.ModTime()
	}
	return time.Time{}
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

type childStart struct {
	child *child
	ctx   context.Context
}

func (m *Manager) newChild(_ context.Context, v oracle.Version, sha, binPath string, restart bool) (childStart, error) {
	if m.hostDraining {
		return childStart{}, ErrHostDraining
	}
	port, err := m.assignPort()
	if err != nil {
		return childStart{}, fmt.Errorf("allocate public port for version %s: %w", v.Name, err)
	}
	childCtx, childCancel := context.WithCancel(m.childCtx)
	c := &child{
		version:       v,
		archiveSHA256: sha,
		binPath:       binPath,
		port:          port,
		stop:          childCancel,
		forceStopCh:   make(chan struct{}),
		done:          make(chan struct{}),
		ready:         make(chan struct{}),
		status:        statusPreparing,
		restart:       restart,
	}
	m.children[c] = struct{}{}
	return childStart{child: c, ctx: childCtx}, nil
}

// startChild must be called with m.mu held.
func (m *Manager) startChild(ctx context.Context, v oracle.Version, sha, binPath string, restart bool) error {
	if m.hostDraining {
		return ErrHostDraining
	}
	start, err := m.newChild(ctx, v, sha, binPath, restart)
	if err != nil {
		return err
	}
	c := start.child
	m.processes[v.Name] = c
	go m.runChild(start.ctx, c)
	return nil
}

// BeginHostDrain freezes desired-state changes and crash restarts while the
// host admission layer waits for already accepted proxy requests to finish.
func (m *Manager) BeginHostDrain() {
	m.mu.Lock()
	m.hostDraining = true
	for c := range m.children {
		c.restart = false
	}
	m.downloading = make(map[string]struct{})
	m.mu.Unlock()
	m.cancelOperations()
}

// RequestChildrenDrain removes every child route before issuing lifecycle
// drain requests. Calls run concurrently and never hold m.mu during network I/O.
func (m *Manager) RequestChildrenDrain(ctx context.Context) error {
	children := m.prepareChildrenForDrain()
	errCh := make(chan error, len(children))
	var wg sync.WaitGroup
	for _, c := range children {
		wg.Add(1)
		go func(c *child) {
			defer wg.Done()
			if err := m.requestDrain(ctx, c); err != nil {
				errCh <- fmt.Errorf("request drain for %s: %w", c.version.Name, err)
			}
		}(c)
	}
	wg.Wait()
	close(errCh)
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// WaitChildrenIdle waits for every child-side lifecycle counter to reach zero.
// Legacy children without a status endpoint receive the configured compatibility
// grace before they are considered drained.
func (m *Manager) WaitChildrenIdle(ctx context.Context) error {
	children := m.snapshotChildren()
	errCh := make(chan error, len(children))
	var wg sync.WaitGroup
	for _, c := range children {
		wg.Add(1)
		go func(c *child) {
			defer wg.Done()
			if err := m.waitChildIdle(ctx, c); err != nil {
				errCh <- fmt.Errorf("wait for %s to drain: %w", c.version.Name, err)
			}
		}(c)
	}
	wg.Wait()
	close(errCh)
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (m *Manager) waitChildIdle(ctx context.Context, c *child) error {
	for {
		select {
		case <-c.done:
			return nil
		default:
		}

		inflight, err := m.fetchInflight(ctx, c)
		if err == nil && inflight == 0 {
			return nil
		}
		if errors.Is(err, errLegacyDrainStatus) {
			slog.Warn(
				"drain status endpoint unavailable; waiting legacy drain grace",
				"version", c.version.Name,
				"port", c.port,
				"grace", m.cfg.DrainKillGrace,
			)
			timer := time.NewTimer(m.cfg.DrainKillGrace)
			defer timer.Stop()
			select {
			case <-c.done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
		if err != nil {
			slog.Warn("child drain status failed; retrying", "version", c.version.Name, "error", err)
		}
		timer := time.NewTimer(m.cfg.DrainPollInterval)
		select {
		case <-c.done:
			timer.Stop()
			return nil
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *Manager) prepareChildrenForDrain() []*child {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hostDraining = true
	children := m.allChildrenLocked()
	for _, c := range children {
		c.restart = false
		if !childDone(c) {
			transitionGenerationLocked(c, statusRetiring)
		}
		delete(m.processes, c.version.Name)
		m.appendDrainingLocked(c)
	}
	m.downloading = make(map[string]struct{})
	m.rebuildRoutes()
	return children
}

func (m *Manager) appendDrainingLocked(target *child) {
	for _, c := range m.draining[target.version.Name] {
		if c == target {
			return
		}
	}
	m.draining[target.version.Name] = append(m.draining[target.version.Name], target)
}

func (m *Manager) allChildrenLocked() []*child {
	seen := make(map[*child]struct{}, len(m.children)+len(m.processes)+len(m.draining))
	children := make([]*child, 0, len(m.children)+len(m.processes)+len(m.draining))
	appendChild := func(c *child) {
		if c == nil {
			return
		}
		if _, ok := seen[c]; ok {
			return
		}
		seen[c] = struct{}{}
		children = append(children, c)
	}
	for c := range m.children {
		appendChild(c)
	}
	for _, c := range m.processes {
		appendChild(c)
	}
	for _, draining := range m.draining {
		for _, c := range draining {
			appendChild(c)
		}
	}
	return children
}

func (m *Manager) snapshotChildren() []*child {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.allChildrenLocked()
}

func (m *Manager) ForceStopChildren() {
	m.cancelOperations()
	m.cancelChildren()
	for _, c := range m.snapshotChildren() {
		c.ForceStop()
	}
}

func (m *Manager) Shutdown(ctx context.Context) error {
	children := m.prepareChildrenForDrain()
	defer m.clearStoppedChildren()
	for _, c := range children {
		m.mu.Lock()
		transitionGenerationLocked(c, statusStopping)
		m.mu.Unlock()
		c.Stop()
	}
	m.cancelChildren()

	if len(children) == 0 {
		return nil
	}

	allDone := make(chan struct{})
	go func() {
		defer close(allDone)
		for _, c := range children {
			slog.Info("waiting for child shutdown", "version", c.version.Name)
			waitForChild(c)
		}
	}()

	select {
	case <-allDone:
		return nil
	case <-ctx.Done():
	}
	select {
	case <-allDone:
		return nil
	default:
	}

	// The caller's deadline escalates every remaining child to SIGKILL. It does
	// not waive process ownership: wait until every command has been reaped.
	for _, c := range children {
		c.ForceStop()
	}
	<-allDone
	return ctx.Err()
}

// ShutdownGrace is the SIGTERM-to-SIGKILL allowance needed by the slowest
// child. The host shutdown coordinator reserves this tail before spending the
// rest of its absolute budget on request and lifecycle drain.
func (m *Manager) ShutdownGrace() time.Duration {
	return m.childStopTimeout()
}

func (m *Manager) clearStoppedChildren() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.processes = make(map[string]*child)
	m.draining = make(map[string][]*child)
	m.children = make(map[*child]struct{})
	m.downloading = make(map[string]struct{})
	m.rebuildRoutes()
}

func waitForChild(c *child) {
	<-c.Done()
}

func waitForChildContext(ctx context.Context, c *child) error {
	select {
	case <-c.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) runChild(ctx context.Context, c *child) {
	defer close(c.done)
	defer func() {
		m.mu.Lock()
		transitionGenerationLocked(c, statusStopped)
		delete(m.children, c)
		if current, ok := m.processes[c.version.Name]; ok && current == c {
			delete(m.processes, c.version.Name)
			m.rebuildRoutes()
		}
		m.removeDrainingLocked(c)
		m.releasePort(c.port)
		m.releasePort(int(c.adminPort.Load()))
		m.mu.Unlock()
	}()

	dataDir := filepath.Join(m.cfg.DataDir, c.version.Name)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		slog.Error("create data dir failed", "version", c.version.Name, "error", err)
		return
	}

	preflight, err := preflightChildWithAdminProbeContext(
		ctx,
		c.binPath,
		c.version.Name,
		m.devshardAdminEligible(),
	)
	if err != nil {
		slog.Error("child preflight failed", "version", c.version.Name, "bin", c.binPath, "error", err)
		m.mu.Lock()
		transitionGenerationFailureLocked(c)
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	c.binaryVersion = preflight.binaryLogVersion
	c.storageMode = preflight.storageMode
	if preflight.adminAPISupported && c.adminPort.Load() == 0 {
		adminPort, portErr := m.assignPort()
		if portErr != nil {
			transitionGenerationFailureLocked(c)
			m.mu.Unlock()
			slog.Error("allocate child admin port failed", "version", c.version.Name, "error", portErr)
			return
		}
		c.adminPort.Store(int64(adminPort))
	}
	transitionGenerationLocked(c, statusStarting)
	adminAddr := c.adminAddr()
	m.mu.Unlock()

	backoff := time.Second
	lastStart := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		cmd := exec.Command(c.binPath,
			"--data-dir", dataDir,
			"--port", fmt.Sprintf("%d", c.port),
		)
		cmd.Env = childEnv(preflight.binaryLogVersion, adminAddr, preflight.haDeployment)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		lastStart = time.Now()

		// The fork is linearised with the drain barrier: the guard and
		// cmd.Start happen under the same m.mu that BeginHostDrain and
		// prepareChildrenForDrain take, so only two orders exist — the process
		// starts before the barrier and the drain sees and stops it, or the
		// barrier is up first and the process never starts. A guard that
		// released the lock before forking would only narrow the window:
		// retirement could land between the check and cmd.Start.
		//
		// Both halves of the barrier matter. BeginHostDrain sets hostDraining
		// long before RequestChildrenDrain retires the generations, and a child
		// finishing its preflight during the announce window sits in `starting`
		// — still legal by phase, already frozen by contract. The orphan would
		// get no route, yet WaitChildrenIdle would out-wait its startup on a
		// port nobody listens on: an evacuation stretched by a whole preflight
		// and ready timeout for a child that exists only to be killed.
		// startSupervisedProcess is fork/exec plus watcher goroutines; it does
		// no child I/O, so holding m.mu across it does not violate the
		// no-network-under-mutex rule.
		m.mu.Lock()
		if m.hostDraining || generationPhase(c.status) >= generationPhase(statusRetiring) {
			slog.Info("child start suppressed; the host is draining or the generation retired",
				"version", c.version.Name, "status", c.status, "host_draining", m.hostDraining)
			m.mu.Unlock()
			return
		}
		if c.status == statusFailed {
			transitionGenerationLocked(c, statusStarting)
		}
		slog.Info("starting child", "version", c.version.Name, "port", c.port, "admin_addr", adminAddr, "sha256", c.archiveSHA256)
		proc, err := startSupervisedProcess(cmd, ctx.Done(), c.forceStopCh, m.childStopTimeout())
		m.mu.Unlock()
		if err != nil {
			slog.Error("child start failed", "version", c.version.Name, "error", err)
			m.mu.Lock()
			transitionGenerationFailureLocked(c)
			m.mu.Unlock()
			return
		}

		if !waitForChildServingReady(ctx, c, m.cfg.ReadyPath, m.cfg.ReadyTimeout) {
			slog.Warn("child did not become ready in time", "version", c.version.Name, "port", c.port, "lifecycle_port", c.lifecyclePort(), "ready_path", m.cfg.ReadyPath)
			proc.ForceStop()
			_ = proc.Wait()
			m.mu.Lock()
			restart := c.restart
			transitionGenerationFailureLocked(c)
			if current, ok := m.processes[c.version.Name]; ok && current == c {
				m.rebuildRoutes()
			}
			m.mu.Unlock()
			if !restart {
				return
			}
			if !m.waitForRestartBackoff(ctx, c, backoff) {
				return
			}
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
			continue
		}
		// Seed the readiness flag before anything can see the route. close(c.ready)
		// wakes the swap path, which publishes this generation itself, and a
		// published route whose flag is still false answers 503 to the balancer's
		// next per-version check — with fall 1, one such answer evicts the host,
		// and a fleet-wide rolling update would blink the whole pool.
		c.serving.Store(true)
		c.servingAt.Store(time.Now().UnixNano())

		m.mu.Lock()
		transitionGenerationLocked(c, statusRunning)
		c.proxyTarget = proxy.NewTarget(fmt.Sprintf("localhost:%d", c.port))
		c.readyOnce.Do(func() { close(c.ready) })
		if current, ok := m.processes[c.version.Name]; ok && current == c {
			m.rebuildRoutes()
		}
		m.mu.Unlock()

		// The monitor owns the flag from here. Its context is scoped to this
		// process attempt and it is awaited after the process exits: a monitor
		// left running through a quick crash-restart could otherwise finish a
		// probe of the dead process and write the answer over the next attempt's.
		monitorCtx, cancelMonitor := context.WithCancel(ctx)
		monitorDone := make(chan struct{})
		go func() {
			defer close(monitorDone)
			m.watchChildReadiness(monitorCtx, c)
		}()

		err = proc.Wait()
		cancelMonitor()
		<-monitorDone

		select {
		case <-ctx.Done():
			return
		default:
		}

		slog.Error("child exited", "version", c.version.Name, "error", err)

		m.mu.Lock()
		restart := c.restart
		transitionGenerationFailureLocked(c)
		if current, ok := m.processes[c.version.Name]; ok && current == c {
			m.rebuildRoutes()
		}
		m.mu.Unlock()
		if !restart {
			return
		}

		if time.Since(lastStart) > 60*time.Second {
			backoff = time.Second
		}

		slog.Info("restarting child after backoff", "version", c.version.Name, "backoff", backoff)

		if !m.waitForRestartBackoff(ctx, c, backoff) {
			return
		}

		backoff *= 2
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
	}
}

func (m *Manager) waitForRestartBackoff(ctx context.Context, c *child, backoff time.Duration) bool {
	timer := time.NewTimer(backoff)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
	}

	m.mu.Lock()
	restart := c.restart
	m.mu.Unlock()
	return restart
}

// childEnv sets per-child env vars for devshardd (and testapp in e2e).
// binaryLogVersion is normally the link-time build id from --print-binary-version
// (e.g. 0.2.13-v2-r2). Legacy binaries without that flag use the governance
// slot name (e.g. v2) instead.
func childEnv(binaryLogVersion, adminAddr string, haDeployment *bool) []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "DEVSHARD_ADMIN_ADDR=") ||
			(haDeployment != nil && strings.HasPrefix(entry, envHADeployment+"=")) {
			continue
		}
		env = append(env, entry)
	}
	if binaryLogVersion != "" {
		env = append(env, fmt.Sprintf("DEVSHARD_BINARY_LOG_VERSION=%s", binaryLogVersion))
	}
	if adminAddr != "" {
		env = append(env, fmt.Sprintf("DEVSHARD_ADMIN_ADDR=%s", adminAddr))
	}
	if haDeployment != nil {
		env = append(env, fmt.Sprintf("%s=%t", envHADeployment, *haDeployment))
	}
	return env
}

func (m *Manager) childStopTimeout() time.Duration {
	return m.cfg.ChildShutdownGrace
}

func (m *Manager) rollingOverlapAllowed(versionName string, old *child, newMode string) bool {
	name := strings.ToLower(m.cfg.BinaryName)
	if name != "devshard" && name != "devshardd" {
		return true
	}

	m.mu.Lock()
	oldMode := ""
	if old != nil {
		oldMode = old.storageMode
	}
	m.mu.Unlock()

	if oldMode != storageModePostgres {
		slog.Warn(
			"rolling overlap disabled: running devshard storage mode is not postgres",
			"version", versionName,
			"storage_mode", oldMode,
		)
		return false
	}
	if newMode != storageModePostgres {
		slog.Warn(
			"rolling overlap disabled: new devshard storage mode is not postgres",
			"version", versionName,
			"storage_mode", newMode,
		)
		return false
	}
	return true
}

func waitForChildReady(ctx context.Context, c *child) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ready:
		return nil
	case <-c.done:
		return fmt.Errorf("child exited before readiness")
	}
}

func childDone(c *child) bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *child) lifecyclePort() int {
	if adminPort := int(c.adminPort.Load()); adminPort != 0 {
		return adminPort
	}
	return c.port
}

func (c *child) adminAddr() string {
	adminPort := int(c.adminPort.Load())
	if adminPort == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", childLoopbackHost, adminPort)
}

// waitForChildServingReady gates the Starting -> Running transition. Modern
// devshardd children must be logically ready on their admin listener and also
// serve health checks on the public listener that receives proxied traffic.
func waitForChildServingReady(ctx context.Context, c *child, path string, timeout time.Duration) bool {
	adminPort := int(c.adminPort.Load())
	if adminPort == 0 {
		return waitForReadiness(
			ctx,
			timeout,
			func(probeCtx context.Context, client *http.Client) bool {
				ready, viaLegacy := readyEndpointReady(probeCtx, client, c.port, path, true)
				if viaLegacy {
					c.noteLegacyFallback(path)
				}
				return ready
			},
		)
	}
	return waitForReadiness(ctx, timeout, func(probeCtx context.Context, client *http.Client) bool {
		ready, _ := readyEndpointReady(probeCtx, client, adminPort, path, false)
		return ready && publicEndpointReady(probeCtx, client, c.port)
	})
}

func waitForReadiness(
	ctx context.Context,
	timeout time.Duration,
	probe func(context.Context, *http.Client) bool,
) bool {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for {
		if probe(probeCtx, client) {
			return true
		}
		retry := time.NewTimer(100 * time.Millisecond)
		select {
		case <-probeCtx.Done():
			retry.Stop()
			return false
		case <-retry.C:
		}
	}
}

// readyEndpointReady reports readiness and whether the answer came through the
// legacy /healthz fallback. Logging that fallback is the caller's job: this runs
// once a second per child from the monitor, and only the caller knows the
// generation it belongs to.
func readyEndpointReady(ctx context.Context, client *http.Client, port int, path string, allowLegacy bool) (ready, viaLegacy bool) {
	readyPath := normalizeHTTPPath(path)
	status, err := getHTTPStatus(ctx, client, port, readyPath)
	if err != nil {
		return false, false
	}
	if status == http.StatusOK {
		return true, false
	}
	if allowLegacy && legacyReadyFallbackAllowed(readyPath, status) && legacyReady(ctx, client, port) {
		return true, true
	}
	return false, false
}

func publicEndpointReady(ctx context.Context, client *http.Client, port int) bool {
	status, err := getHTTPStatus(ctx, client, port, "/healthz")
	return err == nil && status >= http.StatusOK && status < http.StatusMultipleChoices
}

func getHTTPStatus(ctx context.Context, client *http.Client, port int, path string) (int, error) {
	url := fmt.Sprintf("http://%s:%d%s", childLoopbackHost, port, normalizeHTTPPath(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func legacyReadyFallbackAllowed(path string, status int) bool {
	if path != "/ready" {
		return false
	}
	switch status {
	case 0, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func legacyReady(ctx context.Context, client *http.Client, port int) bool {
	url := fmt.Sprintf("http://%s:%d/healthz", childLoopbackHost, port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return tcpReady(ctx, port)
	}
	resp, err := client.Do(req)
	if err == nil {
		status := resp.StatusCode
		resp.Body.Close()
		if status >= 200 && status < 300 {
			return true
		}
		if status != http.StatusNotFound && status != http.StatusMethodNotAllowed && status != http.StatusNotImplemented {
			return false
		}
	}
	return tcpReady(ctx, port)
}

func tcpReady(ctx context.Context, port int) bool {
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", childLoopbackHost, port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func normalizeHTTPPath(path string) string {
	if path == "" {
		return "/"
	}
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}

func (m *Manager) requestDrain(ctx context.Context, c *child) error {
	lifecyclePort := c.lifecyclePort()
	url := fmt.Sprintf("http://%s:%d%s", childLoopbackHost, lifecyclePort, normalizeHTTPPath(m.cfg.DrainPath))
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("drain request returned %d", resp.StatusCode)
	}
	return nil
}

func (m *Manager) drainAfterProxy(c *child, proxyDrained <-chan struct{}) {
	// Proxy admission and child lifecycle draining share one safety deadline.
	deadline := time.Now().Add(m.cfg.DrainTimeout)
	timer := time.NewTimer(m.cfg.DrainTimeout)
	defer timer.Stop()
	select {
	case <-c.done:
		return
	case <-proxyDrained:
		slog.Info("proxy requests drained", "version", c.version.Name, "port", c.port)
	case <-timer.C:
		slog.Warn("proxy drain timeout reached", "version", c.version.Name, "port", c.port)
	}
	m.mu.Lock()
	transitionGenerationLocked(c, statusDraining)
	m.mu.Unlock()
	if err := m.requestDrain(context.Background(), c); err != nil {
		slog.Warn("drain request failed", "version", c.version.Name, "port", c.port, "error", err)
	}
	m.drainAndStopBefore(c, deadline)
}

func (m *Manager) drainAndStopBefore(c *child, deadline time.Time) {
	for {
		select {
		case <-c.done:
			return
		default:
		}
		inflight, err := m.fetchInflight(context.Background(), c)
		if errors.Is(err, errLegacyDrainStatus) {
			slog.Warn(
				"drain status endpoint unavailable; using legacy drain grace",
				"version", c.version.Name,
				"port", c.port,
				"grace", m.cfg.DrainKillGrace,
				"error", err,
			)
			select {
			case <-c.done:
				return
			case <-time.After(m.cfg.DrainKillGrace):
			}
			break
		}
		if err == nil && inflight == 0 {
			slog.Info("draining child is idle", "version", c.version.Name, "port", c.port)
			break
		}
		if time.Now().After(deadline) {
			if err != nil {
				slog.Warn("drain timeout reached with unreadable drain status", "version", c.version.Name, "port", c.port, "error", err)
			} else {
				slog.Warn("drain timeout reached with in-flight work", "version", c.version.Name, "port", c.port, "inflight", inflight)
			}
			break
		}
		if err != nil {
			slog.Warn("drain status failed; retrying", "version", c.version.Name, "port", c.port, "error", err)
		}
		select {
		case <-c.done:
			return
		case <-time.After(m.cfg.DrainPollInterval):
		}
	}
	m.mu.Lock()
	transitionGenerationLocked(c, statusStopping)
	m.mu.Unlock()
	c.Stop()
	waitForChild(c)
}

func retireProxyTarget(c *child) <-chan struct{} {
	if c.proxyTarget != nil {
		return c.proxyTarget.Retire()
	}
	drained := make(chan struct{})
	close(drained)
	return drained
}

// retireChildForStopStartLocked atomically withdraws one expected generation
// before a replacement that cannot overlap it. New proxy requests immediately
// reload a route table without this target; requests that already acquired it
// remain counted by the returned lease channel. Manager.mu must be held.
func (m *Manager) retireChildForStopStartLocked(c *child) (<-chan struct{}, bool) {
	current, ok := m.processes[c.version.Name]
	if !ok || current != c {
		return nil, false
	}
	transitionGenerationLocked(c, statusRetiring)
	c.restart = false
	delete(m.processes, c.version.Name)
	m.appendDrainingLocked(c)
	m.rebuildRoutes()
	return retireProxyTarget(c), true
}

// stopRetiredChild waits only for requests already admitted by versiond. The
// configured timeout remains the safety boundary; after it, the child's own
// graceful shutdown is the final chance for an unusually long request.
func (m *Manager) stopRetiredChild(
	ctx context.Context,
	c *child,
	proxyDrained <-chan struct{},
) error {
	timer := time.NewTimer(m.cfg.DrainTimeout)
	defer timer.Stop()
	select {
	case <-c.done:
		m.mu.Lock()
		m.removeDrainingLocked(c)
		m.mu.Unlock()
		return nil
	case <-proxyDrained:
		slog.Info("proxy requests drained before stop/start", "version", c.version.Name, "port", c.port)
	case <-ctx.Done():
		go m.drainAfterProxy(c, proxyDrained)
		return ctx.Err()
	case <-timer.C:
		slog.Warn("proxy drain timeout reached before stop/start", "version", c.version.Name, "port", c.port)
	}

	m.mu.Lock()
	transitionGenerationLocked(c, statusStopping)
	m.mu.Unlock()
	c.Stop()
	if err := waitForChildContext(ctx, c); err != nil {
		return err
	}
	// Production runChild removes this entry before closing done. Keeping this
	// idempotent cleanup here also makes the helper's lifecycle contract explicit.
	m.mu.Lock()
	m.removeDrainingLocked(c)
	m.mu.Unlock()
	return nil
}

func (m *Manager) fetchInflight(ctx context.Context, c *child) (int64, error) {
	return m.fetchInflightAt(ctx, c.version.Name, c.port, c.lifecyclePort())
}

func (m *Manager) fetchInflightAt(ctx context.Context, versionName string, port, lifecyclePort int) (int64, error) {
	url := fmt.Sprintf("http://%s:%d%s", childLoopbackHost, lifecyclePort, normalizeHTTPPath(m.cfg.DrainStatusPath))
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound ||
		resp.StatusCode == http.StatusMethodNotAllowed ||
		resp.StatusCode == http.StatusNotImplemented {
		return 0, fmt.Errorf("%w: drain status returned %d", errLegacyDrainStatus, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("drain status returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	var status struct {
		Inflight *int64 `json:"inflight"`
		Active   *int64 `json:"active"`
		Count    *int64 `json:"count"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return 0, err
	}
	switch {
	case status.Inflight != nil:
		return *status.Inflight, nil
	case status.Active != nil:
		return *status.Active, nil
	case status.Count != nil:
		return *status.Count, nil
	default:
		return 0, fmt.Errorf("drain status missing inflight count for %s on port %d", versionName, port)
	}
}

func (m *Manager) removeDrainingLocked(target *child) {
	children := m.draining[target.version.Name]
	for i, c := range children {
		if c != target {
			continue
		}
		children = append(children[:i], children[i+1:]...)
		if len(children) == 0 {
			delete(m.draining, target.version.Name)
		} else {
			m.draining[target.version.Name] = children
		}
		return
	}
}

// rebuildRoutes publishes running children, then retires replaced targets so a
// stale proxy lookup either owns a counted lease or retries against the new map.
// Must be called with m.mu held.
func (m *Manager) rebuildRoutes() {
	previous := m.routes.Load().(proxy.RouteTable)
	routes := make(proxy.RouteTable)
	for _, c := range m.processes {
		if c.status == statusRunning {
			if c.proxyTarget == nil {
				c.proxyTarget = proxy.NewTarget(fmt.Sprintf("localhost:%d", c.port))
			}
			routes[c.version.Name] = c.proxyTarget
		}
	}
	m.routes.Store(routes)
	if len(routes) > 0 {
		select {
		case m.available <- struct{}{}:
		default:
		}
	}
	for version, target := range previous {
		if routes[version] != target {
			target.Retire()
		}
	}
}
