package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/ncruces/zenity"
	"golang.org/x/oauth2"

	"github.com/Emyrk/chronicle-sidekick/internal/autostart"
	"github.com/Emyrk/chronicle-sidekick/internal/engine"
	"github.com/Emyrk/chronicle-sidekick/internal/pair"
	"github.com/Emyrk/chronicle-sidekick/internal/sidekick"
	"github.com/Emyrk/chronicle-sidekick/internal/store"
	"github.com/Emyrk/chronicle-sidekick/internal/tenant"
	"github.com/Emyrk/chronicle-sidekick/internal/update"
	"github.com/Emyrk/chronicle-sidekick/internal/upload"
	"github.com/Emyrk/chronicle-sidekick/internal/wowdir"
)

type app struct {
	server         string
	uploadOverride string
	layout         tenant.Layout
	store          *store.Store
	logger         *slog.Logger
	engine         *engine.Engine
	cancel         context.CancelFunc
	installation   installationInfo
	background     bool

	mu     sync.Mutex
	status engine.Status
	items  menuItems
	ready  bool

	updateLink    string
	updateVersion string
	updating      bool
	pairing       bool               // a pairing is in flight
	pairCancel    context.CancelFunc // cancels the active pairing request and poll
	pairCode      string             // device code shown while the pairing is waiting
	pairMsg       string             // last pairing outcome, shown until the next attempt

	authMu     sync.Mutex
	token      *oauth2.Token
	tokenDirty bool
}

type wowMenuEntry struct {
	item   *systray.MenuItem
	cancel context.CancelFunc
}

type menuItems struct {
	status, version, upload, wows, report, connect, cancelConnect, disconnect, update, forceUpdate *systray.MenuItem
	wowFolders                                                                                     map[string]wowMenuEntry
	uploadEntries                                                                                  []wowMenuEntry
	uploadDirs                                                                                     []string
	uploadCount                                                                                    int
}

func (a *app) runTray(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		systray.Quit()
	}()
	systray.Run(func() { a.onReady(ctx) }, a.cancel)
	return nil
}

func (a *app) onReady(ctx context.Context) {
	systray.SetTitle("")
	systray.SetTooltip("Chronicle Sidekick")

	a.items.status = systray.AddMenuItem("Starting…", "")
	a.items.status.Disable()
	a.items.version = systray.AddMenuItem(versionLabel(version), "The running Chronicle Sidekick version")
	a.items.version.Disable()
	systray.AddSeparator()
	a.items.upload = systray.AddMenuItem("Upload now", "Upload combat logs")
	a.items.report = systray.AddMenuItem("Open last report", "")
	a.items.report.Disable()
	a.items.connect = systray.AddMenuItem("Connect to Chronicle…", "")
	a.items.connect.Hide()
	a.items.cancelConnect = systray.AddMenuItem("Cancel connection", "Cancel the current browser sign-in and start over")
	a.items.cancelConnect.Hide()
	a.items.disconnect = systray.AddMenuItem("Disconnect", "Forget this computer's Chronicle sign-in")
	a.items.disconnect.Hide()
	a.items.update = systray.AddMenuItem("Update available", "")
	a.items.update.Hide()
	a.items.forceUpdate = systray.AddMenuItem("Force update now…", "Download, verify, and reinstall the latest release")
	if nativeUpdaterAvailable() {
		a.items.forceUpdate.Hide()
		a.items.update.SetTitle("Check for updates…")
		a.items.update.Show()
		startNativeUpdater()
	}
	systray.AddSeparator()
	a.items.wows = systray.AddMenuItem("World of Warcraft folders", "Configured World of Warcraft installations")
	a.items.wowFolders = map[string]wowMenuEntry{}
	for _, path := range configuredWoWPaths(a.store.Get()) {
		a.addWoWMenuEntry(ctx, path)
	}
	a.setWoWMenuTitle(len(a.items.wowFolders))
	a.refreshUploadMenu(ctx)
	choose := systray.AddMenuItem("Add WoW folder…", "Watch another World of Warcraft installation")
	clearFolders := systray.AddMenuItem("Use automatic WoW detection", "Forget manually selected WoW folders")
	archive := systray.AddMenuItemCheckbox("Archive the log after upload", "Rename the combat log once it is uploaded so the next session starts a fresh one", !a.store.Get().KeepLog)
	login := systray.AddMenuItemCheckbox("Start at login", "", autostart.Enabled())
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "")

	a.mu.Lock()
	a.ready = true
	a.mu.Unlock()
	a.render()

	go a.engine.Run(ctx)
	go a.startup(ctx)
	go a.checkUpdate(ctx)
	go a.reportStatus(ctx)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.items.upload.ClickedCh:
				a.uploadFromMainMenu()
			case <-a.items.report.ClickedCh:
				if u := a.current().ReportURL; u != "" {
					openURL(u)
				}
			case <-a.items.connect.ClickedCh:
				go a.connect(ctx)
			case <-a.items.cancelConnect.ClickedCh:
				a.cancelPairing()
			case <-a.items.disconnect.ClickedCh:
				go a.disconnect(ctx)
			case <-a.items.update.ClickedCh:
				if nativeUpdaterAvailable() {
					checkNativeUpdater()
				} else {
					go a.installUpdate(ctx, false)
				}
			case <-a.items.forceUpdate.ClickedCh:
				go a.installUpdate(ctx, true)
			case <-choose.ClickedCh:
				a.chooseFolder(ctx)
			case <-clearFolders.ClickedCh:
				a.clearWoWFolders(ctx)
			case <-archive.ClickedCh:
				on := !archive.Checked()
				a.toggle(archive, on)
				_ = a.store.Update(func(s *store.State) { s.KeepLog = !on })
			case <-login.ClickedCh:
				on := !login.Checked()
				exe, err := os.Executable()
				if err == nil {
					err = autostart.Set(on, exe)
				}
				if err != nil {
					a.logger.Error("start at login", "err", err)
					notify("Chronicle", "Couldn't change the start-at-login setting.")
					continue
				}
				a.toggle(login, on)
			case <-quit.ClickedCh:
				a.cancel()
				return
			}
		}
	}()
}

