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
	lock, err := acquireInstanceWithWait(filepath.Join(dir, "sidekick.lock"), *waitForInstance)
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
		installation: installation, background: *background,
	}
	if *server != "" {
		a.uploadOverride = tn.BaseURL
	}
	a.engine = engine.New(engine.Config{
		Store:    st,
		Installs: a.installs,
		Token:    a.cachedToken,
		Upload: func(ctx context.Context, _ string, baseURL, path, sourceID string, size int64, fast bool) (upload.Result, error) {
			token, err := a.accessToken(ctx)
			if err != nil {
				return upload.Result{}, err
			}
			c := upload.Client{BaseURL: baseURL, Token: token, Fast: fast}
			return c.Upload(ctx, path, sourceID, size)
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
		Running:  watch.NewGame(tn.Layout, nil).Running,
		Notify:   notify,
		OnStatus: a.onStatus,
		Logger:   logger,
	})
	go func() {
		if err := upload.Ping(ctx, nil, tn.BaseURL); err != nil {
			logger.Error("server check failed", "err", err)
		}
	}()
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

func sidekickStatus(installs []wowdir.Install, platform, appVersion string) sidekick.Status {
	if len(installs) > maxStatusWoWDirectories {
		installs = installs[:maxStatusWoWDirectories]
	}
	directories := make([]sidekick.WoWDirectory, 0, len(installs))
	for _, install := range installs {
		name := install.Flavor
		if name == "" {
			name = install.BaseURL
		}
		directories = append(directories, sidekick.WoWDirectory{
			Path: install.Dir, TenantName: name, TenantBaseURL: install.BaseURL, Realmlist: install.Realmlist,
		})
	}
	return sidekick.Status{
		Platform: platform, Version: appVersion,
		WoWDirectories: directories,
		Issues:         []sidekick.Issue{},
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

func removeWoWPath(st *store.State, path string) {
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

func automaticWoWDetectionEnabled(st store.State) bool {
	return !st.DisableAutomaticWoWDetection
}

func setAutomaticWoWDetection(st *store.State, enabled bool) {
	st.DisableAutomaticWoWDetection = !enabled
}

// installs returns every automatically detected and manually configured WoW
// client. State from releases that only supported one folder is treated as a
// one-element manual list.
func (a *app) installs() []wowdir.Install {
	st := a.store.Get()
	var automatic []wowdir.Install
	if !st.DisableAutomaticWoWDetection {
		if a.detectWoW != nil {
			automatic = a.detectWoW()
		} else {
			automatic = wowdir.Detect(wowdir.StandardRoots(), a.layout)
		}
	}
	return a.installsFrom(st, automatic)
}

func canonicalWoWDir(path string) string {
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

func (a *app) installsFrom(st store.State, automatic []wowdir.Install) []wowdir.Install {
	found := make([]wowdir.Install, 0, len(automatic))
	seen := make(map[string]bool, len(automatic))
	for _, install := range automatic {
		key := canonicalWoWDir(install.Dir)
		if !seen[key] {
			seen[key] = true
			found = append(found, install)
		}
	}
	for _, path := range configuredWoWPaths(st) {
		installs, err := wowdir.Resolve(path, a.layout)
		if err != nil {
			continue
		}
		for _, install := range installs {
			key := canonicalWoWDir(install.Dir)
			if !seen[key] {
				seen[key] = true
				found = append(found, install)
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
