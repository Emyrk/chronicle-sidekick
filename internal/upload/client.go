// Package upload sends combat logs to a Chronicle server.
package upload

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	uploadPath = "/api/v1/raidlogs/logs/upload-v2"
	appendPath = "/api/v1/raidlogs/logs/"
	// sourcePath answers what the signed-in owner already holds under a source
	// id, before any body is sent. The source id is appended to it.
	sourcePath = "/api/v1/raidlogs/logs/by-source-id/"
	probePath  = "/api/v1/whoami/dump"
	// sourceHeader carries the content-keyed id of the log (see Identify).
	// A server that already has a group for it replaces that group rather
	// than creating a second one.
	sourceHeader        = "X-Chronicle-Source-Id"
	uploadIDHeader      = "X-Chronicle-Upload-ID"
	chunkSequenceHeader = "X-Chronicle-Chunk-Sequence"
)

// Ping checks that baseURL serves the Chronicle API. Without credentials the
// probe endpoint answers 400, 401 or 403 depending on the deployment; a landing
// page answers 404 or 405.
func Ping(ctx context.Context, httpc *http.Client, baseURL string) error {
	if httpc == nil {
		httpc = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+probePath, nil)
	if err != nil {
		return err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
	default:
		return &NotChronicleError{BaseURL: baseURL, Status: resp.StatusCode}
	}
	return nil
}

// NotChronicleError means the host is up but is not a Chronicle API.
type NotChronicleError struct {
	BaseURL string
	Status  int
}

func (e *NotChronicleError) Error() string {
	return fmt.Sprintf("server %s does not look like a Chronicle API (HTTP %d)", e.BaseURL, e.Status)
}

// ErrUnauthorized means the token is missing, expired or revoked.
var ErrUnauthorized = errors.New("not signed in to Chronicle")

// Error is a failed upload that came back from the server.
type Error struct {
	Status  int
	Message string
	Detail  string
}

func (e *Error) Error() string {
	message := e.Message
	if e.Detail != "" && e.Detail != message {
		message += ": " + e.Detail
	}
	return fmt.Sprintf("upload failed (HTTP %d): %s", e.Status, message)
}

// Retryable reports whether the same request may succeed later.
func Retryable(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Status >= 500 || e.Status == http.StatusTooManyRequests || e.Status == http.StatusRequestTimeout
	}
	// Anything that is not a server answer (DNS, connection reset, timeout) is
	// a connectivity problem.
	return !errors.Is(err, ErrUnauthorized) && !errors.Is(err, context.Canceled) && !errors.Is(err, os.ErrNotExist)
}

// IsQuotaError reports whether Chronicle rejected an upload because it would
// exceed the account's storage limit.
func IsQuotaError(err error) bool {
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusBadRequest {
		return false
	}
	text := strings.ToLower(e.Message + " " + e.Detail)
	return strings.Contains(text, "storage limit exceeded") ||
		strings.Contains(text, "exceed the storage limit")
}

// Result is the outcome of one upload.
type Result struct {
	// LogID identifies the Chronicle log group that received the upload.
	LogID string
	// ReportURL is where the log can be viewed.
	ReportURL string
	// Duplicate is true when the server already had this exact file.
	Duplicate bool
}

// ProcessingResult is Chronicle's current parser result for an uploaded log.
type ProcessingResult struct {
	State     string
	Message   string
	Instances []Instance
}

// Instance is one parsed raid instance belonging to an uploaded log.
type Instance struct {
	ID   string
	Name string
	URL  string
}

// Client uploads logs.
type Client struct {
	// BaseURL is the tenant's Chronicle site.
	BaseURL string
	// Format and Flavor are the tenant's upload-v2 parameters. The server
	// does not sniff them; both are required.
	Format, Flavor string
	// AppendLogID adds this upload to an existing Chronicle log group. UploadID
	// and a positive ChunkSequence are required when it is set.
	AppendLogID   string
	UploadID      string
	ChunkSequence int
	// SidekickStoragePath is an optional 3.3.5a sidecar artifact included with
	// both initial and append requests.
	SidekickStoragePath string
	// Token returns the current bearer token ("" when signed out).
	Token string
	HTTP  *http.Client
	// Fast compresses at gzip.BestSpeed: ~3x the throughput of the default for a
	// ratio that is irrelevant next to a frame hitch while the game runs.
	Fast bool
}

