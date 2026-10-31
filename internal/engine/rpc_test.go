package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/Emyrk/chronicle-sidekick/internal/store"
	"github.com/Emyrk/chronicle-sidekick/internal/upload"
	"github.com/Emyrk/chronicle-sidekick/internal/wowdir"
)

func TestLogIDFromReportURL(t *testing.T) {
	t.Parallel()
	const id = "550e8400-e29b-41d4-a716-446655440000"
	require.Equal(t, id, logIDFromReportURL("https://chronicle.example/logs/"+id))
	require.Empty(t, logIDFromReportURL("https://chronicle.example/logs/file/hash"))
}

func TestRPCStateAllowlist(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"queued", "uploading", "processing", "complete", "failed"} {
		require.Equal(t, state, rpcState(state))
	}
	require.Equal(t, "failed", rpcState("processing\tforged\nINSTANCE"))
}

func TestCleanRPCFieldIsBoundedAndValidUTF8(t *testing.T) {
	t.Parallel()
	got := cleanAddonField(strings.Repeat("界", rpcFieldLimit))
	require.LessOrEqual(t, len(got), rpcFieldLimit)
	require.True(t, utf8.ValidString(got))
}

func TestRPCFailureMessageDoesNotExposeLocalOrServerDetails(t *testing.T) {
	t.Parallel()
	local := &os.PathError{Op: "open", Path: `C:\\Games\\WoW\\secret.txt`, Err: os.ErrPermission}
	require.NotContains(t, rpcFailureMessage(local), "secret.txt")
	server := &upload.Error{Status: 400, Message: "bad", Detail: "internal server detail"}
	require.Equal(t, "Chronicle rejected the upload (HTTP 400).", rpcFailureMessage(server))
}

func TestParseRPCRequestsIgnoresPartialUnsupportedAndMalformedRecords(t *testing.T) {
	t.Parallel()
	body := []byte("\n1\trequest\tTester_1\t1000\tcombat_log.upload\tChronicleUpload_Tester_1.txt\n" +
		"1\trequest\tbad/id\t1000\tcombat_log.upload\tChronicleUpload_bad.txt\n" +
		"1\trequest\tTester_2\t1000\tinstance.prepare\tChronicleUpload_Tester_2.txt\n" +
		"1\tupload\tlegacy\t1000\tChronicleUpload_legacy.txt\n" +
		"1\tretry\tTester_1\t1001\tcombat_log.upload\tChronicleUpload_Tester_1.txt")

	requests := parseRPCRequests(body)
	require.Len(t, requests, 1)
	require.Equal(t, "Tester_1", requests[0].id)
	require.Equal(t, "request", requests[0].action)
	require.Equal(t, rpcUploadMethod, requests[0].method)
	require.Equal(t, time.Unix(1000, 0), requests[0].created)
}

func TestCompanionRPCInstallsRequiresEnabledAndRunning(t *testing.T) {
	t.Parallel()
	installs := []wowdir.Install{{Dir: "running-1.12"}, {Dir: "stopped-1.12"}, {Dir: "running-other"}}
	running := map[string]bool{"running-1.12": true, "stopped-1.12": false, "running-other": true}
	enabled := func(install wowdir.Install) bool { return install.Dir != "running-other" }

	require.Equal(t, installs[:1], companionRPCInstalls(installs, running, enabled))
	require.Nil(t, companionRPCInstalls(installs, running, nil))
}

