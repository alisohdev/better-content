package workspace

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"bettercontent/backend/internal/platform/postgres"
	"github.com/google/uuid"
)

type testSubject struct {
	userID        string
	authenticated bool
}

func (s testSubject) AuthenticatedUserID() (string, bool) {
	return s.userID, s.authenticated
}

func TestRequireMembershipRejectsZeroIdentityAndMalformedWorkspaceID(t *testing.T) {
	if _, err := RequireMembership(context.Background(), nil, testSubject{}, WorkspaceID(uuid.New())); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("zero identity error = %v, want %v", err, ErrUnauthenticated)
	}
	if _, err := ParseID("not-a-uuid"); !errors.Is(err, ErrInvalidWorkspaceID) {
		t.Fatalf("malformed workspace ID parse error = %v, want %v", err, ErrInvalidWorkspaceID)
	}
	if _, err := RequireMembership(context.Background(), nil, testSubject{userID: "caller", authenticated: true}, WorkspaceID{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("malformed workspace ID error = %v, want %v", err, ErrForbidden)
	}
}

func TestDefaultWorkspaceIsIdempotentAndMembershipIsScoped(t *testing.T) {
	database := openTestDatabase(t)
	ctx := context.Background()
	userID := uuid.NewString()
	email := userID + "@example.com"
	if _, err := database.ExecContext(ctx, `
		INSERT INTO "user" (id, name, email, email_verified, created_at, updated_at)
		VALUES ($1, 'Test owner', $2, false, now(), now())
	`, userID, email); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() { _, _ = database.ExecContext(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID) })

	first := provision(t, ctx, database, userID)
	second := provision(t, ctx, database, userID)
	if first.ID != second.ID || first.Name != defaultWorkspaceName {
		t.Fatalf("workspace was not idempotent: first=%+v second=%+v", first, second)
	}

	owner := testSubject{userID: userID, authenticated: true}
	if _, err := RequireOwner(ctx, database, owner, first.ID); err != nil {
		t.Fatalf("owner should have access: %v", err)
	}
	other := testSubject{userID: uuid.NewString(), authenticated: true}
	if _, err := RequireMembership(ctx, database, other, first.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unrelated user error = %v, want %v", err, ErrForbidden)
	}
}

func TestDefaultWorkspaceConcurrentProvisioning(t *testing.T) {
	database := openTestDatabase(t)
	ctx := context.Background()
	userID := uuid.NewString()
	if _, err := database.ExecContext(ctx, `
		INSERT INTO "user" (id, name, email, email_verified, created_at, updated_at)
		VALUES ($1, 'Concurrent owner', $2, false, now(), now())
	`, userID, userID+"@example.com"); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() { _, _ = database.ExecContext(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID) })

	const callers = 8
	results := make(chan Workspace, callers)
	errorsFound := make(chan error, callers)
	for range callers {
		go func() {
			workspace, err := provisionWithContext(ctx, database, userID)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- workspace
		}()
	}

	var id WorkspaceID
	for range callers {
		select {
		case err := <-errorsFound:
			t.Fatalf("concurrent workspace creation failed: %v", err)
		case workspace := <-results:
			if id.IsZero() {
				id = workspace.ID
			} else if id != workspace.ID {
				t.Fatalf("concurrent requests created different workspaces: %s and %s", id.String(), workspace.ID.String())
			}
		}
	}
	var memberships int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM workspace_members WHERE user_id = $1`, userID).Scan(&memberships); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	if memberships != 1 {
		t.Fatalf("membership count = %d, want 1", memberships)
	}
}

func provision(t *testing.T, ctx context.Context, database *sql.DB, userID string) Workspace {
	t.Helper()
	result, err := provisionWithContext(ctx, database, userID)
	if err != nil {
		t.Fatalf("provision workspace: %v", err)
	}
	return result
}

func provisionWithContext(ctx context.Context, database *sql.DB, userID string) (Workspace, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback()
	result, err := GetOrCreateDefaultWorkspace(ctx, tx, userID)
	if err != nil {
		return Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return Workspace{}, err
	}
	return result, nil
}

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Path == "" || !strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") {
		t.Fatal("TEST_DATABASE_URL must point to a database whose name ends in _test")
	}
	if primaryURL := os.Getenv("DATABASE_URL"); primaryURL != "" {
		primary, parseErr := url.Parse(primaryURL)
		if parseErr != nil {
			t.Fatal("DATABASE_URL is invalid")
		}
		if strings.EqualFold(parsed.Host+parsed.Path, primary.Host+primary.Path) {
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
