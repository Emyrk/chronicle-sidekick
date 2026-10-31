package engine_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Emyrk/chronicle-sidekick/internal/engine"
	"github.com/Emyrk/chronicle-sidekick/internal/sidekick"
	"github.com/Emyrk/chronicle-sidekick/internal/store"
	"github.com/Emyrk/chronicle-sidekick/internal/upload"
	"github.com/Emyrk/chronicle-sidekick/internal/watch"
	"github.com/Emyrk/chronicle-sidekick/internal/wowdir"
)

type harness struct {
	e       *engine.Engine
	st      *store.Store
	logPath string

	mu                     sync.Mutex
	running                bool
	runningInstalls        map[string]bool
	runningInstallFn       func(wowdir.Install) bool
	token                  string
	results                []error // consumed one per Upload call
	res                    upload.Result
	uploads                int
	sources                []string // the source id of each Upload call
	destinations           []string // Chronicle tenant URL of each Upload call
	fast                   []bool   // the fast flag of each Upload call
	baseURL                string   // tenant URL returned with the test install
	installs               []wowdir.Install
	installsFn             func() []wowdir.Install
	autoUpload             func(wowdir.Install) watch.Trigger
	captureSidekickStorage bool
	appendGroupKey         func(string) string
	uploadPaths            []string
	uploadRequests         []engine.UploadRequest
	lookups                int           // pre-flight source lookups made
	gate                   chan struct{} // when set, Upload blocks until it is closed
	notified               []string
	statuses               []engine.Status
}

func newHarness(t testing.TB) *harness { return newHarnessToken(t, nil) }

// newHarnessToken replaces the keychain read when tokenFn is non-nil.
func newHarnessToken(t testing.TB, tokenFn func() string) *harness {
	return newHarnessLogger(t, tokenFn, nil)
}

// newHarnessLogger also sets the engine's logger when logger is non-nil.
func newHarnessLogger(t testing.TB, tokenFn func() string, logger *slog.Logger) *harness {
	return newHarnessLookup(t, tokenFn, logger, nil)
}

// newHarnessLookup also wires the pre-flight source lookup when lookup is
// non-nil; nil leaves Config.Lookup unset, as the real uploader does.
func newHarnessLookup(t testing.TB, tokenFn func() string, logger *slog.Logger, lookup func(sourceID string) (upload.Have, error)) *harness {
	t.Helper()
	dir := t.TempDir()
	logDir := filepath.Join(dir, "_x_", "Logs")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	h := &harness{
		logPath: filepath.Join(logDir, "WoWCombatLog.txt"), token: "tok", running: true,
		captureSidekickStorage: true,
	}
	require.NoError(t, os.WriteFile(h.logPath, []byte("some combat"), 0o644))
	st, err := store.Open(filepath.Join(dir, "state.json"))
	require.NoError(t, err)
	h.st = st
	var lookupFn func(context.Context, string, string, string) (upload.Have, error)
	if lookup != nil {
		lookupFn = func(_ context.Context, _, _, sourceID string) (upload.Have, error) {
			h.mu.Lock()
			h.lookups++
			h.mu.Unlock()
			return lookup(sourceID)
		}
	}
	h.e = engine.New(engine.Config{
		Store:  st,
		Logger: logger,
		Installs: func() []wowdir.Install {
			if h.installsFn != nil {
				return h.installsFn()
			}
			if h.installs != nil {
				return h.installs
			}
			return []wowdir.Install{{Dir: filepath.Dir(logDir), LogDir: logDir, BaseURL: h.baseURL}}
		},
		AutoUpload: func(install wowdir.Install) watch.Trigger {
			if h.autoUpload != nil {
				return h.autoUpload(install)
			}
			return watch.Trigger{}
		},
		CaptureSidekickStorage: func(wowdir.Install) bool { return h.captureSidekickStorage },
		AppendGroupKey: func(_ wowdir.Install, path string) string {
			if h.appendGroupKey != nil {
				return h.appendGroupKey(path)
			}
			return ""
		},
		Token: func() string {
			if tokenFn != nil {
				return tokenFn()
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.token
		},
		Lookup: lookupFn,
		Upload: func(_ context.Context, _ string, req engine.UploadRequest) (upload.Result, error) {
			h.mu.Lock()
			h.uploads++
			h.uploadPaths = append(h.uploadPaths, req.Path)
			h.uploadRequests = append(h.uploadRequests, req)
			h.sources = append(h.sources, req.SourceID)
			h.destinations = append(h.destinations, req.BaseURL)
			h.fast = append(h.fast, req.Fast)
			gate := h.gate
			var err error
			if len(h.results) > 0 {
				err = h.results[0]
				h.results = h.results[1:]
			}
			res := h.res
			h.mu.Unlock()
			if gate != nil {
				<-gate
			}
			return res, err
		},
		Running: func(context.Context, []string) bool { h.mu.Lock(); defer h.mu.Unlock(); return h.running },
		RunningInstall: func(_ context.Context, install wowdir.Install) bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.runningInstallFn != nil {
				return h.runningInstallFn(install)
			}
			if h.runningInstalls != nil {
				return h.runningInstalls[install.Dir]
			}
			return h.running
		},
		Notify:    func(title, _ string) { h.notified = append(h.notified, title) },
		OnStatus:  func(s engine.Status) { h.statuses = append(h.statuses, s) },
		RetryBase: time.Millisecond,
		RetryMax:  time.Millisecond,
		// Five 1 ms waits, so a 5xx loop makes six attempts.
		ServerRetryFor: 5 * time.Millisecond,
	})
	return h
}

