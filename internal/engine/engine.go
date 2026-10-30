// Package engine ties detection, watching and uploading together and reports
// what the tray should show.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/Emyrk/chronicle-sidekick/internal/sidekick"
	"github.com/Emyrk/chronicle-sidekick/internal/store"
	"github.com/Emyrk/chronicle-sidekick/internal/upload"
	"github.com/Emyrk/chronicle-sidekick/internal/watch"
	"github.com/Emyrk/chronicle-sidekick/internal/wowdir"
)

// State is what the tray icon shows.
type State int

const (
	// StateIdle: waiting for WoW.
	StateIdle State = iota
	// StateNoLog: WoW is running but its combat log is not growing.
	StateNoLog
	// StateWaiting: a combat log was created this session but has not grown yet.
	StateWaiting
	// StateRecording: the combat log is growing.
	StateRecording
	// StateFinalizing: WoW exited and Sidekick is waiting for the log to settle.
	StateFinalizing
	// StateUploading: a log is being sent (or waiting to retry offline).
	StateUploading
	// StateUploaded: a log was just uploaded.
	StateUploaded
	// StateReconnect: the Chronicle sign-in is missing or was rejected.
	StateReconnect
)

// Status is a snapshot for the tray.
type Status struct {
	State State
	// Detail is a short human sentence for the menu.
	Detail string
	// ReportURL is the most recent report, if any.
	ReportURL string
}

// Config wires the engine to the outside world.
type Config struct {
	Store *store.Store
	// Installs returns the flavor folders to watch right now.
	Installs func() []wowdir.Install
	// AutoUpload returns the automatic trigger implemented for one install.
	AutoUpload func(wowdir.Install) watch.Trigger
	// Token reads the bearer token from the keychain. It is cached; call
	// Engine.TokenChanged after storing or removing one.
	Token func() string
	// Upload sends one immutable archived snapshot. Fast asks for the cheapest
	// compression; it is set while WoW is running so the game never competes for CPU.
	Upload func(ctx context.Context, token string, req UploadRequest) (upload.Result, error)
	// Lookup asks the selected tenant what it has of sourceID before a body is sent.
	// It is only called for a log the ledger has never seen (fresh install,
	// wiped state file, second machine). nil disables the pre-flight check: an
	// unknown log is then uploaded and the server's duplicate answer recovers
	// the report link.
	Lookup func(ctx context.Context, token, baseURL, sourceID string) (upload.Have, error)
	// Running reports whether any WoW client is running.
	Running func(ctx context.Context, installDirs []string) bool
	// RunningInstall reports whether one specific WoW installation is running.
	// When set, it provides the per-directory lifecycle used by auto-upload.
	RunningInstall func(context.Context, wowdir.Install) bool
	// Notify shows a native notification.
	Notify func(title, message string)
	// OnStatus is called when the status changes.
	OnStatus func(Status)
	Logger   *slog.Logger

	Poll      time.Duration // default 5s
	IdleAfter time.Duration // default 5m
	// RetryBase is the first offline retry delay; it doubles up to RetryMax.
	RetryBase time.Duration // default 5s
	RetryMax  time.Duration // default 5m
	// ServerRetryFor bounds how long a log is retried against a server that
	// keeps answering 5xx (waiting RetryMax between tries) before it is marked
	// failed. Offline retries have no bound.
	ServerRetryFor time.Duration // default 1h
}

// UploadRequest describes an immutable archive snapshot and its Chronicle target.
type UploadRequest struct {
	BaseURL             string
	Path                string
	SourceID            string
	Size                int64
	Fast                bool
	SidekickStoragePath string
	AppendLogID         string
	UploadID            string
	ChunkSequence       int
}

// Engine is the uploader's brain. Create with New and call Run.
type Engine struct {
	cfg     Config
	tracker *watch.Tracker
	manual  chan uploadRequest
	wake    chan struct{}
	dirty   atomic.Bool // token changed behind our back; re-read it

	status        Status
	rejectedToken string
	token         string // cached keychain value
	tokenRead     time.Time
	tokenLoaded   bool
	uploadedUntil time.Time
	now           time.Time // start of the current step
	lastUploaded  Status
	watching      map[string]bool // log files already announced
	wowUp         bool
	sessionStart  time.Time
	lastSession   time.Duration                              // length of the most recent WoW session
	wait          func(ctx context.Context, d time.Duration) // overridden in tests

	// mu guards everything the poll loop and the upload worker share: tracker,
	// status and the fields the worker reads (token, wowUp, now, ...).
	mu             sync.Mutex
	pending        []job
	blocked        []job // immutable snapshots waiting for a refreshed token
	current        *job
	kick           chan struct{} // wakes the worker when pending grows
	workerOnce     sync.Once
	installRunning map[string]bool
	installSeen    map[string]bool
	installStarted map[string]time.Time
	installExited  map[string]time.Time
}

