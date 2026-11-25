package dna

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"bettercontent/backend/internal/platform/postgres"
	"github.com/google/uuid"
)

type testSubject string

func (s testSubject) AuthenticatedUserID() (string, bool) { return string(s), s != "" }

func TestServiceSnapshotsHistoryAndWorkspaceAuthorization(t *testing.T) {
	database := openDNATestDatabase(t)
	ctx := context.Background()
	userID, workspaceID := createDNAOwner(t, database)
	otherUserID, otherWorkspaceID := createDNAOwner(t, database)
	service := NewService(database)
	owner := testSubject(userID)
	other := testSubject(otherUserID)
	partial := []byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"Creator"}}`)
	first, err := service.Save(ctx, owner, workspaceID, nil, partial)
	if err != nil {
		t.Fatal(err)
	}
	if first.VersionNumber != 1 || first.Readiness != Incomplete || !first.IsCurrent {
		t.Fatalf("first snapshot = %+v", first)
	}
	equivalent, err := service.Save(ctx, owner, workspaceID, &first.ID, []byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"  Creator  "}}`))
	if err != nil || equivalent.ID != first.ID || equivalent.VersionNumber != 1 {
		t.Fatalf("equivalent normalized save = %+v, err=%v", equivalent, err)
	}
	changed := []byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"Updated creator"}}`)
	second, err := service.Save(ctx, owner, workspaceID, &first.ID, changed)
	if err != nil {
		t.Fatal(err)
	}
	if second.VersionNumber != 2 || !second.IsCurrent {
		t.Fatalf("second snapshot = %+v", second)
	}
	if _, err := service.Save(ctx, owner, workspaceID, &first.ID, partial); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save error = %v, want conflict", err)
	}
	history, err := service.ListVersions(ctx, owner, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].ID != second.ID || !history[0].IsCurrent || history[1].ID != first.ID || history[1].IsCurrent {
		t.Fatalf("history = %+v", history)
	}
	old, err := service.GetVersion(ctx, owner, workspaceID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Payload.Identity == nil || *old.Payload.Identity.CreatorOrBrandDescription != "Creator" {
		t.Fatalf("historical snapshot was mutated: %+v", old.Payload)
	}
	if _, err := service.GetVersion(ctx, other, otherWorkspaceID, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign version lookup = %v, want nondisclosing not-found", err)
	}
	if _, err := service.Save(ctx, other, workspaceID, &second.ID, partial); err == nil {
		t.Fatal("unrelated user changed owner-only Content DNA")
	}
	current, err := service.GetCurrent(ctx, owner, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CurrentVersion == nil || current.CurrentVersion.ID != second.ID || current.Status != "INCOMPLETE" {
		t.Fatalf("current Content DNA = %+v", current)
	}
	assertDNASnapshotImmutable(t, database, first.ID)
	assertDNAConstraints(t, database, userID, workspaceID)
}

func TestServiceSerializesConcurrentFirstSaves(t *testing.T) {
	database := openDNATestDatabase(t)
	ctx := context.Background()
	userID, workspaceID := createDNAOwner(t, database)
	service := NewService(database)
	owner := testSubject(userID)
	inputs := [][]byte{
		[]byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"First"}}`),
		[]byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"Second"}}`),
	}
	type result struct {
		version Version
		err     error
	}
	results := make(chan result, len(inputs))
	var start sync.WaitGroup
	start.Add(1)
	for _, input := range inputs {
		go func(input []byte) {
			start.Wait()
			version, err := service.Save(ctx, owner, workspaceID, nil, input)
			results <- result{version, err}
		}(input)
	}
	start.Done()
	first := <-results
	second := <-results
	if (first.err == nil) == (second.err == nil) {
		t.Fatalf("save results = (%+v, %+v), want one winner and one conflict", first, second)
	}
	winner := first
	loser := second
	if second.err == nil {
		winner, loser = second, first
	}
	if !winner.version.IsCurrent || winner.version.VersionNumber != 1 || !errors.Is(loser.err, ErrConflict) {
		t.Fatalf("winner/loser = (%+v, %v)", winner.version, loser.err)
	}
	var containers, versions int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM content_dna WHERE workspace_id = $1::uuid`, workspaceID).Scan(&containers); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM content_dna_versions WHERE content_dna_id = (SELECT id FROM content_dna WHERE workspace_id = $1::uuid)`, workspaceID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if containers != 1 || versions != 1 {
		t.Fatalf("container/version counts = %d/%d, want 1/1", containers, versions)
	}
	updates := [][]byte{
		[]byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"Update A"}}`),
		[]byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"Update B"}}`),
	}
	results = make(chan result, len(updates))
	start = sync.WaitGroup{}
	start.Add(1)
	for _, input := range updates {
		go func(input []byte) {
			start.Wait()
			version, err := service.Save(ctx, owner, workspaceID, &winner.version.ID, input)
			results <- result{version, err}
		}(input)
	}
	start.Done()
	first = <-results
	second = <-results
	if (first.err == nil) == (second.err == nil) {
		t.Fatalf("concurrent update results = (%+v, %+v), want one winner and one conflict", first, second)
	}
	winnerUpdate, loserUpdate := first, second
	if second.err == nil {
		winnerUpdate, loserUpdate = second, first
	}
	if winnerUpdate.version.VersionNumber != 2 || !winnerUpdate.version.IsCurrent || !errors.Is(loserUpdate.err, ErrConflict) {
		t.Fatalf("update winner/loser = (%+v, %v)", winnerUpdate.version, loserUpdate.err)
	}
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM content_dna_versions WHERE content_dna_id = (SELECT id FROM content_dna WHERE workspace_id = $1::uuid)`, workspaceID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 2 {
		t.Fatalf("version count after concurrent updates = %d, want 2", versions)
	}
}

func openDNATestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || strings.TrimPrefix(parsed.Path, "/") == "" || !strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") {
		t.Fatal("TEST_DATABASE_URL must target a database ending in _test")
	}
	if primary := os.Getenv("DATABASE_URL"); primary != "" {
		p, parseErr := url.Parse(primary)
		if parseErr != nil || strings.EqualFold(parsed.Host+parsed.Path, p.Host+p.Path) {
			t.Fatal("TEST_DATABASE_URL must not identify DATABASE_URL")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	database, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func createDNAOwner(t *testing.T, database *sql.DB) (string, string) {
	t.Helper()
	userID, workspaceID := uuid.NewString(), uuid.NewString()
	if _, err := database.ExecContext(context.Background(), `INSERT INTO "user" (id, name, email) VALUES ($1, 'DNA test', $2)`, userID, userID+"@example.com"); err != nil {
		t.Fatalf("create DNA user: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO workspaces (id, name) VALUES ($1::uuid, 'DNA test workspace')`, workspaceID); err != nil {
		t.Fatalf("create DNA workspace: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1::uuid, $2, 'owner')`, workspaceID, userID); err != nil {
		t.Fatalf("create DNA owner membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DELETE FROM content_dna_versions WHERE content_dna_id IN (SELECT id FROM content_dna WHERE workspace_id = $1::uuid)`, workspaceID)
		_, _ = database.ExecContext(context.Background(), `DELETE FROM content_dna WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = database.ExecContext(context.Background(), `DELETE FROM workspace_members WHERE workspace_id = $1::uuid`, workspaceID)
		_, _ = database.ExecContext(context.Background(), `DELETE FROM workspaces WHERE id = $1::uuid`, workspaceID)
		_, _ = database.ExecContext(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return userID, workspaceID
}

func assertDNASnapshotImmutable(t *testing.T, database *sql.DB, versionID string) {
	t.Helper()
	if _, err := database.ExecContext(context.Background(), `UPDATE content_dna_versions SET payload = '{"schemaVersion":1}'::jsonb WHERE id = $1::uuid`, versionID); err == nil {
		t.Fatal("database accepted in-place snapshot mutation")
	}
}

func assertDNAConstraints(t *testing.T, database *sql.DB, userID, workspaceID string) {
	t.Helper()
	var containerID string
	if err := database.QueryRowContext(context.Background(), `SELECT id::text FROM content_dna WHERE workspace_id = $1::uuid`, workspaceID).Scan(&containerID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO content_dna_versions (id, content_dna_id, version_number, payload, created_by_user_id) VALUES ($1::uuid, $2::uuid, 1, '{"schemaVersion":1}'::jsonb, $3)`, uuid.NewString(), containerID, userID); err == nil {
		t.Fatal("database accepted duplicate Content DNA version number")
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO content_dna (id, workspace_id, current_version_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`, uuid.NewString(), workspaceID, uuid.NewString()); err == nil {
		t.Fatal("database accepted a second Content DNA container for one workspace")
	}
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `UPDATE content_dna SET current_version_id = $1::uuid WHERE id = $2::uuid`, uuid.NewString(), containerID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("stage invalid current pointer: %v", err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("database committed a current pointer without a matching same-container version")
	}
}