// step polls and then waits for the upload worker to finish what was queued.
func (h *harness) step(t *testing.T, now time.Time, manual bool) {
	t.Helper()
	h.e.Step(context.Background(), now, manual)
	require.Eventually(t, func() bool { return !h.e.Busy() }, 5*time.Second, time.Millisecond)
}

func pathWithinTest(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func TestEngineRoutesUploadToInstallTenant(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.baseURL = "https://octo.chronicleclassic.com"
	h.step(t, time.Now(), true)
	require.Equal(t, []string{h.baseURL}, h.destinations)
}

func TestEngineRefreshesTenantImmediatelyBeforeUpload(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	installDir := filepath.Dir(filepath.Dir(h.logPath))
	logDir := filepath.Dir(h.logPath)
	var calls atomic.Int32
	h.installsFn = func() []wowdir.Install {
		baseURL := "https://old.example"
		if calls.Add(1) > 1 {
			baseURL = "https://new.example"
		}
		return []wowdir.Install{{Dir: installDir, LogDir: logDir, BaseURL: baseURL}}
	}

	h.step(t, time.Now(), true)

	require.GreaterOrEqual(t, calls.Load(), int32(2))
	require.Equal(t, []string{"https://new.example"}, h.destinations)
}

func TestEngineUploadsOnlySelectedInstall(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	firstDir := filepath.Dir(filepath.Dir(h.logPath))
	root := filepath.Dir(firstDir)
	secondDir := filepath.Join(root, "_y_")
	secondLogDir := filepath.Join(secondDir, "Logs")
	secondLog := filepath.Join(secondLogDir, "WoWCombatLog.txt")
	require.NoError(t, os.MkdirAll(secondLogDir, 0o755))
	require.NoError(t, os.WriteFile(secondLog, []byte("second combat"), 0o644))
	h.installs = []wowdir.Install{
		{Dir: firstDir, LogDir: filepath.Dir(h.logPath), BaseURL: "https://first.example"},
		{Dir: secondDir, LogDir: secondLogDir, BaseURL: "https://second.example"},
	}

	h.e.StepFor(context.Background(), time.Now(), []string{secondDir})
	require.Eventually(t, func() bool { return !h.e.Busy() }, 5*time.Second, time.Millisecond)
	require.Len(t, h.uploadPaths, 1)
	require.True(t, pathWithinTest(h.uploadPaths[0], filepath.Join(secondDir, "ChronicleSidekick")))
	require.Equal(t, []string{"https://second.example"}, h.destinations)
}

func TestEngineUploadsOnlyOnManualRequest(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.res = upload.Result{ReportURL: "https://x/logs/1"}
	t0 := time.Now()

	h.step(t, t0, false)
	require.Equal(t, 0, h.uploads)
	require.Equal(t, engine.StateNoLog, h.statuses[len(h.statuses)-1].State)

	// Log growth followed by a long quiet period does not upload automatically.
	require.NoError(t, os.WriteFile(h.logPath, []byte("some combat and more"), 0o644))
	h.step(t, t0.Add(time.Minute), false)
	h.step(t, t0.Add(10*time.Minute), false)
	require.Equal(t, 0, h.uploads)

	// Exiting WoW does not upload automatically either.
	h.mu.Lock()
	h.running = false
	h.mu.Unlock()
	h.step(t, t0.Add(11*time.Minute), false)
	require.Equal(t, 0, h.uploads)

	// The user's Upload now request sends the latest log.
	h.step(t, t0.Add(11*time.Minute+5*time.Second), true)
	require.Equal(t, 1, h.uploads)
	last := h.statuses[len(h.statuses)-1]
	require.Equal(t, engine.StateUploaded, last.State)
	require.Equal(t, "https://x/logs/1", last.ReportURL)
	require.Equal(t, []string{"Log uploaded"}, h.notified)
	_, tracked := h.st.Get().Uploaded[h.logPath]
	require.False(t, tracked, "a removed live path must not suppress a future same-sized log")
	require.NoFileExists(t, h.logPath)

	// Nothing more is sent without another request.
	h.step(t, t0.Add(12*time.Minute), false)
	require.Equal(t, 1, h.uploads)
}

func TestEngineAutoUploadsConfiguredInstallOnGameExit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	firstDir := filepath.Dir(filepath.Dir(h.logPath))
	root := filepath.Dir(firstDir)
	secondDir := filepath.Join(root, "_y_")
	secondLogDir := filepath.Join(secondDir, "Logs")
	secondLog := filepath.Join(secondLogDir, "WoWCombatLog.txt")
	require.NoError(t, os.MkdirAll(secondLogDir, 0o755))
	require.NoError(t, os.WriteFile(secondLog, []byte("second combat"), 0o644))
	h.installs = []wowdir.Install{
		{Dir: firstDir, LogDir: filepath.Dir(h.logPath), BaseURL: "https://first.example"},
		{Dir: secondDir, LogDir: secondLogDir, BaseURL: "https://second.example"},
	}
	h.autoUpload = func(install wowdir.Install) watch.Trigger {
		return watch.Trigger{OnExit: install.Dir == secondDir}
	}
	h.runningInstalls = map[string]bool{firstDir: true, secondDir: true}

	t0 := time.Now()
	h.step(t, t0, false)
	require.Empty(t, h.uploadPaths)

	h.mu.Lock()
	h.runningInstalls[secondDir] = false
	h.mu.Unlock()
	h.step(t, t0.Add(time.Minute), false)
	require.Empty(t, h.uploadPaths)
	require.Equal(t, engine.StateFinalizing, h.statuses[len(h.statuses)-1].State)
	h.step(t, t0.Add(time.Minute+29*time.Second), false)
	require.Empty(t, h.uploadPaths)
	h.step(t, t0.Add(time.Minute+30*time.Second), false)
	require.Len(t, h.uploadPaths, 1)
	require.True(t, pathWithinTest(h.uploadPaths[0], filepath.Join(secondDir, "ChronicleSidekick")))
	require.NoFileExists(t, secondLog)
	require.Equal(t, []string{"https://second.example"}, h.destinations)
}

