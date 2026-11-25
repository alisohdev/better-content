package content

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

var (
	ErrQueueValidation = errors.New("production queue request is invalid")
	ErrQueueConflict   = errors.New("production queue changed")
)

type QueueAttempt struct {
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	ErrorCategory *string    `json:"errorCategory"`
	CreatedAt     time.Time  `json:"createdAt"`
	FailedAt      *time.Time `json:"failedAt"`
}

type QueueItem struct {
	ID                      string        `json:"id"`
	Title                   string        `json:"title"`
	Description             string        `json:"description"`
	Language                string        `json:"language"`
	ProductionQueuePosition int           `json:"productionQueuePosition"`
	LastAttempt             *QueueAttempt `json:"lastAttempt"`
}

type QueueService struct{ database *sql.DB }

func NewQueueService(database *sql.DB) *QueueService { return &QueueService{database: database} }

func (s *QueueService) Get(ctx context.Context, subject workspace.MembershipSubject, workspaceID string) ([]QueueItem, error) {
	parsed, err := workspace.ParseID(workspaceID)
	if err != nil {
		return nil, ErrQueueValidation
	}
	if _, err := workspace.RequireMembership(ctx, s.database, subject, parsed); err != nil {
		return nil, err
	}
	return listQueue(ctx, s.database, parsed.String())
}

func (s *QueueService) Reorder(ctx context.Context, subject workspace.MembershipSubject, workspaceID string, orderedIdeaIDs []string) ([]QueueItem, error) {
	parsed, err := workspace.ParseID(workspaceID)
	if err != nil {
		return nil, ErrQueueValidation
	}
	canonicalIDs := make([]string, len(orderedIdeaIDs))
	seen := make(map[string]struct{}, len(orderedIdeaIDs))
	for index, id := range orderedIdeaIDs {
		parsedID, parseErr := uuid.Parse(id)
		if parseErr != nil || parsedID == uuid.Nil {
			return nil, ErrQueueValidation
		}
		canonicalIDs[index] = parsedID.String()
		if _, exists := seen[canonicalIDs[index]]; exists {
			return nil, ErrQueueConflict
		}
		seen[canonicalIDs[index]] = struct{}{}
	}
	if _, err := workspace.RequireOwner(ctx, s.database, subject, parsed); err != nil {
		return nil, err
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, errors.New("could not begin production queue reorder")
	}
	defer tx.Rollback()
	if err := lockWorkspace(ctx, tx, parsed.String()); err != nil {
		return nil, err
	}
	if _, err := workspace.RequireOwner(ctx, tx, subject, parsed); err != nil {
		return nil, err
	}
	if err := lockWorkspaceIdeas(ctx, tx, parsed.String()); err != nil {
		return nil, err
	}
	current, err := listQueue(ctx, tx, parsed.String())
	if err != nil {
		return nil, err
	}
	if !sameIDs(current, canonicalIDs) {
		return nil, ErrQueueConflict
	}
	for index, ideaID := range canonicalIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE ideas SET production_queue_position = $3 WHERE id = $1::uuid AND batch_id IN (SELECT id FROM idea_generation_batches WHERE workspace_id = $2::uuid)`, ideaID, parsed.String(), index+1); err != nil {
			return nil, errors.New("could not update production queue order")
		}
	}
	finalQueue, err := listQueue(ctx, tx, parsed.String())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, errors.New("could not commit production queue reorder")
	}
	return finalQueue, nil
}

// ReconcileIdeaQueueAfterDecision runs inside the caller's transaction. The
// caller must lock the workspace row first; this function then locks all Ideas
// in stable ID order before touching queue positions.
func ReconcileIdeaQueueAfterDecision(ctx context.Context, tx *sql.Tx, workspaceID, ideaID string) error {
	if err := lockWorkspaceIdeas(ctx, tx, workspaceID); err != nil {
		return err
	}
	var status string
	var contentCount int
	err := tx.QueryRowContext(ctx, `SELECT i.status, count(c.id) FROM ideas i JOIN idea_generation_batches b ON b.id = i.batch_id LEFT JOIN contents c ON c.source_idea_id = i.id AND c.workspace_id = b.workspace_id WHERE b.workspace_id = $1::uuid AND i.id = $2::uuid GROUP BY i.id`, workspaceID, ideaID).Scan(&status, &contentCount)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("decision Idea disappeared before queue update")
	}
	if err != nil {
		return errors.New("could not check Idea queue eligibility")
	}
	if status == "ACCEPTED" && contentCount == 0 {
		var maxPosition int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(eligible.production_queue_position), 0) FROM (SELECT i.id, i.production_queue_position FROM ideas i JOIN idea_generation_batches b ON b.id = i.batch_id LEFT JOIN contents c ON c.source_idea_id = i.id AND c.workspace_id = b.workspace_id WHERE b.workspace_id = $1::uuid AND i.status = 'ACCEPTED' GROUP BY i.id HAVING count(c.id) = 0) eligible WHERE eligible.id <> $2::uuid`, workspaceID, ideaID).Scan(&maxPosition); err != nil {
			// Aggregate filtering is expressed with a subquery below; this branch
			// is replaced by that query if the driver rejects the aggregate form.
			return errors.New("could not find the last production queue position")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ideas SET production_queue_position = $3 WHERE id = $1::uuid AND batch_id IN (SELECT id FROM idea_generation_batches WHERE workspace_id = $2::uuid)`, ideaID, workspaceID, maxPosition+1); err != nil {
			return errors.New("could not append Idea to production queue")
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ideas SET production_queue_position = NULL WHERE id = $1::uuid AND batch_id IN (SELECT id FROM idea_generation_batches WHERE workspace_id = $2::uuid)`, ideaID, workspaceID); err != nil {
		return errors.New("could not remove Idea from production queue")
	}
	return normalizeQueuePositions(ctx, tx, workspaceID)
}

type queueQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func listQueue(ctx context.Context, queryer queueQueryer, workspaceID string) ([]QueueItem, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT i.id::text, i.title, i.description, i.language, i.production_queue_position,
			a.id::text, a.status, a.error_category, a.created_at, a.failed_at
		FROM ideas i
		JOIN idea_generation_batches b ON b.id = i.batch_id
		LEFT JOIN contents c ON c.source_idea_id = i.id AND c.workspace_id = b.workspace_id
		LEFT JOIN LATERAL (
			SELECT ca.id, ca.status, ca.error_category, ca.created_at, ca.failed_at
			FROM content_generation_attempts ca
			WHERE ca.workspace_id = b.workspace_id AND ca.source_idea_id = i.id
			ORDER BY ca.created_at DESC, ca.id DESC LIMIT 1
		) a ON true
		WHERE b.workspace_id = $1::uuid AND i.status = 'ACCEPTED' AND c.id IS NULL
		ORDER BY i.production_queue_position ASC NULLS LAST, i.id ASC`, workspaceID)
	if err != nil {
		return nil, errors.New("could not load production queue")
	}
	defer rows.Close()
	items := []QueueItem{}
	for rows.Next() {
		var item QueueItem
		var position sql.NullInt32
		var attemptID, attemptStatus, attemptError sql.NullString
		var attemptCreatedAt sql.NullTime
		var attemptFailedAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.Title, &item.Description, &item.Language, &position, &attemptID, &attemptStatus, &attemptError, &attemptCreatedAt, &attemptFailedAt); err != nil {
			return nil, errors.New("could not read production queue")
		}
		if item.Language != "en" && item.Language != "fa" {
			return nil, errors.New("stored queue Idea language is invalid")
		}
		if !position.Valid || position.Int32 <= 0 {
			return nil, errors.New("stored queue Idea position is invalid")
		}
		item.ProductionQueuePosition = int(position.Int32)
		if attemptID.Valid {
			attempt := &QueueAttempt{ID: attemptID.String, Status: attemptStatus.String, CreatedAt: attemptCreatedAt.Time}
			if attemptError.Valid {
				attempt.ErrorCategory = &attemptError.String
			}
			if attemptFailedAt.Valid {
				value := attemptFailedAt.Time
				attempt.FailedAt = &value
			}
			if !validAttemptStatus(attempt.Status) || (attempt.ErrorCategory != nil && !validFailureCategory(*attempt.ErrorCategory)) {
				return nil, errors.New("stored queue attempt state is invalid")
			}
			item.LastAttempt = attempt
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("could not read production queue")
	}
	return items, nil
}

func lockWorkspace(ctx context.Context, tx *sql.Tx, workspaceID string) error {
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT id::text FROM workspaces WHERE id = $1::uuid FOR UPDATE`, workspaceID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workspace.ErrForbidden
		}
		return errors.New("could not lock workspace for queue mutation")
	}
	return nil
}

func lockWorkspaceIdeas(ctx context.Context, tx *sql.Tx, workspaceID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT i.id FROM ideas i JOIN idea_generation_batches b ON b.id = i.batch_id WHERE b.workspace_id = $1::uuid ORDER BY i.id FOR UPDATE OF i`, workspaceID)
	if err != nil {
		return errors.New("could not lock workspace Ideas for queue mutation")
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return errors.New("could not lock workspace Ideas for queue mutation")
		}
	}
	if err := rows.Err(); err != nil {
		return errors.New("could not lock workspace Ideas for queue mutation")
	}
	return nil
}

func LockWorkspaceIdeasForMutation(ctx context.Context, tx *sql.Tx, workspaceID string) error {
	return lockWorkspaceIdeas(ctx, tx, workspaceID)
}

func normalizeQueuePositions(ctx context.Context, tx *sql.Tx, workspaceID string) error {
	items, err := listQueue(ctx, tx, workspaceID)
	if err != nil {
		return err
	}
	for index, item := range items {
		position := index + 1
		if item.ProductionQueuePosition == position {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ideas SET production_queue_position = $3 WHERE id = $1::uuid AND batch_id IN (SELECT id FROM idea_generation_batches WHERE workspace_id = $2::uuid)`, item.ID, workspaceID, position); err != nil {
			return errors.New("could not normalize production queue positions")
		}
	}
	return nil
}

func sameIDs(items []QueueItem, submitted []string) bool {
	if len(items) != len(submitted) {
		return false
	}
	current := make(map[string]struct{}, len(items))
	for _, item := range items {
		current[item.ID] = struct{}{}
	}
	for _, id := range submitted {
		if _, exists := current[id]; !exists {
			return false
		}
	}
	return true
}

func validAttemptStatus(value string) bool {
	return value == "PENDING" || value == "RUNNING" || value == "COMPLETED" || value == "FAILED"
}
func validFailureCategory(value string) bool {
	return value == "TIMEOUT" || value == "RATE_LIMITED" || value == "PROVIDER_UNAVAILABLE" || value == "INVALID_OUTPUT" || value == "INTERRUPTED" || value == "UNKNOWN"
}
