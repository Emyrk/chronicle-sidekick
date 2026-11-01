package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Emyrk/chronicle-sidekick/internal/sidekick"
	"github.com/Emyrk/chronicle-sidekick/internal/store"
	"github.com/Emyrk/chronicle-sidekick/internal/upload"
	"github.com/Emyrk/chronicle-sidekick/internal/wowdir"
)

const (
	rpcRequestsName = "ChronicleRPC.requests"
	rpcStatusName   = "ChronicleRPC.status"
	rpcFieldLimit   = 4096
	rpcStatusLimit  = 256 << 10
	rpcUploadMethod = "combat_log.upload"
)

func logIDFromReportURL(reportURL string) string {
	u, err := url.Parse(reportURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != "logs" {
			continue
		}
		if _, err := uuid.Parse(parts[i+1]); err == nil {
			return parts[i+1]
		}
	}
	return ""
}

func addonRequestKey(installDir, id string) string {
	return sidekick.DirectoryID(installDir) + "|" + id
}

func validAddonID(id string) bool {
	if id == "" || len(id) > 200 {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func validAddonArchive(id, name string) bool {
	return validAddonID(id) && name == "ChronicleUpload_"+id+".txt" && filepath.Base(name) == name
}

type rpcRequest struct {
	action, id, method, archive string
	created                     time.Time
}

func parseRPCRequests(body []byte) []rpcRequest {
	if len(body) == 0 || body[len(body)-1] != '\n' {
		if i := strings.LastIndexByte(string(body), '\n'); i >= 0 {
			body = body[:i+1]
		} else {
			return nil
		}
	}
	var out []rpcRequest
	s := bufio.NewScanner(strings.NewReader(string(body)))
	for s.Scan() {
		line := strings.TrimSuffix(s.Text(), "\r")
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 6 || f[0] != "1" || (f[1] != "request" && f[1] != "retry") || f[4] != rpcUploadMethod || !validAddonArchive(f[2], f[5]) {
			continue
		}
		sec, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, rpcRequest{
			action: f[1], id: f[2], created: time.Unix(sec, 0), method: f[4], archive: f[5],
		})
	}
	return out
}

func (e *Engine) scanRPCRequests(ctx context.Context, now time.Time, installs []wowdir.Install) {
	for _, install := range installs {
		customData := filepath.Join(install.Dir, "CustomData")
		body, err := os.ReadFile(filepath.Join(customData, rpcRequestsName))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			e.cfg.Logger.Warn("reading ChronicleCompanion requests", "install", install.Dir, "err", err)
			continue
		}
		for _, in := range parseRPCRequests(body) {
			key := addonRequestKey(install.Dir, in.id)
			request, exists := e.cfg.Store.Get().AddonRequests[key]
			if !exists {
				groupID := uuid.NewString()
				dir := filepath.Join(install.Dir, "ChronicleSidekick", groupID)
				archivePath := archiveCombatPath(dir, in.created)
				request = store.AddonRequest{
					ID: in.id, Method: in.method, InstallDirectory: install.Dir, ArchiveBasename: in.archive,
					ArchiveGroupID: groupID, State: "queued", CreatedAt: in.created, UpdatedAt: now,
				}
				group := store.ArchiveGroup{
					ID: groupID, InstallDirectory: install.Dir, InstallID: sidekick.DirectoryID(install.Dir),
					SourceKey: "addon:" + in.id, CreatedAt: in.created, Pending: true, Files: []string{archivePath},
				}
				if err := e.cfg.Store.Update(func(s *store.State) {
					if s.AddonRequests == nil {
						s.AddonRequests = map[string]store.AddonRequest{}
					}
					if s.ArchiveGroups == nil {
						s.ArchiveGroups = map[string]store.ArchiveGroup{}
					}
					s.AddonRequests[key] = request
					s.ArchiveGroups[groupID] = group
				}); err != nil {
					e.cfg.Logger.Warn("accepting ChronicleCompanion request", "request", in.id, "err", err)
					continue
				}
			} else if in.action == "retry" && in.created.After(request.LastRetryAt) {
				// Consume every retry record once, even when the request is not in a
				// retryable state or the attempted retry cannot start yet.
				request.LastRetryAt = in.created
				if err := e.cfg.Store.Update(func(s *store.State) { s.AddonRequests[key] = request }); err != nil {
					e.cfg.Logger.Warn("persisting ChronicleCompanion retry", "request", in.id, "err", err)
					continue
				}
				if request.State != "failed" {
					continue
				}
				nextState := "queued"
				if request.LogID != "" && e.cfg.Reprocess != nil {
					e.mu.Lock()
					token := e.currentToken(now)
					e.mu.Unlock()
					if token == "" {
						e.updateAddonRequest(key, "failed", "Connect Chronicle Sidekick, then retry again.", upload.Result{}, now)
						continue
					}
					if err := e.cfg.Reprocess(ctx, token, install.BaseURL, request.LogID); err != nil {
						e.cfg.Logger.Warn("retrying Chronicle processing", "request", in.id, "err", err)
						if errors.Is(err, upload.ErrUnauthorized) {
							e.rejectRPCToken(token)
							e.updateAddonRequest(key, "failed", "Connect Chronicle Sidekick, then retry again.", upload.Result{}, now)
						} else {
							e.updateAddonRequest(key, "failed", "Chronicle Sidekick could not retry processing. Try again.", upload.Result{}, now)
						}
						continue
					}
					nextState = "processing"
				}
				request.State, request.Message, request.UpdatedAt = nextState, "", now
				request.Instances = nil
				if err := e.cfg.Store.Update(func(s *store.State) { s.AddonRequests[key] = request }); err != nil {
					e.cfg.Logger.Warn("persisting ChronicleCompanion retry state", "request", in.id, "err", err)
					continue
				}
				if nextState == "queued" {
					e.mu.Lock()
					delete(e.addonQueued, key)
					e.mu.Unlock()
				}
			}
			if request.State == "queued" || request.State == "uploading" {
				if err := e.queueAddonRequest(key, request, install); err != nil {
					e.cfg.Logger.Warn("queueing ChronicleCompanion RPC upload", "request", in.id, "err", err)
					e.updateAddonRequest(key, "failed", rpcFailureMessage(err), upload.Result{}, now)
				}
			}
		}
		e.pollAddonProcessing(ctx, now, install)
		e.publishRPCStatus(now, install)
	}
}