func TestAddonRequestMovesIntoManagedArchiveAndPublishesStatus(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	customData := filepath.Join(root, "CustomData")
	require.NoError(t, os.MkdirAll(customData, 0o700))
	const id = "Tester_Realm_1000_1"
	archiveName := "ChronicleUpload_" + id + ".txt"
	source := filepath.Join(customData, archiveName)
	require.NoError(t, os.WriteFile(source, []byte("0|HEADER\n1|EVENT\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(customData, rpcRequestsName), []byte("1\trequest\t"+id+"\t1000\t"+rpcUploadMethod+"\t"+archiveName+"\n"), 0o600))

	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, err)
	install := wowdir.Install{Dir: root, BaseURL: "https://chronicle.example"}
	e := New(Config{
		Store: st, Installs: func() []wowdir.Install { return []wowdir.Install{install} },
		Token: func() string { return "token" }, Version: "1.2.3",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	now := time.Unix(1010, 0)
	e.scanRPCRequests(context.Background(), now, []wowdir.Install{install})

	request := st.Get().AddonRequests[addonRequestKey(root, id)]
	require.Equal(t, rpcUploadMethod, request.Method)
	require.Equal(t, "queued", request.State)
	require.NotEmpty(t, request.ArchiveGroupID)
	require.NoFileExists(t, source)
	group := st.Get().ArchiveGroups[request.ArchiveGroupID]
	require.Len(t, group.Files, 1)
	require.FileExists(t, group.Files[0])
	body, err := os.ReadFile(filepath.Join(customData, rpcStatusName))
	require.NoError(t, err)
	require.Contains(t, string(body), "RPC\t1\t1.2.3\t1010\tready\t\n")
	require.NoFileExists(t, filepath.Join(customData, "ChronicleSidekick.status"))
	require.Contains(t, string(body), "RESPONSE\t"+id+"\t"+rpcUploadMethod+"\tqueued\t1000\t1010\t\n")

	e.mu.Lock()
	require.Len(t, e.pending, 1)
	require.Equal(t, group.Files[0], e.pending[0].snapshotPath)
	e.mu.Unlock()

	// Re-reading the append-only request file is idempotent.
	e.scanRPCRequests(context.Background(), now.Add(time.Second), []wowdir.Install{install})
	e.mu.Lock()
	require.Len(t, e.pending, 1)
	e.mu.Unlock()
}

func TestAddonRetryRequeuesFailedManagedArchive(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	customData := filepath.Join(root, "CustomData")
	require.NoError(t, os.MkdirAll(customData, 0o700))
	const id = "Tester_Realm_1000_2"
	archiveName := "ChronicleUpload_" + id + ".txt"
	require.NoError(t, os.WriteFile(filepath.Join(customData, archiveName), []byte("log\n"), 0o600))
	requestsPath := filepath.Join(customData, rpcRequestsName)
	require.NoError(t, os.WriteFile(requestsPath, []byte("1\trequest\t"+id+"\t1000\t"+rpcUploadMethod+"\t"+archiveName+"\n"), 0o600))

	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, err)
	install := wowdir.Install{Dir: root, BaseURL: "https://chronicle.example"}
	e := New(Config{Store: st, Installs: func() []wowdir.Install { return []wowdir.Install{install} }, Token: func() string { return "token" }})
	now := time.Unix(1010, 0)
	e.scanRPCRequests(context.Background(), now, []wowdir.Install{install})
	key := addonRequestKey(root, id)
	e.updateAddonRequest(key, "failed", "try again", upload.Result{}, now)
	e.mu.Lock()
	e.pending = nil // model the completed failed job
	e.mu.Unlock()

	require.NoError(t, os.WriteFile(requestsPath, []byte("1\trequest\t"+id+"\t1000\t"+rpcUploadMethod+"\t"+archiveName+"\n1\tretry\t"+id+"\t1020\t"+rpcUploadMethod+"\t"+archiveName+"\n"), 0o600))
	e.scanRPCRequests(context.Background(), time.Unix(1020, 0), []wowdir.Install{install})

	require.Equal(t, time.Unix(1020, 0), st.Get().AddonRequests[key].LastRetryAt)
	require.Equal(t, "queued", st.Get().AddonRequests[key].State)
	e.mu.Lock()
	require.Len(t, e.pending, 1)
	e.mu.Unlock()

	// The append-only retry record is consumed once. A later failure requires a
	// newer retry record instead of replaying the old one every poll.
	e.mu.Lock()
	e.pending = nil
	e.mu.Unlock()
	e.updateAddonRequest(key, "failed", "failed again", upload.Result{}, time.Unix(1021, 0))
	e.scanRPCRequests(context.Background(), time.Unix(1022, 0), []wowdir.Install{install})
	require.Equal(t, "failed", st.Get().AddonRequests[key].State)
	e.mu.Lock()
	require.Empty(t, e.pending)
	e.mu.Unlock()
}

func TestAddonProcessingPublishesParsedInstances(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	customData := filepath.Join(root, "CustomData")
	require.NoError(t, os.MkdirAll(customData, 0o700))
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, err)
	const key = "install|request"
	require.NoError(t, st.Update(func(s *store.State) {
		s.AddonRequests = map[string]store.AddonRequest{key: {
			ID: "request", InstallDirectory: root, ArchiveBasename: "ChronicleUpload_request.txt",
			State: "processing", LogID: "550e8400-e29b-41d4-a716-446655440000",
			CreatedAt: time.Unix(1000, 0), UpdatedAt: time.Unix(1010, 0),
		}}
	}))
	install := wowdir.Install{Dir: root, BaseURL: "https://chronicle.example"}
	e := New(Config{
		Store: st, Installs: func() []wowdir.Install { return []wowdir.Install{install} },
		Token: func() string { return "token" },
		Processing: func(context.Context, string, string, string) (upload.ProcessingResult, error) {
			return upload.ProcessingResult{State: "complete", Instances: []upload.Instance{
				{ID: "instance-1", Name: "Molten Core", URL: "https://chronicle.example/instances/molten-core"},
				{ID: "instance-2", Name: "Blackwing Lair", URL: "https://chronicle.example/instances/blackwing-lair"},
			}}, nil
		},
	})
	e.scanRPCRequests(context.Background(), time.Unix(1020, 0), []wowdir.Install{install})

	request := st.Get().AddonRequests[key]
	require.Equal(t, "complete", request.State)
	require.Len(t, request.Instances, 2)
	body, err := os.ReadFile(filepath.Join(customData, rpcStatusName))
	require.NoError(t, err)
	require.Contains(t, string(body), "RESPONSE\trequest\t"+rpcUploadMethod+"\tcomplete")
	require.Contains(t, string(body), "INSTANCE\trequest\tinstance-1\tMolten Core\thttps://chronicle.example/instances/molten-core\n")
	require.Contains(t, string(body), "INSTANCE\trequest\tinstance-2\tBlackwing Lair\thttps://chronicle.example/instances/blackwing-lair\n")
}

func TestAddonUploadWaitsForAuthenticationWithoutDroppingJob(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	archive := filepath.Join(root, "ChronicleSidekick", "group", "log_CombatLog.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(archive), 0o700))
	require.NoError(t, os.WriteFile(archive, []byte("log\n"), 0o600))
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, err)
	const key = "install|request"
	require.NoError(t, st.Update(func(s *store.State) {
		s.AddonRequests = map[string]store.AddonRequest{key: {
			ID: "request", InstallDirectory: root, ArchiveBasename: "ChronicleUpload_request.txt",
			ArchiveGroupID: "group", State: "queued", CreatedAt: time.Unix(1000, 0), UpdatedAt: time.Unix(1000, 0),
		}}
		s.ArchiveGroups = map[string]store.ArchiveGroup{"group": {
			ID: "group", InstallDirectory: root, Pending: true, Files: []string{archive},
		}}
	}))
	install := wowdir.Install{Dir: root, BaseURL: "https://chronicle.example"}
	e := New(Config{
		Store: st, Installs: func() []wowdir.Install { return []wowdir.Install{install} },
		Token: func() string { return "" },
		Upload: func(context.Context, string, UploadRequest) (upload.Result, error) {
			t.Fatal("upload must not run while signed out")
			return upload.Result{}, nil
		},
	})
	e.tokenLoaded = true
	e.token = ""
	rejected, _ := e.uploadOne(context.Background(), job{
		path: archive, snapshotPath: archive, size: 4, install: install,
		groupID: "group", addonRequestID: key, sourceRemoved: true,
	})
	require.True(t, rejected, "addon job should be retained in the blocked queue")
	request := st.Get().AddonRequests[key]
	require.Equal(t, "queued", request.State)
	require.Contains(t, request.Message, "sign-in")
}

func TestAddonRetryReprocessesUploadedFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	customData := filepath.Join(root, "CustomData")
	require.NoError(t, os.MkdirAll(customData, 0o700))
	const id = "Tester_Realm_1000_3"
	const logID = "550e8400-e29b-41d4-a716-446655440000"
	archiveName := "ChronicleUpload_" + id + ".txt"
	require.NoError(t, os.WriteFile(filepath.Join(customData, rpcRequestsName), []byte("1\tretry\t"+id+"\t1020\t"+rpcUploadMethod+"\t"+archiveName+"\n"), 0o600))
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, err)
	key := addonRequestKey(root, id)
	require.NoError(t, st.Update(func(s *store.State) {
		s.AddonRequests = map[string]store.AddonRequest{key: {
			ID: id, InstallDirectory: root, ArchiveBasename: archiveName,
			State: "failed", Message: "parser failed", LogID: logID,
			CreatedAt: time.Unix(1000, 0), UpdatedAt: time.Unix(1010, 0),
		}}
	}))
	install := wowdir.Install{Dir: root, BaseURL: "https://chronicle.example"}
	called := false
	e := New(Config{
		Store: st, Installs: func() []wowdir.Install { return []wowdir.Install{install} },
		Token: func() string { return "token" },
		Reprocess: func(_ context.Context, token, baseURL, gotLogID string) error {
			called = true
			require.Equal(t, "token", token)
			require.Equal(t, install.BaseURL, baseURL)
			require.Equal(t, logID, gotLogID)
			return nil
		},
		Processing: func(context.Context, string, string, string) (upload.ProcessingResult, error) {
			return upload.ProcessingResult{State: "processing"}, nil
		},
	})
	e.scanRPCRequests(context.Background(), time.Unix(1020, 0), []wowdir.Install{install})
	require.True(t, called)
	request := st.Get().AddonRequests[key]
	require.Equal(t, "processing", request.State)
	require.Empty(t, request.Message)
}

