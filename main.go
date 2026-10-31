// Command uploader is a tray app that uploads WoW Forever combat logs to
// Chronicle when the user requests an upload.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/google/uuid"

	"github.com/Emyrk/chronicle-sidekick/internal/engine"
	"github.com/Emyrk/chronicle-sidekick/internal/sidekick"
	"github.com/Emyrk/chronicle-sidekick/internal/store"
	"github.com/Emyrk/chronicle-sidekick/internal/tenant"
	"github.com/Emyrk/chronicle-sidekick/internal/upload"
	"github.com/Emyrk/chronicle-sidekick/internal/watch"
	"github.com/Emyrk/chronicle-sidekick/internal/wowdir"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

// maxLogSize is when uploader.log is rotated at.
const maxLogSize = 4 << 20

func main() {
	if err := run(); err != nil {
		if errors.Is(err, errAlreadyRunning) && !hasArg(os.Args[1:], "--background") && !hasArg(os.Args[1:], "--wait-for-instance") {
			showError("Chronicle Sidekick is already running. Use its tray icon, or quit the existing Sidekick before starting it again.")
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var (
		server          = flag.String("server", envOr("CHRONICLE_URL", ""), "Chronicle server URL; remembered for later launches")
		wowPath         = flag.String("wow-path", "", "WoW folder, flavor folder or executable (replaces saved folders for this launch and later launches)")
		devToken        = flag.String("dev-token", os.Getenv("CHRONICLE_UPLOADER_TOKEN"), "development only: token to store in the keychain instead of pairing (prefer CHRONICLE_UPLOADER_TOKEN; argv is visible to other processes)")
		background      = flag.Bool("background", false, "start in the system tray without showing the startup window")
		waitForInstance = flag.Bool("wait-for-instance", false, "wait for the previous Sidekick process to exit")
		stateDebug      = flag.Bool("state-debug", false, "preview each tray icon state, advancing every 3 seconds")
	)
	flag.Usage = func() {
		_, _ = fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		flag.VisitAll(func(f *flag.Flag) {
			if f.Name != "dev-token" && f.Name != "background" && f.Name != "wait-for-instance" { // hidden internal flags
				_, _ = fmt.Fprintf(flag.CommandLine.Output(), "  -%s\n    \t%s\n", f.Name, f.Usage)
			}
		})
	}
	flag.Parse()

	// Never compete with the game for CPU.
	prioErr := lowerPriority()

	dir, err := store.Dir()
	if err != nil {
		return fmt.Errorf("find config dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lockName := "sidekick.lock"
	if *stateDebug {
		lockName = "sidekick-state-debug.lock"
	}
	lock, err := acquireInstanceWithWait(filepath.Join(dir, lockName), *waitForInstance)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	st, err := store.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		return fmt.Errorf("open state: %w", err)
	}
	installation, err := ensureInstallation(st)
	if err != nil {
		return fmt.Errorf("initialize Sidekick installation: %w", err)
	}

	// There is no terminal for a tray app; log to a file.
	var logger *slog.Logger
	if lf, err := openRotating(filepath.Join(dir, "uploader.log"), maxLogSize); err == nil {
		defer func() { _ = lf.Close() }()
		var w io.Writer = lf
		// Also log to the terminal when started from one.
		if fi, err := os.Stderr.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			w = io.MultiWriter(lf, os.Stderr)
		}
		logger = slog.New(slog.NewTextHandler(w, nil))
	} else {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	if *devToken != "" {
		if err := store.SaveAccessToken(*devToken); err != nil {
			return fmt.Errorf("save token to keychain: %w", err)
		}
	}
	if *wowPath != "" {
		if err := st.Update(func(s *store.State) {
			s.WoWPath = ""
			s.WoWPaths = []string{filepath.Clean(*wowPath)}
			s.WoWPathsSet = true
		}); err != nil {
			return err
		}
	}

	tn := tenant.Resolve(st.Get().Tenant, *server)
	if err := st.Update(func(s *store.State) { s.Tenant = tn }); err != nil {
		return err
	}
	if prioErr != nil {
		logger.Warn("could not lower process priority", "err", prioErr)
	}
	logger.Info("chronicle auth server", "base_url", tn.BaseURL)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	token, err := store.LoadToken()
	if err != nil {
		logger.Warn("load OAuth token", "err", err)
	}
	a := &app{
		server: tn.BaseURL, layout: tn.Layout, store: st, logger: logger, cancel: cancel, token: token,
		installation: installation, background: *background, stateDebug: *stateDebug,
	}
	if *stateDebug {
		a.status = stateDebugStatuses[0]
	}
	if *server != "" {
		a.uploadOverride = tn.BaseURL
	}
	games := map[string]*watch.Game{}
	runningInstall := func(ctx context.Context, install wowdir.Install) bool {
		id := sidekick.DirectoryID(install.Dir)
		game := games[id]
		if game == nil {
			game = watch.NewGameForInstall(tn.Layout, nil)
			games[id] = game
		}
		return game.Running(ctx, []string{install.Dir})
	}
	a.engine = engine.New(engine.Config{
		Store:                  st,
		Installs:               a.installs,
		AutoUpload:             a.autoUploadTrigger,
		CaptureSidekickStorage: a.captureSidekickStorage,
		Token:                  a.cachedToken,
		Upload: func(ctx context.Context, _ string, req engine.UploadRequest) (upload.Result, error) {
			token, err := a.accessToken(ctx)
			if err != nil {
				return upload.Result{}, err
			}
			gameFormat := a.gameFormatForPath(req.Path)
			storagePath := req.SidekickStoragePath
			if !gameFormat.IncludesSidekickStorage() {
				storagePath = ""
			}
			c := upload.Client{
				BaseURL: req.BaseURL, Token: token, Format: gameFormat.UploadFormat(), Fast: req.Fast,
				SidekickStoragePath: storagePath, AppendLogID: req.AppendLogID,
				UploadID: req.UploadID, ChunkSequence: req.ChunkSequence,
			}
			return c.Upload(ctx, req.Path, req.SourceID, req.Size)
		},
		// Lookup is the pre-flight check for a log this machine's ledger has
		// never seen. A server without the route answers 404, which reads as
		// "has nothing", and the log is uploaded as before.
		Lookup: func(ctx context.Context, _ string, baseURL, sourceID string) (upload.Have, error) {
			token, err := a.accessToken(ctx)
			if err != nil {
				return upload.Have{}, err
			}
			c := upload.Client{BaseURL: baseURL, Token: token}
			return c.Have(ctx, sourceID)
		},
		Running:        watch.NewGame(tn.Layout, nil).Running,
		RunningInstall: runningInstall,
		Notify:         notify,
		OnStatus:       a.onStatus,
		Logger:         logger,
	})
	if !*stateDebug {
		go func() {
			if err := upload.Ping(ctx, nil, tn.BaseURL); err != nil {
				logger.Error("server check failed", "err", err)
			}
		}()
	}
	return a.runTray(ctx)
}

type installationInfo struct {
	ID       string
	Name     string
	Platform string
	Version  string
}

func installationMetadata(value string) string {
	const maxLength = 100
	if len(value) > maxLength {
		return value[:maxLength]
	}
	return value
}

func ensureInstallation(st *store.Store) (installationInfo, error) {
	state := st.Get()
	id, err := uuid.Parse(state.InstallationID)
	if err != nil || id == uuid.Nil {
		id = uuid.New()
		if err := st.Update(func(s *store.State) { s.InstallationID = id.String() }); err != nil {
			return installationInfo{}, err
		}
	}
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = "Chronicle Sidekick"
	}
	return installationInfo{
		ID: id.String(), Name: installationMetadata(name),
		Platform: installationMetadata(runtime.GOOS + "/" + runtime.GOARCH),
		Version:  installationMetadata(version),
	}, nil
}

const maxStatusWoWDirectories = 50

func (a *app) gameFormatForPath(path string) sidekick.GameFormat {
	config := a.store.Get().EffectiveConfig()
	for _, install := range a.installs() {
		relative, err := filepath.Rel(install.Dir, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		return config.WoWDirectories[sidekick.DirectoryID(install.Dir)].GameFormat
	}
	return ""
}

func (a *app) autoUploadTrigger(install wowdir.Install) watch.Trigger {
	config, ok := a.store.Get().EffectiveConfig().WoWDirectories[sidekick.DirectoryID(install.Dir)]
	if !ok || config.AutoUpload != sidekick.AutoUploadOnGameExit {
		return watch.Trigger{}
	}
	for _, mode := range sidekick.AutoUploadCapabilities()[config.GameFormat] {
		if mode == config.AutoUpload {
			return watch.Trigger{OnExit: true}
		}
	}
	return watch.Trigger{}
}

func (a *app) captureSidekickStorage(install wowdir.Install) bool {
	config := a.store.Get().EffectiveConfig().WoWDirectories[sidekick.DirectoryID(install.Dir)]
	return config.GameFormat.IncludesSidekickStorage()
}

func sidekickStatus(installs []wowdir.Install, config sidekick.SidekickConfig, issues []sidekick.Issue, platform, appVersion string, startAtLogin bool) sidekick.Status {
	if len(installs) > maxStatusWoWDirectories {
		installs = installs[:maxStatusWoWDirectories]
	}
	directories := make([]sidekick.WoWDirectory, 0, len(installs))
	for _, install := range installs {
		name := install.Flavor
		if name == "" {
			name = install.BaseURL
		}
		id := sidekick.DirectoryID(install.Dir)
		directories = append(directories, sidekick.WoWDirectory{
			ID: id, Path: install.Dir, TenantName: name, TenantBaseURL: install.BaseURL,
			Realmlist: install.Realmlist, GameFormat: config.WoWDirectories[id].GameFormat,
		})
	}
	return sidekick.Status{
		Platform: platform, SidekickVersion: appVersion,
		StartAtLogin: startAtLogin, AutoUploadCapabilities: sidekick.AutoUploadCapabilities(),
		WoWDirectories: directories,
		Issues:         append([]sidekick.Issue{}, issues...),
	}
}

func configuredWoWPaths(st store.State) []string {
	paths := append([]string(nil), st.WoWPaths...)
	if len(paths) == 0 && st.WoWPath != "" {
		paths = []string{st.WoWPath}
	}
	return paths
}

func addWoWPath(st *store.State, path string) bool {
	st.WoWPathsSet = true
	path = filepath.Clean(path)
	paths := configuredWoWPaths(*st)
	for _, existing := range paths {
		if existing == path {
			st.WoWPath = ""
			st.WoWPaths = paths
			return false
		}
	}
	st.WoWPath = ""
	st.WoWPaths = append(paths, path)
	return true
}

func canonicalWoWPath(path string) string {
	path = filepath.Clean(path)
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func addDetectedWoWPaths(st *store.State, installs []wowdir.Install, layout tenant.Layout) []string {
	seen := map[string]bool{}
	for _, path := range configuredWoWPaths(*st) {
		seen[canonicalWoWPath(path)] = true
		resolved, err := wowdir.Resolve(path, layout)
		if err != nil {
			continue
		}
		for _, install := range resolved {
			seen[canonicalWoWPath(install.Dir)] = true
		}
	}

	var added []string
	for _, install := range installs {
		path := filepath.Clean(install.Dir)
		key := canonicalWoWPath(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		if addWoWPath(st, path) {
			added = append(added, path)
		}
	}
	return added
}

func removeWoWPath(st *store.State, path string) {
	st.WoWPathsSet = true
	path = filepath.Clean(path)
	paths := configuredWoWPaths(*st)
	kept := paths[:0]
	for _, existing := range paths {
		if existing != path {
			kept = append(kept, existing)
		}
	}
	st.WoWPath = ""
	st.WoWPaths = append([]string(nil), kept...)
}

func useAutomaticWoWFallback(st store.State) bool {
	return len(configuredWoWPaths(st)) == 0 && !st.WoWPathsSet
}

// installs returns every configured WoW client. State from releases that only
// supported one folder is treated as a one-element list. Auto-detection is used
// only for fresh and older state whose folder list has never been managed.
func (a *app) installs() []wowdir.Install {
	st := a.store.Get()
	paths := configuredWoWPaths(st)

	var found []wowdir.Install
	if useAutomaticWoWFallback(st) {
		found = wowdir.Detect(wowdir.StandardRoots(), a.layout)
	} else {
		seen := map[string]bool{}
		for _, path := range paths {
			installs, err := wowdir.Resolve(path, a.layout)
			if err != nil {
				continue
			}
			for _, install := range installs {
				key := canonicalWoWPath(install.Dir)
				if !seen[key] {
					seen[key] = true
					found = append(found, install)
				}
			}
		}
	}
	if a.uploadOverride != "" {
		for i := range found {
			found[i].BaseURL = a.uploadOverride
		}
	}
	return found
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