const statusInterval = time.Minute

func (a *app) reportStatus(ctx context.Context) {
	ticker := time.NewTicker(statusInterval)
	defer ticker.Stop()
	for {
		a.reportStatusOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *app) reportStatusOnce(ctx context.Context) {
	if a.cachedToken() == "" {
		return
	}
	token, err := a.accessToken(ctx)
	if err != nil {
		a.logger.Warn("Sidekick status authentication", "err", err)
		return
	}
	client := sidekick.Client{BaseURL: a.server, Token: token}
	status := sidekickStatus(a.installs(), a.installation.Platform, a.installation.Version)
	status.ConfigHash = a.store.Get().EffectiveConfigHash()
	response, err := client.Update(ctx, status)
	if err != nil {
		a.logger.Warn("Sidekick status update", "err", err)
		return
	}
	changed, err := reconcileConfig(a.store, response)
	if err != nil {
		a.logger.Warn("apply Sidekick config", "err", err)
		return
	}
	if changed {
		a.logger.Info("synced Sidekick config", "config_hash", response.ConfigHash)
	}
}

func reconcileConfig(st *store.Store, response sidekick.UpdateResponse) (bool, error) {
	if response.ConfigHash == st.Get().EffectiveConfigHash() || response.Config == nil {
		return false, nil
	}
	if response.ConfigHash == "" {
		return false, errors.New("Sidekick config response has no config hash")
	}
	if err := response.Config.Validate(); err != nil {
		return false, err
	}
	computedHash, err := sidekick.HashConfig(*response.Config)
	if err != nil {
		return false, fmt.Errorf("hash Sidekick config: %w", err)
	}
	if computedHash != response.ConfigHash {
		return false, fmt.Errorf("Sidekick config hash mismatch: computed %s, response has %s", computedHash, response.ConfigHash)
	}
	if err := st.Update(func(s *store.State) {
		s.Config = *response.Config
	}); err != nil {
		return false, err
	}
	return true, nil
}

// checkUpdate looks for a newer release once at launch and, if there is one,
// shows it in the tray. Failures are logged and otherwise ignored.
// checkUpdate checks at launch and then once a day. Later checks are
// conditional GETs, so an unchanged release list costs one tiny request.
func (a *app) checkUpdate(ctx context.Context) {
	if nativeUpdaterAvailable() {
		return
	}
	checker := &update.Checker{Client: http.DefaultClient, URL: update.DefaultURL}
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		a.checkUpdateOnce(ctx, checker)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *app) checkUpdateOnce(ctx context.Context, checker *update.Checker) {
	rel, ok, err := checker.Check(ctx)
	if err != nil {
		a.logger.Warn("update check", "err", err)
		return
	}
	if !ok || !update.Newer(version, rel.Version) {
		return
	}
	a.mu.Lock()
	a.updateLink = rel.URL
	a.updateVersion = rel.Version
	a.mu.Unlock()
	a.items.update.SetTitle("Install and restart: v" + rel.Version)
	a.items.update.Show()
}

func (a *app) updateURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.updateLink
}