func TestEngineRetriesOfflineThenSucceeds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running = false
	h.results = []error{errors.New("dial tcp: no route to host"), errors.New("dial tcp: no route to host"), nil}

	h.step(t, time.Now(), true)
	require.Equal(t, 3, h.uploads)
	require.Equal(t, engine.StateUploaded, h.statuses[len(h.statuses)-1].State)
}

func TestEngineDuplicate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running = false
	h.res = upload.Result{Duplicate: true, ReportURL: "https://x/logs/file/abc"}

	h.step(t, time.Now(), true)
	require.Equal(t, []string{"Already uploaded"}, h.notified)
}

func TestEngineReconnect(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running = false
	h.token = ""
	t0 := time.Now()

	h.step(t, t0, true)
	require.Equal(t, 0, h.uploads)
	require.Equal(t, engine.StateReconnect, h.statuses[len(h.statuses)-1].State)

	// Signing in uploads the manually queued log that was waiting.
	h.token = "fresh"
	h.e.TokenChanged()
	h.step(t, t0.Add(5*time.Second), false)
	require.Equal(t, 1, h.uploads)
	require.Equal(t, engine.StateUploaded, h.statuses[len(h.statuses)-1].State)
}

func TestEngineRejectedToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running = false
	h.results = []error{upload.ErrUnauthorized}
	t0 := time.Now()

	h.step(t, t0, true)
	// The worker wakes the poll loop, which shows the reconnect state.
	h.step(t, t0.Add(5*time.Second), false)
	require.Equal(t, engine.StateReconnect, h.statuses[len(h.statuses)-1].State)
	require.Contains(t, h.statuses[len(h.statuses)-1].Detail, "Needs reconnect")
	h.step(t, t0.Add(10*time.Second), false)
	require.Equal(t, 1, h.uploads) // not hammered while the token is unchanged
}

