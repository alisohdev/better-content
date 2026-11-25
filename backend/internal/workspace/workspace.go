package workspace

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

const defaultWorkspaceName = "Personal workspace"

var (
	ErrUnauthenticated    = errors.New("authenticated identity is required")
	ErrForbidden          = errors.New("workspace access is forbidden")
	ErrInvalidWorkspaceID = errors.New("workspace ID is invalid")
)

type WorkspaceID uuid.UUID

type Workspace struct {
	ID        WorkspaceID
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func ParseID(value string) (WorkspaceID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil {
		return WorkspaceID{}, ErrInvalidWorkspaceID
	}
	return WorkspaceID(parsed), nil
}

func (id WorkspaceID) String() string { return uuid.UUID(id).String() }

func (id WorkspaceID) IsZero() bool { return uuid.UUID(id) == uuid.Nil }

type MembershipSubject interface {
	AuthenticatedUserID() (string, bool)
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// GetOrCreateDefaultWorkspace is called inside the identity transaction. The
// advisory lock and unique membership index preserve one personal workspace
// when first sign-in races across requests.
func GetOrCreateDefaultWorkspace(ctx context.Context, tx *sql.Tx, userID string) (Workspace, error) {
	if userID == "" {
		return Workspace{}, ErrUnauthenticated
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, userID); err != nil {
		return Workspace{}, errors.New("could not lock default workspace")
	}

	workspace, err := findForUser(ctx, tx, userID)
	if err == nil {
		return workspace, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, errors.New("could not read default workspace")
	}

	var id string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO workspaces (name) VALUES ($1)
		RETURNING id::text, name, created_at, updated_at
	`, defaultWorkspaceName).Scan(&id, &workspace.Name, &workspace.CreatedAt, &workspace.UpdatedAt); err != nil {
		return Workspace{}, errors.New("could not create default workspace")
	}
	workspace.ID, err = ParseID(id)
	if err != nil {
		return Workspace{}, errors.New("could not parse created workspace ID")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workspace_members (workspace_id, user_id, role)
		VALUES ($1::uuid, $2, 'owner')
	`, workspace.ID.String(), userID); err != nil {
		return Workspace{}, errors.New("could not create default workspace membership")
	}
	return workspace, nil
}

func RequireMembership(ctx context.Context, database rowQueryer, subject MembershipSubject, workspaceID WorkspaceID) (Workspace, error) {
	userID, authenticated := subject.AuthenticatedUserID()
	if !authenticated || userID == "" {
		return Workspace{}, ErrUnauthenticated
	}
	if workspaceID.IsZero() {
		return Workspace{}, ErrForbidden
	}
	return findMembership(ctx, database, userID, workspaceID, false)
}

func RequireOwner(ctx context.Context, database rowQueryer, subject MembershipSubject, workspaceID WorkspaceID) (Workspace, error) {
	userID, authenticated := subject.AuthenticatedUserID()
	if !authenticated || userID == "" {
		return Workspace{}, ErrUnauthenticated
	}
	if workspaceID.IsZero() {
		return Workspace{}, ErrForbidden
	}
	return findMembership(ctx, database, userID, workspaceID, true)
}

func findMembership(ctx context.Context, database rowQueryer, userID string, workspaceID WorkspaceID, ownerOnly bool) (Workspace, error) {
	query := `
		SELECT w.id::text, w.name, w.created_at, w.updated_at
		FROM workspaces AS w
		JOIN workspace_members AS wm ON wm.workspace_id = w.id
		WHERE wm.user_id = $1 AND wm.workspace_id = $2::uuid
	`
	if ownerOnly {
		query += ` AND wm.role = 'owner'`
	}
	var result Workspace
	var id string
	if err := database.QueryRowContext(ctx, query, userID, workspaceID.String()).Scan(
		&id, &result.Name, &result.CreatedAt, &result.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Workspace{}, ErrForbidden
		}
		return Workspace{}, errors.New("could not verify workspace membership")
	}
	parsedID, err := ParseID(id)
	if err != nil {
		return Workspace{}, errors.New("could not parse workspace membership ID")
	}
	result.ID = parsedID
	return result, nil
}

func findForUser(ctx context.Context, tx *sql.Tx, userID string) (Workspace, error) {
	var result Workspace
	var id string
	err := tx.QueryRowContext(ctx, `
		SELECT w.id::text, w.name, w.created_at, w.updated_at
		FROM workspace_members AS wm
		JOIN workspaces AS w ON w.id = wm.workspace_id
		WHERE wm.user_id = $1
	`, userID).Scan(&id, &result.Name, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return Workspace{}, err
	}
	result.ID, err = ParseID(id)
	return result, err
}