type uploadRequest struct {
	// dirs is empty for every configured install.
	dirs []string
}

// job is one log waiting to be (or being) uploaded.
type job struct {
	path          string
	size          int64
	baseURL       string
	install       wowdir.Install
	running       bool
	startedAt     time.Time
	exitedAt      time.Time
	queuedAt      time.Time
	snapshotPath  string
	storagePath   string
	groupID       string
	appendLogID   string
	uploadID      string
	sequence      int
	sourceRemoved bool
}

const exitFinalizeAfter = 30 * time.Second
const appendRestartWindow = 15 * time.Minute

const uploadedStickiness = time.Minute

// New returns an Engine.
func New(cfg Config) *Engine {
	if cfg.Poll == 0 {
		cfg.Poll = 5 * time.Second
	}
	if cfg.IdleAfter == 0 {
		cfg.IdleAfter = 5 * time.Minute
	}
	if cfg.RetryBase == 0 {
		cfg.RetryBase = 5 * time.Second
	}
	if cfg.RetryMax == 0 {
		cfg.RetryMax = 5 * time.Minute
	}
	if cfg.ServerRetryFor == 0 {
		cfg.ServerRetryFor = time.Hour
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	st := pruneUploaded(cfg.Store, cfg.Logger)
	e := &Engine{
		cfg:            cfg,
		tracker:        watch.NewTracker(st.Uploaded),
		manual:         make(chan uploadRequest, 1),
		wake:           make(chan struct{}, 1),
		kick:           make(chan struct{}, 1),
		status:         Status{ReportURL: st.LastReportURL},
		installRunning: map[string]bool{},
		installSeen:    map[string]bool{},
		installStarted: map[string]time.Time{},
		installExited:  map[string]time.Time{},
		wait: func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		},
	}
	e.restorePendingArchives(st)
	return e
}

func (e *Engine) restorePendingArchives(st store.State) {
	if e.cfg.Installs == nil {
		return
	}
	installs := e.cfg.Installs()
	for _, group := range st.ArchiveGroups {
		if !group.Pending {
			continue
		}
		var combatPath, storagePath string
		for _, path := range group.Files {
			if _, err := os.Stat(path); err != nil {
				continue
			}
			if filepath.Base(path) == "ChronicleSidekickBridgeWoTLK.lua" {
				storagePath = path
			} else if strings.HasSuffix(filepath.Base(path), "_CombatLog.txt") {
				combatPath = path
			}
		}
		if combatPath == "" {
			continue
		}
		info, err := os.Stat(combatPath)
		if err != nil || info.Size() == 0 {
			continue
		}
		var install wowdir.Install
		for _, candidate := range installs {
			if candidate.Dir == group.InstallDirectory {
				install = candidate
				break
			}
		}
		if install.Dir == "" {
			continue
		}
		j := job{
			path: combatPath, size: info.Size(), baseURL: install.BaseURL, install: install,
			queuedAt: group.CreatedAt, exitedAt: group.LastExitAt,
			snapshotPath: combatPath, storagePath: storagePath, groupID: group.ID,
			sourceRemoved: true,
		}
		if group.LogID != "" {
			j.appendLogID = group.LogID
			j.uploadID = group.UploadID
			j.sequence = group.ChunkSequence
		}
		e.blocked = append(e.blocked, j)
	}
}

// pruneUploaded forgets uploaded logs that are gone or are archives, so the
// state file and the tracker do not grow with every session. Archives are not
// watched, and a log that no longer exists will never be seen again.
func pruneUploaded(s *store.Store, log *slog.Logger) store.State {
	st := s.Get()
	var stale []string
	for p := range st.Uploaded {
		if _, err := os.Stat(p); err != nil || watch.IsArchive(p) {
			stale = append(stale, p)
		}
	}
	if len(stale) == 0 {
		return st
	}
	err := s.Update(func(s *store.State) {
		for _, p := range stale {
			delete(s.Uploaded, p)
		}
	})
	if err != nil {
		log.Warn("saving state", "err", err)
	}
	log.Info("forgot uploaded logs that are gone", "count", len(stale))
	return s.Get()
}

// UploadNow asks every configured WoW install for logs with new data.
func (e *Engine) UploadNow() { e.requestUpload(nil) }

// UploadNowFor asks only the installs rooted at dirs for logs with new data.
func (e *Engine) UploadNowFor(dirs []string) {
	if len(dirs) == 0 {
		return
	}
	e.requestUpload(dirs)
}