func TestEngineRejectedTokenRetriesSameSnapshotAfterReconnect(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running = false
	h.results = []error{upload.ErrUnauthorized, nil}
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	h.step(t, t0, true)
	require.Len(t, h.uploadRequests, 1)
	first := h.uploadRequests[0]
	h.token = "fresh"
	h.e.TokenChanged()
	h.step(t, t0.Add(time.Second), false)

	require.Len(t, h.uploadRequests, 2)
	require.Equal(t, first.Path, h.uploadRequests[1].Path)
	require.Equal(t, first.UploadID, h.uploadRequests[1].UploadID)
}

func TestEngineTokenCached(t *testing.T) {
	t.Parallel()
	reads := 0
	h := newHarnessToken(t, func() string { reads++; return "tok" })
	h.running = false
	t0 := time.Now()

	for i := 0; i < 100; i++ {
		h.step(t, t0.Add(time.Duration(i)*5*time.Second), false)
	}
	require.Equal(t, 1, reads)

	h.e.TokenChanged()
	h.step(t, t0.Add(time.Hour), false)
	require.Equal(t, 2, reads)
}

func TestEngineGivesUpOnServerErrors(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running = false
	unavailable := &upload.Error{Status: 503, Message: "Service Unavailable"}
	for i := 0; i < 20; i++ {
		h.results = append(h.results, unavailable)
	}
	t0 := time.Now()

	h.step(t, t0, true)
	require.Equal(t, 6, h.uploads)
	require.Equal(t, []string{"Chronicle upload failed"}, h.notified)
	require.Equal(t, []sidekick.Issue{{
		Code: "upload_failed", Message: "A combat log upload failed.",
		Details: "Chronicle returned HTTP 503 (Service Unavailable).",
	}}, h.st.Get().Issues)

	// Failed for this size: nothing is sent until the log changes.
	h.step(t, t0.Add(10*time.Minute), false)
	require.Equal(t, 6, h.uploads)

	h.results = nil
	require.NoError(t, os.WriteFile(h.logPath, []byte("some combat and more"), 0o644))
	h.step(t, t0.Add(20*time.Minute), false) // sees the growth
	h.step(t, t0.Add(30*time.Minute), true)  // the user retries the changed log
	require.Equal(t, 7, h.uploads)
	require.Empty(t, h.st.Get().Issues)
}