func (a *app) installUpdate(ctx context.Context, force bool) {
	a.mu.Lock()
	if a.updating {
		a.mu.Unlock()
		return
	}
	a.updating = true
	wanted := a.updateVersion
	a.mu.Unlock()

	a.items.update.Disable()
	a.items.forceUpdate.Disable()
	if force {
		a.items.forceUpdate.SetTitle("Installing latest release…")
	}
	if wanted != "" {
		a.items.update.SetTitle("Installing v" + wanted + "…")
	} else {
		a.items.update.SetTitle("Installing update…")
	}

	installer, err := update.NewInstaller()
	if err == nil {
		_, err = installer.InstallLatest(ctx, version, force)
	}
	if err == nil {
		err = restartSidekick()
	}
	if err != nil {
		a.logger.Warn("install update", "err", err)
		notify("Chronicle Sidekick", "The update could not be installed. Open the release page to update manually.")
		a.mu.Lock()
		a.updating = false
		a.mu.Unlock()
		if wanted != "" {
			a.items.update.SetTitle("Install and restart: v" + wanted)
		} else {
			a.items.update.SetTitle("Update available")
		}
		a.items.update.Enable()
		a.items.forceUpdate.SetTitle("Force update now…")
		a.items.forceUpdate.Enable()
		if page := a.updateURL(); page != "" {
			openURL(page)
		}
		return
	}
	a.cancel()
}

func (a *app) toggle(item *systray.MenuItem, on bool) {
	if on {
		item.Check()
	} else {
		item.Uncheck()
	}
}

type wowUploadTarget struct {
	Name string
	Dirs []string
}

func wowUploadTargets(installs []wowdir.Install) []wowUploadTarget {
	nameCount := map[string]int{}
	for _, install := range installs {
		name := install.Flavor
		if name == "" {
			name = filepath.Base(install.Dir)
		}
		nameCount[name]++
	}

	var targets []wowUploadTarget
	for _, install := range installs {
		name := install.Flavor
		if name == "" {
			name = filepath.Base(install.Dir)
		}
		if nameCount[name] > 1 {
			name = fmt.Sprintf("%s (%s)", name, filepath.Base(install.Dir))
		}
		targets = append(targets, wowUploadTarget{Name: name, Dirs: []string{install.Dir}})
	}
	return targets
}

func (a *app) refreshUploadMenu(ctx context.Context) {
	targets := wowUploadTargets(a.installs())

	a.mu.Lock()
	old := a.items.uploadEntries
	a.items.uploadEntries = nil
	a.items.uploadDirs = nil
	a.items.uploadCount = len(targets)
	if len(targets) == 1 {
		a.items.uploadDirs = append([]string(nil), targets[0].Dirs...)
	}
	a.mu.Unlock()
	for _, entry := range old {
		entry.cancel()
		entry.item.Remove()
	}

	switch len(targets) {
	case 0:
		a.items.upload.SetTitle("Upload now")
	case 1:
		a.items.upload.SetTitle("Upload " + targets[0].Name)
	default:
		a.items.upload.SetTitle("Upload now")
		for _, target := range targets {
			item := a.items.upload.AddSubMenuItem(target.Name, "Upload new logs from "+target.Name)
			entryCtx, cancel := context.WithCancel(ctx)
			a.mu.Lock()
			a.items.uploadEntries = append(a.items.uploadEntries, wowMenuEntry{item: item, cancel: cancel})
			a.mu.Unlock()
			go uploadOnClick(entryCtx, item, a.engine, target.Dirs)
		}
	}
}

func (a *app) uploadFromMainMenu() {
	a.mu.Lock()
	count := a.items.uploadCount
	dirs := append([]string(nil), a.items.uploadDirs...)
	a.mu.Unlock()
	switch count {
	case 0:
		a.engine.UploadNow()
	case 1:
		a.engine.UploadNowFor(dirs)
	}
}

func uploadOnClick(ctx context.Context, item *systray.MenuItem, e *engine.Engine, dirs []string) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-item.ClickedCh:
			e.UploadNowFor(dirs)
		}
	}
}