func (e *Engine) requestUpload(dirs []string) {
	req := uploadRequest{dirs: append([]string(nil), dirs...)}
	select {
	case e.manual <- req:
	default:
	}
}

// TokenChanged tells the engine the stored token was replaced or removed. It
// re-reads the token and re-evaluates now, without the "nothing new" notice of
// UploadNow.
func (e *Engine) TokenChanged() {
	e.dirty.Store(true)
	e.mu.Lock()
	if len(e.blocked) > 0 {
		e.pending = append(e.pending, e.blocked...)
		e.blocked = nil
		select {
		case e.kick <- struct{}{}:
		default:
		}
	}
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Run polls until ctx is done.
func (e *Engine) Run(ctx context.Context) {
	tick := time.NewTicker(e.cfg.Poll)
	defer tick.Stop()
	var manual *uploadRequest
	for {
		e.stepSelected(ctx, time.Now(), manual)
		manual = nil
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-e.wake:
		case req := <-e.manual:
			manual = &req
		}
	}
}

func (e *Engine) step(ctx context.Context, now time.Time, manual bool) {
	var req *uploadRequest
	if manual {
		req = &uploadRequest{}
	}
	e.stepSelected(ctx, now, req)
}

func (e *Engine) stepSelected(ctx context.Context, now time.Time, manual *uploadRequest) {
	e.workerOnce.Do(func() { go e.worker(ctx) })
	e.cleanupArchives(now)
	installs := e.cfg.Installs()
	var patterns, installDirs []string
	for i := range installs {
		in := &installs[i]
		if len(in.LogGlobs) == 0 && in.LogDir != "" {
			in.LogGlobs = []string{
				filepath.Join(in.LogDir, watch.LiveLogName),
				filepath.Join(in.LogDir, "WoWCombatLog-*.txt"),
			}
		}
		patterns = append(patterns, in.LogGlobs...)
		installDirs = append(installDirs, in.Dir)
	}

	files := watch.Files(patterns)
	triggers := make(map[string]watch.Trigger, len(files))
	installByFile := make(map[string]wowdir.Install, len(files))
	for _, f := range files {
		for _, in := range installs {
			if pathWithin(f.Path, in.Dir) {
				installByFile[f.Path] = in
				trigger := watch.Trigger{IdleAfter: e.cfg.IdleAfter}
				if e.cfg.AutoUpload != nil {
					trigger = e.cfg.AutoUpload(in)
					trigger.IdleAfter = e.cfg.IdleAfter
					if trigger.OnExit {
						trigger.FinalizeAfter = exitFinalizeAfter
					}
				}
				triggers[f.Path] = trigger
				break
			}
		}
	}
	runningByDir := make(map[string]bool, len(installs))
	running := false
	if e.cfg.RunningInstall != nil {
		for _, install := range installs {
			isRunning := e.cfg.RunningInstall(ctx, install)
			runningByDir[install.Dir] = isRunning
			running = running || isRunning
		}
	} else {
		running = e.cfg.Running(ctx, installDirs)
		for _, install := range installs {
			runningByDir[install.Dir] = running
		}
	}
	for _, f := range files {
		if !e.watching[f.Path] {
			if e.watching == nil {
				e.watching = map[string]bool{}
			}
			e.watching[f.Path] = true
			e.cfg.Logger.Info("watching", "file", f.Path)
		}
	}
	var selectFile func(watch.File) bool
	selectedFiles := len(files)
	if manual != nil {
		if len(manual.dirs) == 0 {
			selectFile = func(watch.File) bool { return true }
		} else {
			selectedFiles = 0
			selectFile = func(f watch.File) bool {
				for _, dir := range manual.dirs {
					if pathWithin(f.Path, dir) {
						selectedFiles++
						return true
					}
				}
				return false
			}
		}
	}

	e.mu.Lock()
	e.now = now
	e.trackSession(now, running)
	e.trackInstalls(now, installs, runningByDir)
	queue, recording := e.tracker.ObserveConfigured(now, files, selectFile, func(f watch.File) watch.Trigger {
		return triggers[f.Path]
	}, func(f watch.File) bool {
		return runningByDir[installByFile[f.Path].Dir]
	})
	logging := running && e.tracker.CreatedSince(e.sessionStart)
	e.mu.Unlock()

	if manual != nil && len(queue) == 0 {
		msg := "Nothing new to upload."
		if selectedFiles == 0 {
			msg = "No combat log yet. Type /combatlog in WoW to start one."
		}
		e.cfg.Notify("Chronicle", msg)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	token := e.currentToken(now)
	needAuth := token == "" || token == e.rejectedToken
	if !needAuth {
		if len(e.blocked) > 0 {
			e.pending = append(e.pending, e.blocked...)
			e.blocked = nil
			select {
			case e.kick <- struct{}{}:
			default:
			}
		}
		e.enqueue(queue, installByFile)
	}
	if e.busy() {
		return // the worker owns the status until it is done
	}
	rejected := token != "" && token == e.rejectedToken
	e.setStatus(e.baseline(now, len(installs) > 0, running, recording, e.tracker.Finalizing(), logging, needAuth, rejected))
}

// currentDestination re-reads the configured installs immediately before an
// upload starts. Legacy installs derive BaseURL from realmlist.wtf, so a realm
// change made after the job was queued is honored without restarting Sidekick.
func (e *Engine) currentDestination(path string) (string, bool) {
	for _, install := range e.cfg.Installs() {
		if pathWithin(path, install.Dir) {
			return install.BaseURL, true
		}
	}
	return "", false
}

func pathWithin(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// trackSession logs WoW starting and exiting and remembers how long it ran.
func (e *Engine) trackSession(now time.Time, running bool) {
	switch {
	case running && !e.wowUp:
		e.sessionStart = now
		e.cfg.Logger.Info("wow started")
	case !running && e.wowUp:
		e.lastSession = now.Sub(e.sessionStart)
		e.cfg.Logger.Info("wow exited", "session", e.lastSession.Round(time.Minute))
	}
	e.wowUp = running
}

func (e *Engine) trackInstalls(now time.Time, installs []wowdir.Install, running map[string]bool) {
	for _, install := range installs {
		dir := install.Dir
		up := running[dir]
		if !e.installSeen[dir] {
			e.installSeen[dir] = true
			e.installRunning[dir] = up
			if up {
				e.installStarted[dir] = now
			} else {
				e.installExited[dir] = now
			}
			continue
		}
		was := e.installRunning[dir]
		switch {
		case up && !was:
			e.installStarted[dir] = now
		case !up && was:
			e.installExited[dir] = now
		}
		e.installRunning[dir] = up
	}
}

// sessionLength is the current session if WoW is running, else the last one.
func (e *Engine) sessionLength() time.Duration {
	if e.wowUp {
		return e.now.Sub(e.sessionStart)
	}
	return e.lastSession
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dm", m)
}

func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// reconnectRecheck bounds how often a missing or rejected token is re-read
// (it may have been written by another process).
const reconnectRecheck = time.Minute

// currentToken returns the cached token. The keychain is only read at the
// start, after TokenChanged, and at most once per reconnectRecheck while no
// usable token is held; reading it can mean spawning a process.
func (e *Engine) currentToken(now time.Time) string {
	reconnect := e.token == "" || e.token == e.rejectedToken
	if e.dirty.Swap(false) || !e.tokenLoaded || (reconnect && now.Sub(e.tokenRead) >= reconnectRecheck) {
		e.token = e.cfg.Token()
		e.tokenRead = now
		e.tokenLoaded = true
	}
	return e.token
}

func (e *Engine) baseline(now time.Time, haveInstall, running, recording, finalizing, logging, needAuth, rejected bool) Status {
	last := e.status.ReportURL
	switch {
	case rejected:
		return Status{State: StateReconnect, Detail: "Needs reconnect. Connect to Chronicle again.", ReportURL: last}
	case needAuth:
		return Status{State: StateReconnect, Detail: "Not connected. Connect to Chronicle to upload your logs.", ReportURL: last}
	case now.Before(e.uploadedUntil):
		return e.lastUploaded
	case finalizing:
		return Status{State: StateFinalizing, Detail: "Finalizing your combat log…", ReportURL: last}
	case recording:
		return Status{State: StateRecording, Detail: "Recording your combat log.", ReportURL: last}
	case running && logging:
		return Status{State: StateWaiting, Detail: "Combat logging is on. Waiting for your first fight.", ReportURL: last}
	case running:
		return Status{State: StateNoLog, Detail: "Not logging yet. Type /combatlog in game, then fight something — the dot turns green after your first combat.", ReportURL: last}
	case !haveInstall:
		return Status{State: StateIdle, Detail: "Can't find WoW. Choose its folder from the menu.", ReportURL: last}
	}
	return Status{State: StateIdle, Detail: "Waiting for WoW.", ReportURL: last}
}

// enqueue hands queue to the worker. Observe keeps returning a file until the
// worker marks it, so a file that is already queued (or in flight at the same
// size) is skipped. Callers hold e.mu.
func (e *Engine) enqueue(queue []watch.File, installs map[string]wowdir.Install) {
	for _, f := range queue {
		install := installs[f.Path]
		j := job{
			path: f.Path, size: f.Size, baseURL: install.BaseURL, install: install,
			running: e.installRunning[install.Dir], startedAt: e.installStarted[install.Dir],
			exitedAt: e.installExited[install.Dir], queuedAt: e.now,
		}
		switch {
		case e.current != nil && e.current.path == f.Path && e.current.size == f.Size:
		case e.replacePending(j):
		default:
			e.pending = append(e.pending, j)
		}
	}
	if len(e.pending) > 0 {
		select {
		case e.kick <- struct{}{}:
		default:
		}
	}
}

// replacePending refreshes an already queued path before its snapshot exists.
// Once a snapshot has been prepared, its immutable size and append identity stay fixed.
func (e *Engine) replacePending(j job) bool {
	for i := range e.pending {
		if e.pending[i].path == j.path {
			if e.pending[i].snapshotPath == "" {
				e.pending[i] = j
			}
			return true
		}
	}
	return false
}

// busy reports whether the worker has anything queued or in flight. Callers
// hold e.mu.
func (e *Engine) busy() bool { return e.current != nil || len(e.pending) > 0 }

// worker uploads queued logs one at a time so the poll loop never blocks.
func (e *Engine) worker(ctx context.Context) {
	for {
		e.mu.Lock()
		if len(e.pending) == 0 {
			e.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-e.kick:
			}
			continue
		}
		j := e.pending[0]
		e.pending = e.pending[1:]
		e.current = &j
		e.mu.Unlock()

		rejected, j := e.uploadOne(ctx, j)

		e.mu.Lock()
		e.current = nil
		if rejected {
			e.blocked = append(e.blocked, j)
			e.pending = nil
		}
		e.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		// Give the memory used by the gzip and transfer buffers back.
		debug.FreeOSMemory()
		select {
		case e.wake <- struct{}{}:
		default:
		}
	}
}

func (e *Engine) prepareSnapshot(ctx context.Context, j *job) error {
	st := e.cfg.Store.Get()
	group, appendExisting := selectAppendGroup(st.ArchiveGroups, j.install.Dir, j.startedAt)
	if !appendExisting {
		group = store.ArchiveGroup{
			ID: uuid.NewString(), InstallDirectory: j.install.Dir,
			InstallID: sidekick.DirectoryID(j.install.Dir), CreatedAt: j.queuedAt,
			UploadID: uuid.NewString(),
		}
	} else {
		group.ChunkSequence++
		if group.UploadID == "" {
			group.UploadID = uuid.NewString()
		}
	}
	if !j.exitedAt.IsZero() {
		group.LastExitAt = j.exitedAt
	}
	group.Pending = true
	group.DeleteAfter = time.Time{}

	dir := filepath.Join(j.install.Dir, "ChronicleSidekick", group.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create archive directory: %w", err)
	}
	combatPath := archiveCombatPath(dir, j.queuedAt)
	group.Files = append(group.Files, combatPath)
	sidecarSource, sidecarErr := newestSidecarPath(j.install.Dir)
	storagePath := ""
	if sidecarErr != nil {
		e.cfg.Logger.Warn("finding optional Sidekick storage", "err", sidecarErr)
	} else if sidecarSource != "" {
		storagePath = filepath.Join(dir, filepath.Base(sidecarSource))
		group.Files = append(group.Files, storagePath)
	}
	if err := e.cfg.Store.Update(func(s *store.State) {
		if s.ArchiveGroups == nil {
			s.ArchiveGroups = map[string]store.ArchiveGroup{}
		}
		s.ArchiveGroups[group.ID] = group
	}); err != nil {
		return fmt.Errorf("persist archive group: %w", err)
	}

	// The process may restart after the finalization poll but before the worker
	// reaches this job. Copy in that case so an open writer cannot follow a
	// renamed inode into the archive.
	running := j.running
	if e.cfg.RunningInstall != nil {
		running = e.cfg.RunningInstall(ctx, j.install)
	} else if e.cfg.Running != nil {
		running = e.cfg.Running(ctx, []string{j.install.Dir})
	}
	if err := copySnapshot(j.path, combatPath, j.size); err != nil {
		return fmt.Errorf("copy combat log snapshot: %w", err)
	}
	if !running {
		info, err := os.Stat(j.path)
		if err != nil {
			return fmt.Errorf("stat combat log before removal: %w", err)
		}
		if info.Size() != j.size {
			return fmt.Errorf("%s changed before it could be archived", j.path)
		}
		if err := os.Remove(j.path); err != nil {
			return fmt.Errorf("remove archived combat log: %w", err)
		}
		j.sourceRemoved = true
		e.mu.Lock()
		e.tracker.Forget(j.path)
		e.mu.Unlock()
	}

	if sidecarSource != "" {
		if err := os.Rename(sidecarSource, storagePath); err != nil {
			e.cfg.Logger.Warn("moving optional Sidekick storage into archive", "file", sidecarSource, "err", err)
			storagePath = ""
		}
	}
	j.snapshotPath = combatPath
	j.storagePath = storagePath
	j.groupID = group.ID
	if appendExisting {
		j.appendLogID = group.LogID
		j.uploadID = group.UploadID
		j.sequence = group.ChunkSequence
	}
	return nil
}

func selectAppendGroup(groups map[string]store.ArchiveGroup, installDir string, startedAt time.Time) (store.ArchiveGroup, bool) {
	var best store.ArchiveGroup
	for _, group := range groups {
		if group.InstallDirectory != installDir || group.Pending || group.LogID == "" || group.LastExitAt.IsZero() || startedAt.IsZero() {
			continue
		}
		restartedAfter := startedAt.Sub(group.LastExitAt)
		if restartedAfter < 0 || restartedAfter > appendRestartWindow {
			continue
		}
		if best.ID == "" || group.LastExitAt.After(best.LastExitAt) {
			best = group
		}
	}
	return best, best.ID != ""
}

func archiveCombatPath(dir string, now time.Time) string {
	stem := now.In(time.Local).Format("Jan_02_06_15_04")
	path := filepath.Join(dir, stem+"_CombatLog.txt")
	for suffix := 2; ; suffix++ {
		if _, err := os.Lstat(path); err != nil {
			return path
		}
		path = filepath.Join(dir, fmt.Sprintf("%s_%d_CombatLog.txt", stem, suffix))
	}
}

func copySnapshot(from, to string, size int64) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = dst.Close()
		if !ok {
			_ = os.Remove(to)
		}
	}()
	written, err := io.CopyN(dst, src, size)
	if err != nil {
		return err
	}
	if written != size {
		return fmt.Errorf("copied %d of %d bytes", written, size)
	}
	if err := dst.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func newestSidecarPath(wowDir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(wowDir, "WTF", "Account", "*", "SavedVariables", "ChronicleSidekickBridgeWoTLK.lua"))
	if err != nil || len(matches) == 0 {
		return "", err
	}
	var newest string
	var newestInfo os.FileInfo
	for _, path := range matches {
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		if newest == "" || info.ModTime().After(newestInfo.ModTime()) {
			newest, newestInfo = path, info
		}
	}
	if newest == "" {
		return "", errors.New("no regular Sidekick storage file found")
	}
	return newest, nil
}