func TestEngineStepDoesNotWaitForUpload(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.gate = make(chan struct{})
	ctx := context.Background()
	t0 := time.Now()

	h.e.Step(ctx, t0, false)
	h.e.Step(ctx, t0.Add(time.Second), true) // Upload now while WoW runs; blocks in Upload
	require.Eventually(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.uploads == 1 }, 5*time.Second, time.Millisecond)

	// The poll loop keeps going: WoW exits and the session is noticed, and
	// repeated polls do not queue the same log again.
	h.mu.Lock()
	h.running = false
	h.mu.Unlock()
	h.e.Step(ctx, t0.Add(10*time.Minute), false)
	h.e.Step(ctx, t0.Add(10*time.Minute+5*time.Second), true)
	close(h.gate)
	require.Eventually(t, func() bool { return !h.e.Busy() }, 5*time.Second, time.Millisecond)

	require.Equal(t, 1, h.uploads)
	require.Equal(t, []bool{true}, h.fast) // the game was running
	last := h.statuses[len(h.statuses)-1]
	require.Equal(t, engine.StateUploaded, last.State)
	require.Equal(t, "Your 10m session is on Chronicle.", last.Detail)
}

func TestSelectAppendGroupUsesFifteenMinuteRestartWindowPerInstall(t *testing.T) {
	t.Parallel()
	exit := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	groups := map[string]store.ArchiveGroup{
		"first": {ID: "first", InstallDirectory: "/wow/a", SourceKey: "_thrall.txt", LogID: "log-1", LastExitAt: exit},
		"other": {ID: "other", InstallDirectory: "/wow/b", SourceKey: "_jaina.txt", LogID: "log-2", LastExitAt: exit.Add(time.Minute)},
	}
	group, ok := engine.SelectAppendGroup(groups, "/wow/a", "_thrall.txt", exit.Add(15*time.Minute))
	require.True(t, ok)
	require.Equal(t, "first", group.ID)
	_, ok = engine.SelectAppendGroup(groups, "/wow/a", "_jaina.txt", exit.Add(2*time.Minute))
	require.False(t, ok)
	_, ok = engine.SelectAppendGroup(groups, "/wow/a", "_thrall.txt", exit.Add(15*time.Minute+time.Nanosecond))
	require.False(t, ok)
	group, ok = engine.SelectAppendGroup(groups, "/wow/b", "_jaina.txt", exit.Add(2*time.Minute))
	require.True(t, ok)
	require.Equal(t, "other", group.ID)
}

func TestArchiveCombatPathUsesDeterministicCollisionSuffix(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, time.January, 2, 6, 15, 4, 0, time.Local)
	first := filepath.Join(dir, "Jan_02_26_06_15_CombatLog.txt")
	second := filepath.Join(dir, "Jan_02_26_06_15_2_CombatLog.txt")
	require.NoError(t, os.WriteFile(first, nil, 0o600))
	require.Equal(t, second, engine.ArchiveCombatPath(dir, now))
	require.NoError(t, os.WriteFile(second, nil, 0o600))
	require.Equal(t, filepath.Join(dir, "Jan_02_26_06_15_3_CombatLog.txt"), engine.ArchiveCombatPath(dir, now))
}

func TestEngineSnapshotsBeforeUploadAndMovesStoppedLog(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running = false
	h.res = upload.Result{LogID: "log-1", ReportURL: "https://x/logs/1"}
	t0 := time.Date(2026, time.January, 2, 6, 15, 4, 0, time.Local)

	h.step(t, t0, true)

	require.Len(t, h.uploadRequests, 1)
	req := h.uploadRequests[0]
	require.NoFileExists(t, h.logPath)
	require.FileExists(t, req.Path)
	require.Equal(t, "Jan_02_26_06_15_CombatLog.txt", filepath.Base(req.Path))
	groups := h.st.Get().ArchiveGroups
	require.Len(t, groups, 1)
	for _, group := range groups {
		require.False(t, group.Pending)
		require.Equal(t, "log-1", group.LogID)
		require.Equal(t, []string{req.Path}, group.Files)
		require.Equal(t, t0.Add(7*24*time.Hour), group.DeleteAfter)
	}
}