func (e *Engine) removeHandledRPCRequests(install wowdir.Install) bool {
	path := filepath.Join(install.Dir, "CustomData", rpcRequestsName)
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		e.cfg.Logger.Warn("reading ChronicleCompanion requests for cleanup", "install", install.Dir, "err", err)
		return false
	}
	state := e.cfg.Store.Get()
	for _, in := range parseRPCRequests(body) {
		request, exists := state.AddonRequests[addonRequestKey(install.Dir, in.id)]
		if !exists || (in.action == "retry" && request.LastRetryAt.Before(in.created)) {
			return false
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		e.cfg.Logger.Warn("removing handled ChronicleCompanion requests", "install", install.Dir, "err", err)
		return false
	}
	e.cfg.Logger.Info("removed handled ChronicleCompanion request journal", "install", install.Dir)
	return true
}

func (e *Engine) clearAddonRequestState(install wowdir.Install) bool {
	var removed []string
	if err := e.cfg.Store.Update(func(s *store.State) {
		for key, request := range s.AddonRequests {
			if request.InstallDirectory == install.Dir {
				delete(s.AddonRequests, key)
				removed = append(removed, key)
			}
		}
	}); err != nil {
		e.cfg.Logger.Warn("clearing ChronicleCompanion request state", "install", install.Dir, "err", err)
		return false
	}
	removedSet := make(map[string]struct{}, len(removed))
	e.mu.Lock()
	for _, key := range removed {
		removedSet[key] = struct{}{}
		delete(e.addonQueued, key)
	}
	for i := range e.pending {
		if _, ok := removedSet[e.pending[i].addonRequestID]; ok {
			e.pending[i].addonRequestID = ""
		}
	}
	for i := range e.blocked {
		if _, ok := removedSet[e.blocked[i].addonRequestID]; ok {
			e.blocked[i].addonRequestID = ""
		}
	}
	if e.current != nil {
		if _, ok := removedSet[e.current.addonRequestID]; ok {
			e.current.addonRequestID = ""
		}
	}
	e.mu.Unlock()
	return true
}