// uploadOne sends one log, retrying as the failure allows. It returns true when
// the server rejected the token.
//
// Connectivity errors are retried forever with a growing delay; a dial that
// fails never starts compressing because the multipart pipe blocks until the
// request is being written. A 5xx is retried every RetryMax for ServerRetryFor
// and then marked failed. Any other 4xx fails at once.
func (e *Engine) uploadOne(ctx context.Context, j job) (rejected bool, prepared job) {
	log := e.cfg.Logger.With("file", j.path, "size", j.size)
	baseURL, ok := e.currentDestination(j.path)
	if !ok {
		err := fmt.Errorf("cannot determine the Chronicle tenant; check this WoW folder's realmlist.wtf")
		log.Error("refreshing upload destination", "err", err)
		e.fail(j, err)
		return false, j
	}
	j.baseURL = baseURL
	j.install.BaseURL = baseURL
	if j.snapshotPath == "" {
		if err := e.prepareSnapshot(ctx, &j); err != nil {
			log.Error("preparing archive snapshot", "err", err)
			e.fail(j, err)
			return false, j
		}
	}
	log = e.cfg.Logger.With("file", j.snapshotPath, "size", j.size, "archive_group", j.groupID)

	src, err := upload.Identify(j.snapshotPath)
	if err != nil {
		// Without an id there is no ledger to consult; upload as before.
		log.Debug("could not identify log", "err", err)
	}
	ledgerID := sourceLedgerID(j.baseURL, src)
	if src != "" && e.alreadyUploaded(ctx, log, j, src, ledgerID) {
		return false, j
	}
	delay := e.cfg.RetryBase
	var serverWaited time.Duration
	log.Info("uploading", "file", j.path, "size", formatSize(j.size))
	for {
		e.mu.Lock()
		token, fast := e.token, e.wowUp
		if token == "" || token == e.rejectedToken {
			// Signed out while retrying; the tracker still has it queued.
			e.mu.Unlock()
			return false, j
		}
		e.setStatus(Status{State: StateUploading, Detail: "Uploading your log…", ReportURL: e.status.ReportURL})
		e.mu.Unlock()

		res, err := e.cfg.Upload(ctx, token, UploadRequest{
			BaseURL: j.baseURL, Path: j.snapshotPath, SourceID: src, Size: j.size, Fast: fast,
			SidekickStoragePath: j.storagePath, AppendLogID: j.appendLogID,
			UploadID: j.uploadID, ChunkSequence: j.sequence,
		})
		var serverErr *upload.Error
		switch {
		case err == nil:
			e.finish(j, ledgerID, res)
			return false, j
		case ctx.Err() != nil:
			return false, j
		case errors.Is(err, upload.ErrUnauthorized):
			log.Warn("token rejected")
			e.mu.Lock()
			e.rejectedToken = token
			e.mu.Unlock()
			e.cfg.Notify("Chronicle", "Chronicle needs you to sign in again.")
			return true, j
		case errors.As(err, &serverErr) && upload.Retryable(err):
			if serverWaited >= e.cfg.ServerRetryFor {
				log.Error("giving up on server errors", "err", err, "waited", serverWaited)
				e.fail(j, err)
				return false, j
			}
			log.Warn("server error, will retry", "err", err, "in", e.cfg.RetryMax)
			e.retryStatus(fmt.Sprintf("Chronicle is having trouble. Retrying in %s…", e.cfg.RetryMax.Round(time.Second)))
			e.wait(ctx, e.cfg.RetryMax)
			serverWaited += e.cfg.RetryMax
		case upload.Retryable(err):
			log.Warn("upload failed, will retry", "err", err, "in", delay)
			e.retryStatus(fmt.Sprintf("Can't reach Chronicle. Retrying in %s…", delay.Round(time.Second)))
			e.wait(ctx, delay)
			delay = min(delay*2, e.cfg.RetryMax)
		default:
			log.Error("upload failed", "err", err)
			e.fail(j, err)
			return false, j
		}
	}
}