func TestRPCStatusIsBoundedAndPrioritizesOutstandingRequests(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, err)
	now := time.Unix(2000, 0)
	require.NoError(t, st.Update(func(s *store.State) {
		s.AddonRequests = map[string]store.AddonRequest{
			"outstanding": {
				ID: "outstanding", Method: rpcUploadMethod, InstallDirectory: root,
				ArchiveBasename: "ChronicleUpload_outstanding.txt", State: "processing",
				Message: strings.Repeat("界", rpcStatusLimit), CreatedAt: time.Unix(1, 0), UpdatedAt: now,
			},
		}
		for i := 0; i < 400; i++ {
			id := fmt.Sprintf("complete_%03d", i)
			s.AddonRequests[id] = store.AddonRequest{
				ID: id, Method: rpcUploadMethod, InstallDirectory: root,
				ArchiveBasename: "ChronicleUpload_" + id + ".txt", State: "complete",
				Message: strings.Repeat("x", 1024), CreatedAt: now.Add(-time.Duration(i) * time.Second), UpdatedAt: now,
			}
		}
	}))
	install := wowdir.Install{Dir: root, BaseURL: "https://chronicle.example"}
	e := New(Config{
		Store: st, Installs: func() []wowdir.Install { return []wowdir.Install{install} },
		Token: func() string { return "token" }, Version: "test",
	})
	e.publishRPCStatus(now, install)
	body, err := os.ReadFile(filepath.Join(root, "CustomData", rpcStatusName))
	require.NoError(t, err)
	require.LessOrEqual(t, len(body), rpcStatusLimit)
	require.Contains(t, string(body), "RESPONSE\toutstanding\tcombat_log.upload\tprocessing")
}