func (e *Engine) queueAddonRequest(key string, request store.AddonRequest, install wowdir.Install) error {
	e.mu.Lock()
	queued := e.addonQueued[key]
	e.mu.Unlock()
	if queued {
		return nil
	}
	st := e.cfg.Store.Get()
	group, ok := st.ArchiveGroups[request.ArchiveGroupID]
	if !ok || len(group.Files) == 0 {
		return errors.New("managed archive metadata is missing")
	}
	destination := group.Files[0]
	destinationInfo, destinationErr := os.Lstat(destination)
	if destinationErr == nil && destinationInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed archive must not be a symlink")
	}
	if errors.Is(destinationErr, os.ErrNotExist) {
		source := filepath.Join(install.Dir, "CustomData", request.ArchiveBasename)
		info, statErr := os.Lstat(source)
		if statErr != nil {
			return fmt.Errorf("inspect addon archive: %w", statErr)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("archive is not a regular file")
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return fmt.Errorf("create managed archive directory: %w", err)
		}
		if err := os.Rename(source, destination); err != nil {
			return fmt.Errorf("move archive into managed storage: %w", err)
		}
	}
	info, err := os.Stat(destination)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("managed archive is missing or empty")
	}
	j := job{
		path: destination, snapshotPath: destination, size: info.Size(), baseURL: install.BaseURL,
		install: install, queuedAt: request.CreatedAt, groupID: request.ArchiveGroupID,
		addonRequestID: key, sourceRemoved: true,
	}
	e.mu.Lock()
	e.pending = append(e.pending, j)
	e.addonQueued[key] = true
	e.mu.Unlock()
	select {
	case e.kick <- struct{}{}:
	default:
	}
	return nil
}

func (e *Engine) updateAddonRequest(key, state, message string, result upload.Result, now time.Time) {
	if key == "" {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	if err := e.cfg.Store.Update(func(s *store.State) {
		r, ok := s.AddonRequests[key]
		if !ok {
			return
		}
		r.State, r.Message, r.UpdatedAt = rpcState(state), message, now
		if result.LogID != "" {
			r.LogID = result.LogID
		}
		if result.ReportURL != "" {
			r.ReportURL = result.ReportURL
		}
		s.AddonRequests[key] = r
	}); err != nil {
		e.cfg.Logger.Warn("saving ChronicleCompanion request status", "request", key, "err", err)
	}
	if state == "complete" || state == "failed" {
		e.mu.Lock()
		delete(e.addonQueued, key)
		e.mu.Unlock()
	}
}

func (e *Engine) rejectRPCToken(token string) {
	if token == "" {
		return
	}
	e.mu.Lock()
	e.rejectedToken = token
	e.mu.Unlock()
}

func validRPCURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Host != "" &&
		strings.IndexFunc(value, unicode.IsSpace) < 0
}

func (e *Engine) pollAddonProcessing(ctx context.Context, now time.Time, install wowdir.Install) {
	if e.cfg.Processing == nil {
		return
	}
	e.mu.Lock()
	token := e.currentToken(now)
	e.mu.Unlock()
	if token == "" {
		return
	}
	for key, request := range e.cfg.Store.Get().AddonRequests {
		if request.InstallDirectory != install.Dir || request.State != "processing" || request.LogID == "" {
			continue
		}
		result, err := e.cfg.Processing(ctx, token, install.BaseURL, request.LogID)
		if err != nil {
			if errors.Is(err, upload.ErrUnauthorized) {
				e.rejectRPCToken(token)
				e.updateAddonRequest(key, "processing", "Waiting for Chronicle sign-in", upload.Result{}, now)
			}
			continue
		}
		if result.State == "processing" && len(result.Instances) == 0 {
			continue
		}
		if err := e.cfg.Store.Update(func(s *store.State) {
			r, ok := s.AddonRequests[key]
			if !ok {
				return
			}
			r.State, r.Message, r.UpdatedAt = rpcState(result.State), result.Message, now
			r.Instances = nil
			for _, instance := range result.Instances {
				if instance.ID != "" && instance.Name != "" && validRPCURL(instance.URL) {
					r.Instances = append(r.Instances, store.AddonInstance{ID: instance.ID, Name: instance.Name, URL: instance.URL})
				}
			}
			s.AddonRequests[key] = r
		}); err != nil {
			e.cfg.Logger.Warn("saving ChronicleCompanion processing result", "request", request.ID, "err", err)
		}
	}
}