func addUploadActions(parent *systray.MenuItem, ctx context.Context, e *engine.Engine, targets []wowUploadTarget) {
	switch len(targets) {
	case 0:
		return
	case 1:
		item := parent.AddSubMenuItem("Upload now", "Upload new logs from "+targets[0].Name)
		go uploadOnClick(ctx, item, e, targets[0].Dirs)
	default:
		menu := parent.AddSubMenuItem("Upload now", "Choose a WoW client to upload")
		for _, target := range targets {
			item := menu.AddSubMenuItem(target.Name, "Upload new logs from "+target.Name)
			go uploadOnClick(ctx, item, e, target.Dirs)
		}
	}
}

type wowSiteLink struct {
	Name string
	URL  string
}

func (a *app) wowFolderSites(path string) []wowSiteLink {
	found, err := wowdir.Resolve(path, a.layout)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var links []wowSiteLink
	for _, install := range found {
		baseURL := install.BaseURL
		if a.uploadOverride != "" {
			baseURL = a.uploadOverride
		}
		if baseURL == "" || seen[baseURL] {
			continue
		}
		seen[baseURL] = true
		name := install.Flavor
		if name == "" {
			name = baseURL
		}
		links = append(links, wowSiteLink{Name: name, URL: baseURL})
	}
	return links
}

func (a *app) addWoWMenuEntry(ctx context.Context, path string) {
	path = filepath.Clean(path)
	a.mu.Lock()
	if _, ok := a.items.wowFolders[path]; ok {
		a.mu.Unlock()
		return
	}
	label := a.wowFolderLabel(path)
	item := a.items.wows.AddSubMenuItem(label, path)
	entryCtx, cancel := context.WithCancel(ctx)
	installs, _ := wowdir.Resolve(path, a.layout)
	addUploadActions(item, entryCtx, a.engine, wowUploadTargets(installs))
	sites := a.wowFolderSites(path)
	switch len(sites) {
	case 1:
		visit := item.AddSubMenuItem("Visit Site", sites[0].URL)
		go openSiteOnClick(entryCtx, visit, sites[0].URL)
	default:
		if len(sites) > 1 {
			visit := item.AddSubMenuItem("Visit Site", "Open a Chronicle community")
			for _, site := range sites {
				link := visit.AddSubMenuItem(site.Name, site.URL)
				go openSiteOnClick(entryCtx, link, site.URL)
			}
		}
	}
	remove := item.AddSubMenuItem("Remove", "Stop watching "+path)
	a.items.wowFolders[path] = wowMenuEntry{item: item, cancel: cancel}
	count := len(a.items.wowFolders)
	a.mu.Unlock()
	a.setWoWMenuTitle(count)

	go func() {
		select {
		case <-entryCtx.Done():
		case <-remove.ClickedCh:
			a.removeWoWFolder(ctx, path)
		}
	}()
}

func openSiteOnClick(ctx context.Context, item *systray.MenuItem, siteURL string) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-item.ClickedCh:
			openURL(siteURL)
		}
	}
}

func (a *app) wowFolderLabel(path string) string {
	found, err := wowdir.Resolve(path, a.layout)
	if err == nil && len(found) == 1 && found[0].Flavor != "" {
		return fmt.Sprintf("%s — %s", found[0].Flavor, filepath.Base(path))
	}
	if err == nil && len(found) > 1 {
		return fmt.Sprintf("%s (%d clients)", filepath.Base(path), len(found))
	}
	return filepath.Base(path)
}

func (a *app) removeWoWFolder(ctx context.Context, path string) {
	if err := a.store.Update(func(s *store.State) { removeWoWPath(s, path) }); err != nil {
		a.logger.Error("remove wow folder", "path", path, "err", err)
		return
	}

	path = filepath.Clean(path)
	a.mu.Lock()
	entry, ok := a.items.wowFolders[path]
	if ok {
		delete(a.items.wowFolders, path)
	}
	count := len(a.items.wowFolders)
	a.mu.Unlock()
	if ok {
		entry.cancel()
		entry.item.Remove()
	}
	a.refreshUploadMenu(ctx)
	a.setWoWMenuTitle(count)
}

func (a *app) clearWoWFolders(ctx context.Context) {
	if err := a.store.Update(func(s *store.State) {
		s.WoWPath = ""
		s.WoWPaths = nil
	}); err != nil {
		a.logger.Error("clear wow folders", "err", err)
		return
	}

	a.mu.Lock()
	entries := a.items.wowFolders
	a.items.wowFolders = map[string]wowMenuEntry{}
	a.mu.Unlock()
	for _, entry := range entries {
		entry.cancel()
		entry.item.Remove()
	}
	a.refreshUploadMenu(ctx)
	a.setWoWMenuTitle(0)
}