func TestRPCRetryWhileQueuedIsConsumedWithoutLaterAutoRetry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	customData := filepath.Join(root, "CustomData")
	require.NoError(t, os.MkdirAll(customData, 0o700))
	const id = "queued_retry"
	archiveName := "ChronicleUpload_" + id + ".txt"
	require.NoError(t, os.WriteFile(filepath.Join(customData, archiveName), []byte("log\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(customData, rpcRequestsName), []byte(
		"1\trequest\t"+id+"\t1000\t"+rpcUploadMethod+"\t"+archiveName+"\n"+
			"1\tretry\t"+id+"\t1001\t"+rpcUploadMethod+"\t"+archiveName+"\n",
	), 0o600))
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, err)
	install := wowdir.Install{Dir: root, BaseURL: "https://chronicle.example"}
	e := New(Config{Store: st, Installs: func() []wowdir.Install { return []wowdir.Install{install} }, Token: func() string { return "token" }})
	e.scanRPCRequests(context.Background(), time.Unix(1010, 0), []wowdir.Install{install})
	key := addonRequestKey(root, id)
	require.Equal(t, time.Unix(1001, 0), st.Get().AddonRequests[key].LastRetryAt)

	e.mu.Lock()
	e.pending = nil
	e.mu.Unlock()
	e.updateAddonRequest(key, "failed", "failed later", upload.Result{}, time.Unix(1011, 0))
	e.scanRPCRequests(context.Background(), time.Unix(1012, 0), []wowdir.Install{install})
	require.Equal(t, "failed", st.Get().AddonRequests[key].State)
	e.mu.Lock()
	require.Empty(t, e.pending)
	e.mu.Unlock()
}