func TestEngineRechecksInstallBeforeRemovingLiveLog(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var calls atomic.Int32
	h.runningInstallFn = func(wowdir.Install) bool {
		return calls.Add(1) > 1 // stopped during poll, restarted before snapshot preparation
	}
	t0 := time.Date(2026, time.January, 2, 6, 15, 4, 0, time.Local)

	h.step(t, t0, true)

	require.GreaterOrEqual(t, calls.Load(), int32(2))
	require.FileExists(t, h.logPath)
	require.Len(t, h.uploadPaths, 1)
	require.FileExists(t, h.uploadPaths[0])
}

func TestEngineManualUploadCopiesRunningLog(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	t0 := time.Date(2026, time.January, 2, 6, 15, 4, 0, time.Local)

	h.step(t, t0, true)

	require.FileExists(t, h.logPath)
	require.Len(t, h.uploadPaths, 1)
	require.FileExists(t, h.uploadPaths[0])
	require.NotEqual(t, h.logPath, h.uploadPaths[0])
}

func TestEngineSnapshotsNewestSidecar(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	wowDir := filepath.Dir(filepath.Dir(h.logPath))
	oldPath := filepath.Join(wowDir, "WTF", "Account", "old", "SavedVariables", "ChronicleSidekickBridgeWoTLK.lua")
	newPath := filepath.Join(wowDir, "WTF", "Account", "new", "SavedVariables", "ChronicleSidekickBridgeWoTLK.lua")
	require.NoError(t, os.MkdirAll(filepath.Dir(oldPath), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(newPath), 0o755))
	require.NoError(t, os.WriteFile(oldPath, []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(newPath, []byte("new"), 0o644))
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(oldPath, t0, t0))
	require.NoError(t, os.Chtimes(newPath, t0.Add(time.Minute), t0.Add(time.Minute)))

	h.step(t, t0.Add(time.Hour), true)

	require.Len(t, h.uploadRequests, 1)
	storage := h.uploadRequests[0].SidekickStoragePath
	require.FileExists(t, storage)
	body, err := os.ReadFile(storage)
	require.NoError(t, err)
	require.NoFileExists(t, newPath)
	require.FileExists(t, oldPath)
	require.Equal(t, "new", string(body))
	require.Equal(t, filepath.Dir(h.uploadRequests[0].Path), filepath.Dir(storage))
}

func TestEngineSkipsSidecarWhenInstallDoesNotUseArtifacts(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.captureSidekickStorage = false
	wowDir := filepath.Dir(filepath.Dir(h.logPath))
	sidecar := filepath.Join(wowDir, "WTF", "Account", "account", "SavedVariables", "ChronicleSidekickBridgeWoTLK.lua")
	require.NoError(t, os.MkdirAll(filepath.Dir(sidecar), 0o755))
	require.NoError(t, os.WriteFile(sidecar, []byte("untouched"), 0o644))

	h.step(t, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), true)

	require.Len(t, h.uploadRequests, 1)
	require.Empty(t, h.uploadRequests[0].SidekickStoragePath)
	require.FileExists(t, sidecar)
	groups := h.st.Get().ArchiveGroups
	require.Len(t, groups, 1)
	for _, group := range groups {
		require.Equal(t, []string{h.uploadRequests[0].Path}, group.Files)
	}
}

func TestEngineUploadsCombatLogWhenOptionalSidecarCannotBeCopied(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	wowDir := filepath.Dir(filepath.Dir(h.logPath))
	brokenSidecar := filepath.Join(wowDir, "WTF", "Account", "broken", "SavedVariables", "ChronicleSidekickBridgeWoTLK.lua")
	require.NoError(t, os.MkdirAll(brokenSidecar, 0o755))

	h.step(t, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), true)

	require.Len(t, h.uploadRequests, 1)
	require.Empty(t, h.uploadRequests[0].SidekickStoragePath)
	require.FileExists(t, h.uploadRequests[0].Path)
}

