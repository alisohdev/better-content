package dna

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

var (
	ErrInvalidID = errors.New("content DNA identifier is invalid")
	ErrConflict  = errors.New("content DNA version is no longer current")
	ErrNotFound  = errors.New("content DNA version was not found")
)

type Version struct {
	ID            string    `json:"id"`
	VersionNumber int       `json:"versionNumber"`
	Payload       Payload   `json:"payload"`
	Readiness     Readiness `json:"readiness"`
	CreatedAt     time.Time `json:"createdAt"`
	IsCurrent     bool      `json:"isCurrent"`
}

type Current struct {
	Status         string   `json:"status"`
	CurrentVersion *Version `json:"currentVersion"`
}

type Service struct {
	database *sql.DB
	logger   *slog.Logger
}

func NewService(database *sql.DB) *Service {
	return &Service{database: database, logger: slog.Default()}
}

func (s *Service) GetCurrent(ctx context.Context, subject workspace.MembershipSubject, workspaceID string) (Current, error) {
	id, err := parseWorkspaceID(workspaceID)
	if err != nil {
		return Current{}, err
	}
	if _, err = workspace.RequireMembership(ctx, s.database, subject, id); err != nil {
		return Current{}, err
	}
	version, err := s.loadCurrent(ctx, s.database, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Current{Status: "NOT_CREATED"}, nil
	}
	if err != nil {
		return Current{}, errors.New("could not load current Content DNA")
	}
	version.IsCurrent = true
	return Current{Status: string(version.Readiness), CurrentVersion: &version}, nil
}

