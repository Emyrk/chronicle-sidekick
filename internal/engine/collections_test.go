package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Emyrk/chronicle-sidekick/internal/store"
	"github.com/Emyrk/chronicle-sidekick/internal/wowdir"
	"github.com/stretchr/testify/require"
)

func collectionsEngine(t *testing.T) (*Engine, string, string, string) {
	t.Helper()
	root := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.json")
	st, err := store.Open(statePath)
	require.NoError(t, err)
	source := filepath.Join(root, "WTF", "Account", "A", "SavedVariables", "ChronicleCompanionForever.lua")
	require.NoError(t, os.MkdirAll(filepath.Dir(source), 0700))
	require.NoError(t, os.WriteFile(source, []byte(`ChronicleCompanionForeverDB={settings={sidekick_enabled=true},collections={scouting={a=1},future={unknown="yes"}}}`), 0600))
	install := wowdir.Install{Dir: root}
	e := New(Config{Store: st, Installs: func() []wowdir.Install { return []wowdir.Install{install} }, CaptureForeverScouting: func(wowdir.Install) bool { return true }, Running: func(context.Context, []string) bool { return true }})
	return e, root, source, statePath
}

func collectionJob(t *testing.T, root, name string) job {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.WriteFile(path, []byte("combat"), 0600))
	return job{path: path, size: 6, install: wowdir.Install{Dir: root}, running: true, queuedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func TestCollectionsBatchAndRestartPairEveryLog(t *testing.T) {
	t.Parallel()
	e, root, source, statePath := collectionsEngine(t)
	batch := []job{collectionJob(t, root, "one.txt"), collectionJob(t, root, "two.txt")}
	for i := range batch {
		require.NoError(t, e.prepareSnapshot(context.Background(), &batch[i]))
	}
	e.drainCollections(batch)
	require.NotEqual(t, batch[0].scoutingPath, batch[1].scoutingPath)
	first, err := os.ReadFile(batch[0].scoutingPath)
	require.NoError(t, err)
	second, err := os.ReadFile(batch[1].scoutingPath)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Contains(t, string(first), `collections_lua`)
	require.Contains(t, string(first), `future`)
	live, err := os.ReadFile(source)
	require.NoError(t, err)
	require.Equal(t, `ChronicleCompanionForeverDB={settings={sidekick_enabled=true},collections={}}`, string(live))
	reopened, err := store.Open(statePath)
	require.NoError(t, err)
	restored := New(Config{Store: reopened, Installs: e.cfg.Installs})
	require.Len(t, restored.blocked, 2)
	expected := map[string]string{}
	for _, j := range batch {
		expected[j.snapshotPath] = j.scoutingPath
	}
	for _, j := range restored.blocked {
		require.Equal(t, expected[j.snapshotPath], j.scoutingPath)
	}
	// Restart retries never replace already-owned artifacts with newly flushed data.
	require.NoError(t, os.WriteFile(source, []byte(`ChronicleCompanionForeverDB={collections={new=2}}`), 0600))
	restored.drainCollections(restored.blocked)
	unchanged, err := os.ReadFile(batch[0].scoutingPath)
	require.NoError(t, err)
	require.Equal(t, first, unchanged)
}

func TestCollectionsNoCleanupOnArchiveOrStateFailure(t *testing.T) {
	for _, failure := range []string{"copy", "state"} {
		t.Run(failure, func(t *testing.T) {
			e, root, source, statePath := collectionsEngine(t)
			before, err := os.ReadFile(source)
			require.NoError(t, err)
			j := collectionJob(t, root, "log.txt")
			if failure == "copy" {
				j.size++
			} else {
				require.NoError(t, os.Mkdir(statePath+".tmp", 0700))
			}
			require.Error(t, e.prepareSnapshot(context.Background(), &j))
			e.drainCollections([]job{j})
			after, err := os.ReadFile(source)
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.FileExists(t, j.path)
		})
	}
}

func TestCollectionsAppendKeepsPriorArtifactImmutable(t *testing.T) {
	t.Parallel()
	e, root, source, _ := collectionsEngine(t)
	first := collectionJob(t, root, "first.txt")
	require.NoError(t, e.prepareSnapshot(context.Background(), &first))
	prior, err := os.ReadFile(first.scoutingPath)
	require.NoError(t, err)
	exit := first.queuedAt
	require.NoError(t, e.cfg.Store.Update(func(s *store.State) {
		g := s.ArchiveGroups[first.groupID]
		g.Pending = false
		g.LogID = "log"
		g.LastExitAt = exit
		s.ArchiveGroups[g.ID] = g
	}))
	require.NoError(t, os.WriteFile(source, []byte(`ChronicleCompanionForeverDB={collections={later=true}}`), 0600))
	second := collectionJob(t, root, "second.txt")
	second.startedAt = exit.Add(time.Minute)
	require.NoError(t, e.prepareSnapshot(context.Background(), &second))
	require.Equal(t, first.groupID, second.groupID)
	require.NotEqual(t, first.scoutingPath, second.scoutingPath)
	still, err := os.ReadFile(first.scoutingPath)
	require.NoError(t, err)
	require.Equal(t, prior, still)
	g := e.cfg.Store.Get().ArchiveGroups[first.groupID]
	require.Equal(t, second.snapshotPath, g.Snapshot.CombatPath)
	require.Equal(t, second.scoutingPath, g.Snapshot.CollectionsPath)
}

func TestCollectionsManualRPCArchiveCapturesWithoutPrepareSnapshot(t *testing.T) {
	t.Parallel()
	e, root, source, _ := collectionsEngine(t)
	dir := filepath.Join(root, "ChronicleSidekick", "manual")
	require.NoError(t, os.MkdirAll(dir, 0700))
	combat := filepath.Join(dir, "manual_CombatLog.txt")
	require.NoError(t, os.WriteFile(combat, []byte("combat"), 0600))
	request := store.AddonRequest{ID: "request", ArchiveGroupID: "manual", CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	require.NoError(t, e.cfg.Store.Update(func(s *store.State) {
		s.ArchiveGroups = map[string]store.ArchiveGroup{"manual": {ID: "manual", InstallDirectory: root, Pending: true, ManualUpload: true, Files: []string{combat}}}
	}))
	require.NoError(t, e.queueAddonRequest("request", request, wowdir.Install{Dir: root}))
	require.Len(t, e.pending, 1)
	require.Empty(t, e.pending[0].scoutingPath)
	e.drainCollections(e.pending)
	require.NotEmpty(t, e.pending[0].scoutingPath)
	artifact, err := os.ReadFile(e.pending[0].scoutingPath)
	require.NoError(t, err)
	require.Contains(t, string(artifact), `future`)
	live, err := os.ReadFile(source)
	require.NoError(t, err)
	require.Contains(t, string(live), `collections={}`)
	require.Equal(t, e.pending[0].scoutingPath, e.cfg.Store.Get().ArchiveGroups["manual"].Snapshot.CollectionsPath)
}

func TestCollectionsStateFailureDuringDrainLeavesSource(t *testing.T) {
	t.Parallel()
	e, root, source, statePath := collectionsEngine(t)
	batch := []job{collectionJob(t, root, "log.txt")}
	require.NoError(t, e.prepareSnapshot(context.Background(), &batch[0]))
	before, err := os.ReadFile(source)
	require.NoError(t, err)
	originalArtifact := batch[0].scoutingPath
	require.NoError(t, os.Mkdir(statePath+".tmp", 0700))
	e.drainCollections(batch)
	after, err := os.ReadFile(source)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, originalArtifact, batch[0].scoutingPath)
	require.Equal(t, originalArtifact, e.cfg.Store.Get().ArchiveGroups[batch[0].groupID].Snapshot.CollectionsPath)
}