func rpcState(value string) string {
	switch value {
	case "queued", "uploading", "processing", "complete", "failed":
		return value
	default:
		return "failed"
	}
}

func rpcFailureMessage(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "The archived combat log is no longer available."
	}
	if upload.IsQuotaError(err) {
		return "Your Chronicle storage is full. Delete old logs on Chronicle, then retry."
	}
	var serverErr *upload.Error
	if errors.As(err, &serverErr) {
		return fmt.Sprintf("Chronicle rejected the upload (HTTP %d).", serverErr.Status)
	}
	return "Chronicle Sidekick could not upload this combat log."
}

func cleanAddonField(v string) string {
	v = strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(v)
	if len(v) <= rpcFieldLimit {
		return v
	}
	v = v[:rpcFieldLimit]
	for !utf8.ValidString(v) {
		v = v[:len(v)-1]
	}
	return v
}

func (e *Engine) publishRPCStatus(now time.Time, install wowdir.Install) {
	state := e.cfg.Store.Get()
	readiness, message := "ready", ""
	e.mu.Lock()
	token := e.currentToken(now)
	rejected := token != "" && token == e.rejectedToken
	e.mu.Unlock()
	if token == "" || rejected {
		readiness, message = "sign_in_required", "Connect Chronicle Sidekick to upload logs"
	}
	version := e.cfg.Version
	if version == "" {
		version = "dev"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "RPC\t1\t%s\t%d\t%s\t%s\n", cleanAddonField(version), now.Unix(), readiness, cleanAddonField(message))
	var requests []store.AddonRequest
	for _, r := range state.AddonRequests {
		if r.InstallDirectory == install.Dir && (r.State != "complete" || now.Sub(r.UpdatedAt) <= 24*time.Hour) {
			requests = append(requests, r)
		}
	}
	sort.Slice(requests, func(i, j int) bool {
		iOutstanding := requests[i].State != "complete"
		jOutstanding := requests[j].State != "complete"
		if iOutstanding != jOutstanding {
			return iOutstanding
		}
		return requests[i].CreatedAt.After(requests[j].CreatedAt)
	})
	for _, r := range requests {
		method := r.Method
		if method == "" {
			method = rpcUploadMethod
		}
		line := fmt.Sprintf("RESPONSE\t%s\t%s\t%s\t%d\t%d\t%s\n", cleanAddonField(r.ID), cleanAddonField(method), rpcState(r.State), r.CreatedAt.Unix(), r.UpdatedAt.Unix(), cleanAddonField(r.Message))
		if b.Len()+len(line) > rpcStatusLimit {
			break
		}
		b.WriteString(line)
		for _, instance := range r.Instances {
			line = fmt.Sprintf("INSTANCE\t%s\t%s\t%s\t%s\n", cleanAddonField(r.ID), cleanAddonField(instance.ID), cleanAddonField(instance.Name), cleanAddonField(instance.URL))
			if b.Len()+len(line) > rpcStatusLimit {
				break
			}
			b.WriteString(line)
		}
	}
	customData := filepath.Join(install.Dir, "CustomData")
	if err := os.MkdirAll(customData, 0o700); err != nil {
		return
	}
	path := filepath.Join(customData, rpcStatusName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		e.cfg.Logger.Warn("writing ChronicleCompanion status", "install", install.Dir, "err", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		e.cfg.Logger.Warn("publishing ChronicleCompanion status", "install", install.Dir, "err", err)
	}
}