func TestRPCProcessingUnauthorizedSetsSignInReadiness(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, err)
	key := addonRequestKey(root, "processing_auth")
	require.NoError(t, st.Update(func(s *store.State) {
		s.AddonRequests = map[string]store.AddonRequest{key: {
			ID: "processing_auth", Method: rpcUploadMethod, InstallDirectory: root,
			ArchiveBasename: "ChronicleUpload_processing_auth.txt", State: "processing",
			LogID: "550e8400-e29b-41d4-a716-446655440000", CreatedAt: time.Unix(1000, 0), UpdatedAt: time.Unix(1000, 0),
		}}
	}))
	install := wowdir.Install{Dir: root, BaseURL: "https://chronicle.example"}
	e := New(Config{
		Store: st, Installs: func() []wowdir.Install { return []wowdir.Install{install} },
		Token: func() string { return "rejected-token" }, Version: "test",
		Processing: func(context.Context, string, string, string) (upload.ProcessingResult, error) {
			return upload.ProcessingResult{}, upload.ErrUnauthorized
		},
	})
	e.scanRPCRequests(context.Background(), time.Unix(1010, 0), []wowdir.Install{install})
	body, err := os.ReadFile(filepath.Join(root, "CustomData", rpcStatusName))
	require.NoError(t, err)
	require.Contains(t, string(body), "RPC\t1\ttest\t1010\tsign_in_required\t")
	require.Contains(t, st.Get().AddonRequests[key].Message, "sign-in")
}

func TestRPCFailedReparseConsumesRetryRecord(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	customData := filepath.Join(root, "CustomData")
	require.NoError(t, os.MkdirAll(customData, 0o700))
	const id = "failed_reparse"
	const logID = "550e8400-e29b-41d4-a716-446655440000"
	archiveName := "ChronicleUpload_" + id + ".txt"
	require.NoError(t, os.WriteFile(filepath.Join(customData, rpcRequestsName), []byte(
		"1\tretry\t"+id+"\t1020\t"+rpcUploadMethod+"\t"+archiveName+"\n",
	), 0o600))
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, err)
	key := addonRequestKey(root, id)
	require.NoError(t, st.Update(func(s *store.State) {
		s.AddonRequests = map[string]store.AddonRequest{key: {
			ID: id, Method: rpcUploadMethod, InstallDirectory: root, ArchiveBasename: archiveName,
			State: "failed", LogID: logID, CreatedAt: time.Unix(1000, 0), UpdatedAt: time.Unix(1010, 0),
		}}
	}))
	install := wowdir.Install{Dir: root, BaseURL: "https://chronicle.example"}
	calls := 0
	e := New(Config{
		Store: st, Installs: func() []wowdir.Install { return []wowdir.Install{install} },
		Token: func() string { return "token" },
		Reprocess: func(context.Context, string, string, string) error {
			calls++
			return errors.New("temporary reparse failure")
		},
	})
	e.scanRPCRequests(context.Background(), time.Unix(1020, 0), []wowdir.Install{install})
	e.scanRPCRequests(context.Background(), time.Unix(1021, 0), []wowdir.Install{install})
	require.Equal(t, 1, calls)
	request := st.Get().AddonRequests[key]
	require.Equal(t, "failed", request.State)
	require.Equal(t, time.Unix(1020, 0), request.LastRetryAt)
}