func sourceLedgerID(baseURL, sourceID string) string {
	if baseURL == "" || sourceID == "" {
		return sourceID
	}
	return strings.TrimRight(baseURL, "/") + "|" + sourceID
}

// alreadyUploaded reports whether Chronicle already has j's bytes, recording
// the skip when it does. The ledger answers first; a source id it has never
// seen is asked of the server, when a pre-flight lookup is configured.
func (e *Engine) alreadyUploaded(ctx context.Context, log *slog.Logger, j job, src, ledgerID string) bool {
	if ent, ok := e.cfg.Store.Get().Logs[ledgerID]; ok {
		// A grown log (ent.Size < j.size) is sent again in full under the same
		// source id, which is how the server replaces the group it parsed.
		if ent.Size >= j.size {
			e.skip(j, ledgerID, ent.Size, ent.ReportURL)
			return true
		}
		return false
	}
	if e.cfg.Lookup == nil {
		return false
	}
	e.mu.Lock()
	token := e.token
	e.mu.Unlock()
	have, err := e.cfg.Lookup(ctx, token, j.baseURL, src)
	if err != nil {
		// Offline, or a server without the route: upload and let its duplicate
		// answer sort it out.
		log.Debug("source lookup failed", "err", err)
		return false
	}
	if have.Size < j.size {
		return false
	}
	e.skip(j, ledgerID, have.Size, have.ReportURL)
	return true
}