func TestEngineAppendsAfterRestartWithinWindow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.autoUpload = func(wowdir.Install) watch.Trigger { return watch.Trigger{OnExit: true} }
	h.res = upload.Result{LogID: "log-1", ReportURL: "https://x/logs/1"}
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	h.step(t, t0, false)
	h.mu.Lock()
	h.running = false
	h.mu.Unlock()
	h.step(t, t0.Add(time.Minute), false)
	h.step(t, t0.Add(time.Minute+30*time.Second), false)
	require.Len(t, h.uploadRequests, 1)
	require.Empty(t, h.uploadRequests[0].AppendLogID)

	require.NoError(t, os.WriteFile(h.logPath, []byte("next combat"), 0o644))
	h.mu.Lock()
	h.running = true
	h.mu.Unlock()
	h.step(t, t0.Add(5*time.Minute), false)
	h.mu.Lock()
	h.running = false
	h.results = []error{errors.New("dial tcp: retry"), nil}
	h.mu.Unlock()
	h.step(t, t0.Add(6*time.Minute), false)
	h.step(t, t0.Add(6*time.Minute+30*time.Second), false)

	require.Len(t, h.uploadRequests, 3)
	firstTry, retry := h.uploadRequests[1], h.uploadRequests[2]
	require.Equal(t, "log-1", firstTry.AppendLogID)
	require.NotEmpty(t, firstTry.UploadID)
	require.Equal(t, 1, firstTry.ChunkSequence)
	require.Equal(t, firstTry.Path, retry.Path)
	require.Equal(t, firstTry.UploadID, retry.UploadID)
	require.Equal(t, firstTry.ChunkSequence, retry.ChunkSequence)
	require.Len(t, h.st.Get().ArchiveGroups, 1)
}

func TestEngineStartsNewGroupForDifferentAppendKey(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	key := "_thrall.txt"
	h.appendGroupKey = func(string) string { return key }
	h.autoUpload = func(wowdir.Install) watch.Trigger { return watch.Trigger{OnExit: true} }
	h.res = upload.Result{LogID: "log-1", ReportURL: "https://x/logs/1"}
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	h.step(t, t0, false)
	h.mu.Lock()
	h.running = false
	h.mu.Unlock()
	h.step(t, t0.Add(time.Minute), false)
	h.step(t, t0.Add(time.Minute+30*time.Second), false)
	require.Len(t, h.uploadRequests, 1)
	require.Len(t, h.st.Get().ArchiveGroups, 1)
	for _, group := range h.st.Get().ArchiveGroups {
		require.Equal(t, "_thrall.txt", group.SourceKey)
	}

	require.NoError(t, os.WriteFile(h.logPath, []byte("next combat"), 0o644))
	key = "_jaina.txt"
	h.mu.Lock()
	h.running = true
	h.mu.Unlock()
	h.step(t, t0.Add(5*time.Minute), false)
	h.mu.Lock()
	h.running = false
	h.mu.Unlock()
	h.step(t, t0.Add(6*time.Minute), false)
	h.step(t, t0.Add(6*time.Minute+30*time.Second), false)

	require.Len(t, h.uploadRequests, 2)
	require.Empty(t, h.uploadRequests[1].AppendLogID)
	require.Len(t, h.st.Get().ArchiveGroups, 2)
}

func TestEngineArchiveRetention(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
	}{
		{name: "enabled", enabled: true},
		{name: "disabled", enabled: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.running = false
			require.NoError(t, h.st.Update(func(s *store.State) {
				s.Config = sidekick.SidekickConfig{
					Version:        sidekick.CurrentConfigVersion,
					Archive:        sidekick.ArchivePolicy{Enabled: tc.enabled, RetentionDays: 7},
					WoWDirectories: map[string]sidekick.WoWDirectoryConfig{},
				}
			}))
			t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			h.step(t, t0, true)
			require.Len(t, h.uploadPaths, 1)
			if tc.enabled {
				require.FileExists(t, h.uploadPaths[0])
				h.step(t, t0.Add(7*24*time.Hour-time.Second), false)
				require.FileExists(t, h.uploadPaths[0])
				h.step(t, t0.Add(7*24*time.Hour), false)
				require.NoFileExists(t, h.uploadPaths[0])
			} else {
				require.NoFileExists(t, h.uploadPaths[0])
				require.Len(t, h.st.Get().ArchiveGroups, 1, "metadata survives the append window")
				h.step(t, t0.Add(15*time.Minute-time.Second), false)
				require.Len(t, h.st.Get().ArchiveGroups, 1)
				h.step(t, t0.Add(15*time.Minute), false)
				require.Empty(t, h.st.Get().ArchiveGroups)
			}
		})
	}
}