func (a *app) setWoWMenuTitle(count int) {
	if count == 0 {
		a.items.wows.SetTitle("World of Warcraft folders (automatic)")
		return
	}
	a.items.wows.SetTitle(fmt.Sprintf("World of Warcraft folders (%d)", count))
}

func wowFolderError(err error) (title, message string) {
	var unsupported *wowdir.UnsupportedRealmError
	if errors.As(err, &unsupported) {
		return "Unsupported WoW server", fmt.Sprintf(
			"Sidekick found realmlist.wtf, but %q does not map to a Chronicle community yet. This folder was not added.",
			unsupported.Host,
		)
	}
	return "WoW folder not recognized", "Sidekick could not find a supported World of Warcraft client in that folder. This folder was not added."
}

func (a *app) chooseFolder(ctx context.Context) {
	path, err := zenity.SelectFile(
		zenity.Directory(),
		zenity.Title("Add a World of Warcraft folder"),
	)
	if err != nil {
		if err != zenity.ErrCanceled {
			a.logger.Error("folder picker", "err", err)
		}
		return
	}
	if _, err := wowdir.Resolve(path, a.layout); err != nil {
		title, message := wowFolderError(err)
		if dialogErr := zenity.Error(message, zenity.Title(title)); dialogErr != nil && dialogErr != zenity.ErrCanceled {
			a.logger.Error("show wow folder error", "err", dialogErr)
		}
		return
	}
	path = filepath.Clean(path)
	added := false
	if err := a.store.Update(func(s *store.State) { added = addWoWPath(s, path) }); err != nil {
		a.logger.Error("save wow path", "err", err)
		return
	}
	if added {
		a.addWoWMenuEntry(ctx, path)
		a.refreshUploadMenu(ctx)
	}
}

func (a *app) current() engine.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

func (a *app) onStatus(s engine.Status) {
	a.mu.Lock()
	a.status = s
	a.mu.Unlock()
	a.render()
}

func (a *app) render() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.ready {
		return
	}
	s := a.status
	setTrayIcon(trayIcon(s.State))
	systray.SetTooltip("Chronicle Sidekick: " + s.Detail)
	a.items.status.SetTitle(s.Detail)
	if s.ReportURL != "" {
		a.items.report.Enable()
	}
	switch {
	case a.pairing:
		a.items.connect.Hide()
		a.items.cancelConnect.Show()
		a.items.disconnect.Hide()
	case s.State == engine.StateReconnect:
		a.items.connect.Show()
		a.items.cancelConnect.Hide()
		a.items.disconnect.Hide()
	default:
		a.items.connect.Hide()
		a.items.cancelConnect.Hide()
		a.items.disconnect.Show()
	}
	switch {
	case a.pairing:
		detail := pairingStatus(a.pairCode)
		a.items.status.SetTitle(detail)
		systray.SetTooltip("Chronicle Sidekick: " + detail)
	case a.pairMsg != "":
		a.items.status.SetTitle(a.pairMsg)
	}
}

func pairingStatus(code string) string {
	if code == "" {
		return "Waiting for device code…"
	}
	return fmt.Sprintf("Code %s: approve in your browser…", code)
}

// startup confirms interactive launches and refreshes existing login entries so
// future automatic starts remain quiet.
func (a *app) startup(ctx context.Context) {
	if autostart.Enabled() {
		executable, err := os.Executable()
		if err == nil {
			err = autostart.Set(true, executable)
		}
		if err != nil {
			a.logger.Warn("refresh start at login", "err", err)
		}
	}
	if !a.background {
		_ = zenity.Info(
			"Chronicle Sidekick is running in the system tray. Use the tray icon to upload logs, change settings, or quit.",
			zenity.Title("Chronicle Sidekick"),
			zenity.Context(ctx),
		)
	}
	if !a.background && a.cachedToken() == "" {
		notify("Chronicle", "Not connected. Choose \"Connect to Chronicle…\" from the tray menu to start uploading.")
	}
}

func (a *app) pairClient() *pair.Client {
	return &pair.Client{
		BaseURL:        a.server,
		InstallationID: a.installation.ID, InstallationName: a.installation.Name,
		InstallationPlatform: a.installation.Platform, InstallationVersion: a.installation.Version,
	}
}