// skip records a log the server already has without sending it. It is not a
// failure: the tracker marks it uploaded so it is neither retried nor queued
// again, and the tray keeps pointing at the report that exists.
func (e *Engine) skip(j job, src string, size int64, reportURL string) {
	e.mu.Lock()
	e.tracker.MarkUploaded(j.path, j.size)
	now := e.now
	err := e.cfg.Store.Update(func(s *store.State) {
		if s.Uploaded == nil {
			s.Uploaded = map[string]int64{}
		}
		if j.sourceRemoved {
			delete(s.Uploaded, j.path)
		} else {
			s.Uploaded[j.path] = j.size
		}
		s.RecordLog(src, store.Log{Size: max(size, j.size), ReportURL: reportURL, At: now})
		markGroupSuccess(s, j.groupID, "", reportURL, now)
		if reportURL != "" {
			s.LastReportURL = reportURL
		}
	})
	if err != nil {
		e.cfg.Logger.Warn("saving state", "err", err)
	}
	url := reportURL
	if url == "" {
		url = e.status.ReportURL
	}
	e.cfg.Logger.Info("already on Chronicle, not uploading", "file", j.path, "size", formatSize(j.size), "url", url)
	e.lastUploaded = Status{State: StateUploaded, Detail: "This log is already on Chronicle.", ReportURL: url}
	e.uploadedUntil = e.now.Add(uploadedStickiness)
	e.setStatus(e.lastUploaded)
	e.mu.Unlock()
	e.cleanupArchives(now)
}