func TestEngineFailedUploadPreservesPendingArchive(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.running = false
	h.results = []error{&upload.Error{Status: 400, Message: "bad log"}}

	h.step(t, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), true)

	require.Len(t, h.uploadPaths, 1)
	require.FileExists(t, h.uploadPaths[0])
	groups := h.st.Get().ArchiveGroups
	require.Len(t, groups, 1)
	for _, group := range groups {
		require.True(t, group.Pending)
		require.Zero(t, group.DeleteAfter)
	}
}

func TestEngineRecoversPendingArchiveAfterRestart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wowDir := filepath.Join(root, "wow")
	archiveDir := filepath.Join(wowDir, "ChronicleSidekick", "group-1")
	require.NoError(t, os.MkdirAll(archiveDir, 0o755))
	combatPath := filepath.Join(archiveDir, "Jan_01_26_12_00_CombatLog.txt")
	require.NoError(t, os.WriteFile(combatPath, []byte("some combat"), 0o600))
	st, err := store.Open(filepath.Join(root, "state.json"))
	require.NoError(t, err)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, st.Update(func(s *store.State) {
		s.ArchiveGroups = map[string]store.ArchiveGroup{
			"group-1": {
				ID: "group-1", InstallDirectory: wowDir, InstallID: "wowdir-1",
				CreatedAt: now, Pending: true, Files: []string{combatPath},
			},
		}
	}))

	requests := make(chan engine.UploadRequest, 1)
	e := engine.New(engine.Config{
		Store: st,
		Installs: func() []wowdir.Install {
			return []wowdir.Install{{Dir: wowDir, LogDir: filepath.Join(wowDir, "Logs"), BaseURL: "https://x"}}
		},
		Token: func() string { return "tok" },
		Upload: func(_ context.Context, _ string, req engine.UploadRequest) (upload.Result, error) {
			requests <- req
			return upload.Result{LogID: "log-1", ReportURL: "https://x/logs/1"}, nil
		},
		Running:        func(context.Context, []string) bool { return false },
		RunningInstall: func(context.Context, wowdir.Install) bool { return false },
		Notify:         func(string, string) {},
	})
	e.Step(context.Background(), now.Add(time.Minute), false)
	require.Eventually(t, func() bool { return !e.Busy() }, 5*time.Second, time.Millisecond)

	select {
	case req := <-requests:
		require.Equal(t, combatPath, req.Path)
	case <-time.After(5 * time.Second):
		t.Fatal("pending archive was not uploaded")
	}
	group := st.Get().ArchiveGroups["group-1"]
	require.False(t, group.Pending)
	require.Equal(t, "log-1", group.LogID)
}

func TestEngineWaitsForFirstFightAfterNewLog(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	t0 := time.Now()
	last := func() engine.State { return h.statuses[len(h.statuses)-1].State }

	// The log present before WoW started does not mean logging is on.
	h.step(t, t0, false)
	require.Equal(t, engine.StateNoLog, last())
	h.step(t, t0.Add(5*time.Second), false)
	require.Equal(t, engine.StateNoLog, last())

	// /combatlog creates a new, empty file.
	fresh := filepath.Join(filepath.Dir(h.logPath), "WoWCombatLog-101426_120000.txt")
	require.NoError(t, os.WriteFile(fresh, nil, 0o644))
	h.step(t, t0.Add(10*time.Second), false)
	require.Equal(t, engine.StateWaiting, last())

	// The first fight flushes events.
	require.NoError(t, os.WriteFile(fresh, []byte("first fight"), 0o644))
	h.step(t, t0.Add(15*time.Second), false)
	require.Equal(t, engine.StateRecording, last())
}