func (a *app) cachedToken() string {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	if a.token == nil {
		return ""
	}
	return a.token.AccessToken
}

// accessToken returns a current access token. The lock serializes refresh-token
// rotation so two uploads cannot try to redeem the same refresh token.
func (a *app) accessToken(ctx context.Context) (string, error) {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	if a.token == nil {
		return "", upload.ErrUnauthorized
	}
	next, err := a.pairClient().Refresh(ctx, a.token)
	if err != nil {
		if errors.Is(err, pair.ErrUnauthorized) {
			_ = store.DeleteToken()
			a.token = nil
			a.tokenDirty = false
			return "", upload.ErrUnauthorized
		}
		return "", err
	}
	changed := next.AccessToken != a.token.AccessToken || next.RefreshToken != a.token.RefreshToken || !next.Expiry.Equal(a.token.Expiry)
	a.token = next
	a.tokenDirty = a.tokenDirty || changed
	if a.tokenDirty {
		if err := store.SaveToken(next); err != nil {
			return "", fmt.Errorf("save refreshed OAuth token: %w", err)
		}
		a.tokenDirty = false
	}
	return next.AccessToken, nil
}

// connect runs the browser pairing flow.
func (a *app) connect(ctx context.Context) {
	pairCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	if a.pairing {
		a.mu.Unlock()
		cancel()
		return
	}
	a.pairing = true
	a.pairCancel = cancel
	a.pairCode = ""
	a.pairMsg = ""
	a.mu.Unlock()
	a.render()

	msg := a.pair(pairCtx)
	cancel()

	a.mu.Lock()
	a.pairing = false
	a.pairCancel = nil
	a.pairCode = ""
	a.pairMsg = msg
	a.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	a.render()
	a.engine.TokenChanged()
	go a.reportStatusOnce(ctx)
}

func (a *app) cancelPairing() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pairCancel != nil {
		a.pairCancel()
	}
}

// beginPairingCommit makes approval and cancellation mutually exclusive. Once
// it succeeds, a later Cancel connection click cannot discard an approved token.
func (a *app) beginPairingCommit(ctx context.Context) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	a.pairCancel = nil
	return true
}

// pair returns a failure message, or "" when connected.
func (a *app) pair(ctx context.Context) string {
	c := a.pairClient()
	start, err := c.Start(ctx)
	if errors.Is(err, context.Canceled) {
		return "Pairing canceled"
	}
	if err != nil {
		a.logger.Error("OAuth device authorization", "err", err)
		notify("Chronicle", "Couldn't reach Chronicle. Check your connection and try again.")
		return "Couldn't reach Chronicle, try again"
	}
	if ctx.Err() != nil {
		return "Pairing canceled"
	}
	a.mu.Lock()
	a.pairCode = start.UserCode
	a.mu.Unlock()
	a.render()
	if ctx.Err() != nil {
		return "Pairing canceled"
	}
	openURL(pair.VerificationURL(start))

	outcome, token, err := c.Wait(ctx, start)
	if errors.Is(err, context.Canceled) {
		return "Pairing canceled"
	}
	if err != nil {
		a.logger.Warn("pair wait", "err", err)
	}
	switch {
	case outcome == pair.Approved && err == nil:
		if !a.beginPairingCommit(ctx) {
			return "Pairing canceled"
		}
		if err := store.SaveToken(token); err != nil {
			a.logger.Error("save OAuth token", "err", err)
			notify("Chronicle", "Couldn't save the sign-in to your keychain.")
			return "Couldn't save the sign-in"
		}
		a.authMu.Lock()
		a.token = token
		a.tokenDirty = false
		a.authMu.Unlock()
		notify("Chronicle", "Connected. Your logs will upload automatically.")
		return ""
	case outcome == pair.Denied:
		notify("Chronicle", "Pairing denied.")
		return "Pairing denied"
	default:
		notify("Chronicle", "Pairing expired, try again.")
		return "Pairing expired, try again"
	}
}

func (a *app) disconnect(ctx context.Context) {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	if err := a.pairClient().Revoke(ctx, a.token); err != nil {
		a.logger.Warn("revoke OAuth token", "err", err)
	}
	if err := store.DeleteToken(); err != nil {
		a.logger.Error("delete OAuth token", "err", err)
		notify("Chronicle", "Couldn't remove the sign-in from your keychain.")
		return
	}
	a.token = nil
	a.tokenDirty = false
	a.mu.Lock()
	a.pairMsg = ""
	a.mu.Unlock()
	a.engine.TokenChanged()
}