func (e *Engine) retryStatus(detail string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.setStatus(Status{State: StateUploading, Detail: detail, ReportURL: e.status.ReportURL})
}

// fail records a permanent failure and tells the user once.
func (e *Engine) fail(j job, err error) {
	e.mu.Lock()
	e.tracker.MarkFailed(j.path, j.size)
	e.mu.Unlock()
	e.cfg.Notify("Chronicle upload failed", err.Error())
}

func (e *Engine) finish(f job, src string, res upload.Result) {
	e.mu.Lock()
	e.tracker.MarkUploaded(f.path, f.size)
	now := e.now
	err := e.cfg.Store.Update(func(s *store.State) {
		if s.Uploaded == nil {
			s.Uploaded = map[string]int64{}
		}
		if f.sourceRemoved {
			delete(s.Uploaded, f.path)
		} else {
			s.Uploaded[f.path] = f.size
		}
		if src != "" {
			s.RecordLog(src, store.Log{Size: f.size, ReportURL: res.ReportURL, At: now})
		}
		markGroupSuccess(s, f.groupID, res.LogID, res.ReportURL, now)
		if res.ReportURL != "" {
			s.LastReportURL = res.ReportURL
		}
	})
	if err != nil {
		e.cfg.Logger.Warn("saving state", "err", err)
	}

	url := res.ReportURL
	if url == "" {
		url = e.status.ReportURL
	}
	title, detail := "Log uploaded", "Your log is on Chronicle."
	if res.Duplicate {
		title, detail = "Already uploaded", "This log was already on Chronicle."
		e.cfg.Logger.Info("already uploaded →", "url", url)
	} else {
		e.cfg.Logger.Info("uploaded →", "url", url)
		if d := e.sessionLength(); d >= time.Minute {
			detail = fmt.Sprintf("Your %s session is on Chronicle.", formatDuration(d))
		}
	}
	e.lastUploaded = Status{State: StateUploaded, Detail: detail, ReportURL: url}
	e.uploadedUntil = e.now.Add(uploadedStickiness)
	e.setStatus(e.lastUploaded)
	e.mu.Unlock()
	e.cleanupArchives(now)
	e.cfg.Notify(title, detail)
}