func (s *Service) ListVersions(ctx context.Context, subject workspace.MembershipSubject, workspaceID string) ([]Version, error) {
	id, err := parseWorkspaceID(workspaceID)
	if err != nil {
		return nil, err
	}
	if _, err = workspace.RequireMembership(ctx, s.database, subject, id); err != nil {
		return nil, err
	}
	rows, err := s.database.QueryContext(ctx, `
		SELECT v.id::text, v.version_number, v.payload, v.created_at, v.id = c.current_version_id
		FROM content_dna c JOIN content_dna_versions v ON v.content_dna_id = c.id
		WHERE c.workspace_id = $1::uuid ORDER BY v.version_number DESC`, id.String())
	if err != nil {
		return nil, errors.New("could not list Content DNA versions")
	}
	defer rows.Close()
	versions := make([]Version, 0)
	for rows.Next() {
		version, err := scanVersion(rows)
		if err != nil {
			return nil, errors.New("stored Content DNA version is invalid")
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("could not list Content DNA versions")
	}
	return versions, nil
}

func (s *Service) GetVersion(ctx context.Context, subject workspace.MembershipSubject, workspaceID, versionID string) (Version, error) {
	wid, err := parseWorkspaceID(workspaceID)
	if err != nil {
		return Version{}, err
	}
	vid, err := parseID(versionID)
	if err != nil {
		return Version{}, err
	}
	if _, err = workspace.RequireMembership(ctx, s.database, subject, wid); err != nil {
		return Version{}, err
	}
	var version Version
	var raw []byte
	err = s.database.QueryRowContext(ctx, `
		SELECT v.id::text, v.version_number, v.payload, v.created_at, v.id = c.current_version_id
		FROM content_dna c JOIN content_dna_versions v ON v.content_dna_id = c.id
		WHERE c.workspace_id = $1::uuid AND v.id = $2::uuid`, wid.String(), vid).Scan(
		&version.ID, &version.VersionNumber, &raw, &version.CreatedAt, &version.IsCurrent,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	if err != nil {
		return Version{}, errors.New("could not load Content DNA version")
	}
	version.Payload, err = ParsePayload(raw)
	if err != nil {
		return Version{}, errors.New("stored Content DNA version is invalid")
	}
	version.Readiness = version.Payload.Readiness()
	return version, nil
}

func (s *Service) Save(ctx context.Context, subject workspace.MembershipSubject, workspaceID string, baseVersionID *string, rawPayload []byte) (Version, error) {
	version, err := s.save(ctx, subject, workspaceID, baseVersionID, rawPayload)
	userID, _ := subject.AuthenticatedUserID()
	attributes := []any{"module", "dna", "operation", "saveContentDna"}
	if userID != "" {
		attributes = append(attributes, "user_id", userID)
	}
	if id, idErr := parseWorkspaceID(workspaceID); idErr == nil {
		attributes = append(attributes, "workspace_id", id.String())
	}
	if err != nil {
		attributes = append(attributes, "error_code", serviceErrorCode(err))
		s.logger.Warn("dna.save.failed", attributes...)
		return Version{}, err
	}
	s.logger.Info("dna.save.succeeded", attributes...)
	return version, nil
}

func (s *Service) save(ctx context.Context, subject workspace.MembershipSubject, workspaceID string, baseVersionID *string, rawPayload []byte) (Version, error) {
	wid, err := parseWorkspaceID(workspaceID)
	if err != nil {
		return Version{}, err
	}
	if _, err = workspace.RequireOwner(ctx, s.database, subject, wid); err != nil {
		return Version{}, err
	}
	payload, err := ParsePayload(rawPayload)
	if err != nil {
		return Version{}, ErrInvalidPayload
	}
	var baseID *string
	if baseVersionID != nil {
		parsed, parseErr := parseID(*baseVersionID)
		if parseErr != nil {
			return Version{}, parseErr
		}
		baseID = &parsed
	}
	serialized, err := json.Marshal(payload)
	if err != nil {
		return Version{}, errors.New("could not encode Content DNA snapshot")
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return Version{}, errors.New("could not begin Content DNA transaction")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, wid.String()); err != nil {
		return Version{}, errors.New("could not lock Content DNA workspace")
	}
	if _, err := workspace.RequireOwner(ctx, tx, subject, wid); err != nil {
		return Version{}, err
	}
	var containerID, currentID string
	err = tx.QueryRowContext(ctx, `SELECT id::text, current_version_id::text FROM content_dna WHERE workspace_id = $1::uuid FOR UPDATE`, wid.String()).Scan(&containerID, &currentID)
	if errors.Is(err, sql.ErrNoRows) {
		if baseID != nil {
			return Version{}, ErrConflict
		}
		containerID, currentID = uuid.NewString(), uuid.NewString()
		if _, err = tx.ExecContext(ctx, `INSERT INTO content_dna (id, workspace_id, current_version_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`, containerID, wid.String(), currentID); err != nil {
			return Version{}, errors.New("could not create Content DNA container")
		}
		version, err := insertVersion(ctx, tx, currentID, containerID, 1, serialized, subject, true)
		if err != nil {
			return Version{}, err
		}
		if err = tx.Commit(); err != nil {
			return Version{}, errors.New("could not commit Content DNA snapshot")
		}
		return version, nil
	}
	if err != nil {
		return Version{}, errors.New("could not load Content DNA container")
	}
	if baseID == nil || currentID != *baseID {
		return Version{}, ErrConflict
	}
	current, err := s.loadVersion(ctx, tx, currentID, containerID, true)
	if err != nil {
		return Version{}, fmt.Errorf("current Content DNA snapshot is unavailable: %w", err)
	}
	currentJSON, _ := json.Marshal(current.Payload)
	if bytes.Equal(serialized, currentJSON) {
		if err = tx.Commit(); err != nil {
			return Version{}, errors.New("could not commit Content DNA snapshot")
		}
		return current, nil
	}
	version, err := insertVersion(ctx, tx, uuid.NewString(), containerID, current.VersionNumber+1, serialized, subject, true)
	if err != nil {
		return Version{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE content_dna SET current_version_id = $1::uuid, updated_at = now() WHERE id = $2::uuid`, version.ID, containerID); err != nil {
		return Version{}, errors.New("could not select current Content DNA version")
	}
	if err = tx.Commit(); err != nil {
		return Version{}, errors.New("could not commit Content DNA snapshot")
	}
	return version, nil
}

func serviceErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrInvalidID), errors.Is(err, ErrInvalidPayload):
		return "VALIDATION_ERROR"
	case errors.Is(err, workspace.ErrUnauthenticated):
		return "UNAUTHORIZED"
	case errors.Is(err, workspace.ErrForbidden):
		return "FORBIDDEN"
	case errors.Is(err, ErrConflict):
		return "CONFLICT"
	case errors.Is(err, ErrNotFound), errors.Is(err, sql.ErrNoRows):
		return "NOT_FOUND"
	default:
		return "INTERNAL_ERROR"
	}
}

type rowScanner interface{ Scan(...any) error }

func (s *Service) loadCurrent(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, workspaceID workspace.WorkspaceID) (Version, error) {
	var version Version
	var raw []byte
	err := queryer.QueryRowContext(ctx, `
		SELECT v.id::text, v.version_number, v.payload, v.created_at, true
		FROM content_dna c JOIN content_dna_versions v ON v.content_dna_id = c.id AND v.id = c.current_version_id
		WHERE c.workspace_id = $1::uuid`, workspaceID.String()).Scan(&version.ID, &version.VersionNumber, &raw, &version.CreatedAt, &version.IsCurrent)
	if err != nil {
		return Version{}, err
	}
	version.Payload, err = ParsePayload(raw)
	if err != nil {
		return Version{}, fmt.Errorf("invalid persisted payload: %w", err)
	}
	version.Readiness = version.Payload.Readiness()
	return version, nil
}

func (s *Service) loadVersion(ctx context.Context, tx *sql.Tx, versionID, containerID string, current bool) (Version, error) {
	return scanVersion(tx.QueryRowContext(ctx, `SELECT id::text, version_number, payload, created_at, $3::boolean FROM content_dna_versions WHERE id = $1::uuid AND content_dna_id = $2::uuid`, versionID, containerID, current))
}

func scanVersion(row rowScanner) (Version, error) {
	var version Version
	var raw []byte
	if err := row.Scan(&version.ID, &version.VersionNumber, &raw, &version.CreatedAt, &version.IsCurrent); err != nil {
		return Version{}, err
	}
	var err error
	version.Payload, err = ParsePayload(raw)
	if err != nil {
		return Version{}, err
	}
	version.Readiness = version.Payload.Readiness()
	return version, nil
}

func insertVersion(ctx context.Context, tx *sql.Tx, id, containerID string, number int, payload []byte, subject workspace.MembershipSubject, current bool) (Version, error) {
	userID, authenticated := subject.AuthenticatedUserID()
	if !authenticated || userID == "" {
		return Version{}, workspace.ErrUnauthenticated
	}
	return scanVersion(tx.QueryRowContext(ctx, `INSERT INTO content_dna_versions (id, content_dna_id, version_number, payload, created_by_user_id) VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, $5) RETURNING id::text, version_number, payload, created_at, $6::boolean`, id, containerID, number, payload, userID, current))
}

func parseWorkspaceID(value string) (workspace.WorkspaceID, error) {
	id, err := workspace.ParseID(value)
	if err != nil {
		return workspace.WorkspaceID{}, ErrInvalidID
	}
	return id, nil
}

func parseID(value string) (string, error) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return "", ErrInvalidID
	}
	return id.String(), nil
}