type uploadResponse struct {
	LogID string `json:"log_id"`
	// Duplicate is sent by servers that answer 200 for a file they already
	// have instead of rejecting it.
	Duplicate bool `json:"duplicate"`
	// URL is sent by servers that support idempotent re-upload; older ones
	// only send log_id.
	URL string `json:"url"`
	// Message, Detail and Link come from error responses.
	Message string `json:"message"`
	Detail  string `json:"detail"`
	Link    string `json:"link"`
}

// Upload gzips the first size bytes of path and posts them under sourceID (see
// Identify; "" sends no source id). Only size bytes are read so the upload
// matches the size the caller recorded even while the client keeps appending.
func (c *Client) Upload(ctx context.Context, path, sourceID string, size int64) (Result, error) {
	if c.Token == "" {
		return Result{}, ErrUnauthorized
	}
	if c.AppendLogID != "" {
		if _, err := uuid.Parse(c.UploadID); err != nil {
			return Result{}, fmt.Errorf("append upload requires a valid upload ID: %w", err)
		}
		if c.ChunkSequence < 1 {
			return Result{}, errors.New("append upload requires a positive chunk sequence")
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = f.Close() }()

	var storage io.Reader
	if c.SidekickStoragePath != "" {
		storageFile, err := os.Open(c.SidekickStoragePath)
		if err != nil {
			return Result{}, err
		}
		defer func() { _ = storageFile.Close() }()
		storage = storageFile
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		_ = pw.CloseWithError(writeForm(mw, f, size, filepath.Base(path), c.Fast, storage, filepath.Base(c.SidekickStoragePath)))
	}()

	query := url.Values{}
	if c.Format != "" {
		query.Set("format", c.Format)
	}
	if c.Flavor != "" {
		query.Set("flavor", c.Flavor)
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + uploadPath
	if c.AppendLogID != "" {
		endpoint = strings.TrimRight(c.BaseURL, "/") + appendPath + url.PathEscape(c.AppendLogID) + "/append"
	}
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, pr)
	if err != nil {
		_ = pr.Close()
		return Result{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if sourceID != "" {
		req.Header.Set(sourceHeader, sourceID)
	}
	if c.AppendLogID != "" {
		req.Header.Set(uploadIDHeader, c.UploadID)
		req.Header.Set(chunkSequenceHeader, fmt.Sprintf("%d", c.ChunkSequence))
	}

	httpc := c.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Minute}
	}
	resp, err := httpc.Do(req)
	if err != nil {
		_ = pr.CloseWithError(err)
		return Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out uploadResponse
	_ = json.Unmarshal(body, &out)

	switch {
	case resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK:
		return Result{LogID: out.LogID, ReportURL: c.reportURL(out), Duplicate: out.Duplicate}, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return Result{}, ErrUnauthorized
	case resp.StatusCode == http.StatusBadRequest && strings.Contains(out.Message, "already been uploaded"):
		return Result{ReportURL: c.abs(out.Link), Duplicate: true}, nil
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return Result{}, &NotChronicleError{BaseURL: c.BaseURL, Status: resp.StatusCode}
	}
	msg := out.Message
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return Result{}, &Error{Status: resp.StatusCode, Message: msg, Detail: out.Detail}
}

type processingResponse struct {
	Status struct {
		State  string          `json:"state"`
		Output json.RawMessage `json:"output"`
	} `json:"status"`
}

type processingOutput struct {
	Instances []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"instances"`
}

// Reprocess asks Chronicle to retry parsing a previously uploaded log group.
func (c *Client) Reprocess(ctx context.Context, logID string) error {
	if c.Token == "" {
		return ErrUnauthorized
	}
	if _, err := uuid.Parse(logID); err != nil {
		return fmt.Errorf("invalid log ID: %w", err)
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/api/v1/raidlogs/logs/" + url.PathEscape(logID) + "/reparse"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	httpc := c.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusAccepted {
		return &Error{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	return nil
}

// Processing fetches the actual Chronicle parse state and parsed instances for
// a previously uploaded log group.
func (c *Client) Processing(ctx context.Context, logID string) (ProcessingResult, error) {
	if c.Token == "" {
		return ProcessingResult{}, ErrUnauthorized
	}
	if _, err := uuid.Parse(logID); err != nil {
		return ProcessingResult{}, fmt.Errorf("invalid log ID: %w", err)
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/api/v1/raidlogs/logs/" + url.PathEscape(logID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ProcessingResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	httpc := c.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return ProcessingResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return ProcessingResult{}, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return ProcessingResult{}, &Error{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	var state processingResponse
	if err := json.Unmarshal(body, &state); err != nil {
		return ProcessingResult{}, fmt.Errorf("decode processing status: %w", err)
	}
	result := ProcessingResult{State: "processing"}
	var output processingOutput
	if len(state.Status.Output) > 0 && string(state.Status.Output) != "null" {
		if err := json.Unmarshal(state.Status.Output, &output); err != nil {
			return ProcessingResult{}, fmt.Errorf("decode processing output: %w", err)
		}
	}
	switch state.Status.State {
	case "completed":
		result.State = "complete"
	case "discarded":
		// Chronicle uses discarded with reconstructed output when old completed
		// River jobs have been pruned.
		if len(output.Instances) > 0 {
			result.State = "complete"
		} else {
			result.State = "failed"
		}
	case "cancelled":
		result.State = "failed"
	}
	for _, instance := range output.Instances {
		if instance.ID == "" || instance.Name == "" || instance.Slug == "" {
			continue
		}
		result.Instances = append(result.Instances, Instance{
			ID: instance.ID, Name: instance.Name,
			URL: c.abs("/instances/" + url.PathEscape(instance.Slug)),
		})
	}
	if result.State == "failed" {
		result.Message = "Chronicle could not process this log."
	}
	return result, nil
}

type sourceResponse struct {
	// Size is how many bytes of the source the server holds.
	Size int64 `json:"size"`
	// URL is the report of the group it holds for the id.
	URL string `json:"url"`
	// Message comes from error responses.
	Message string `json:"message"`
}

// Have asks the server what it already holds of sourceID, without sending the
// log. It is the pre-flight check that lets a fresh install (or a second PC)
// skip a body Chronicle already has.
//
// A server that holds nothing for the id answers 404, which is a zero Have and
// no error; so does a server too old to know the route. Anything else is an
// error, and the caller should fall back to uploading.
func (c *Client) Have(ctx context.Context, sourceID string) (Have, error) {
	if c.Token == "" {
		return Have{}, ErrUnauthorized
	}
	if sourceID == "" {
		return Have{}, nil
	}

	endpoint := strings.TrimRight(c.BaseURL, "/") + sourcePath + url.PathEscape(sourceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Have{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)

	httpc := c.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return Have{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var out sourceResponse
	_ = json.Unmarshal(body, &out)

	switch resp.StatusCode {
	case http.StatusOK:
		return Have{Size: out.Size, ReportURL: c.abs(out.URL)}, nil
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return Have{}, nil
	case http.StatusUnauthorized:
		return Have{}, ErrUnauthorized
	}
	msg := out.Message
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return Have{}, &Error{Status: resp.StatusCode, Message: msg}
}

func writeForm(mw *multipart.Writer, src io.Reader, size int64, name string, fast bool, storage io.Reader, storageName string) error {
	// The ".gz" suffix is how the server knows the part is compressed.
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="combat_log"; filename="%s.gz"`, name))
	h.Set("Content-Type", "application/gzip")
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	level := gzip.DefaultCompression
	if fast {
		level = gzip.BestSpeed
	}
	zw, err := gzip.NewWriterLevel(part, level)
	if err != nil {
		return err
	}
	if _, err := io.Copy(zw, io.LimitReader(src, size)); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if storage != nil {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="sidekick_storage"; filename="%s"`, storageName))
		h.Set("Content-Type", "text/plain")
		part, err := mw.CreatePart(h)
		if err != nil {
			return err
		}
		if _, err := io.Copy(part, storage); err != nil {
			return err
		}
	}
	return mw.Close()
}

func (c *Client) reportURL(r uploadResponse) string {
	if r.URL != "" {
		return c.abs(r.URL)
	}
	if r.LogID != "" {
		return c.abs("/logs/" + r.LogID)
	}
	return ""
}

func (c *Client) abs(ref string) string {
	if ref == "" {
		return ""
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return ref
	}
	u, err := base.Parse(ref)
	if err != nil {
		return ref
	}
	return u.String()
}