func markGroupSuccess(s *store.State, groupID, logID, reportURL string, now time.Time) {
	group, ok := s.ArchiveGroups[groupID]
	if !ok {
		return
	}
	if logID != "" {
		group.LogID = logID
	}
	if reportURL != "" {
		group.ReportURL = reportURL
	}
	group.Pending = false
	group.LastSuccessAt = now
	policy := s.EffectiveConfig().Archive
	group.DeleteAfter = now
	if policy.Enabled {
		group.DeleteAfter = now.Add(time.Duration(policy.RetentionDays) * 24 * time.Hour)
	}
	s.ArchiveGroups[groupID] = group
}

func (e *Engine) cleanupArchives(now time.Time) {
	st := e.cfg.Store.Get()
	haveCandidate := false
	for _, group := range st.ArchiveGroups {
		if !group.Pending && !group.LastSuccessAt.IsZero() && !group.DeleteAfter.IsZero() && !now.Before(group.DeleteAfter) {
			haveCandidate = true
			break
		}
	}
	if !haveCandidate {
		return
	}
	if err := e.cfg.Store.Update(func(s *store.State) {
		for id, group := range s.ArchiveGroups {
			if group.Pending || group.LastSuccessAt.IsZero() || group.DeleteAfter.IsZero() || now.Before(group.DeleteAfter) {
				continue
			}
			kept := group.Files[:0]
			for _, path := range group.Files {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					e.cfg.Logger.Warn("deleting archived file", "file", path, "err", err)
					kept = append(kept, path)
				}
			}
			group.Files = append([]string(nil), kept...)
			windowEnd := group.LastSuccessAt.Add(appendRestartWindow)
			if !group.LastExitAt.IsZero() && group.LastExitAt.Add(appendRestartWindow).After(windowEnd) {
				windowEnd = group.LastExitAt.Add(appendRestartWindow)
			}
			if len(group.Files) == 0 && !now.Before(windowEnd) {
				delete(s.ArchiveGroups, id)
				_ = os.Remove(filepath.Join(group.InstallDirectory, "ChronicleSidekick", group.ID))
			} else {
				s.ArchiveGroups[id] = group
			}
		}
	}); err != nil {
		e.cfg.Logger.Warn("saving archive cleanup", "err", err)
	}
}

func (e *Engine) setStatus(s Status) {
	if s == e.status {
		return
	}
	e.status = s
	if e.cfg.OnStatus != nil {
		e.cfg.OnStatus(s)
	}
}
