package ideas

import (
	"context"
	"database/sql"
	"errors"
	"unicode/utf16"

	"bettercontent/backend/internal/content"
	"bettercontent/backend/internal/platform/textnorm"
	"bettercontent/backend/internal/workspace"
)

type DecisionInput struct {
	WorkspaceID     string  `json:"workspaceId"`
	IdeaID          string  `json:"ideaId"`
	NextState       string  `json:"nextState"`
	RejectionReason *string `json:"rejectionReason"`
}

type DecisionDTO struct {
	IdeaDTO
	IsNoop bool `json:"isNoop"`
}

func (s *Service) UpdateDecision(ctx context.Context, subject workspace.MembershipSubject, input DecisionInput) (DecisionDTO, error) {
	workspaceID, err := parseUUID(input.WorkspaceID)
	if err != nil {
		return DecisionDTO{}, ErrValidation
	}
	ideaID, err := parseUUID(input.IdeaID)
	if err != nil {
		return DecisionDTO{}, ErrValidation
	}
	if !validIdeaState(input.NextState) {
		return DecisionDTO{}, ErrValidation
	}
	workspaceValue := mustWorkspaceID(workspaceID)
	if _, err := workspace.RequireOwner(ctx, s.database, subject, workspaceValue); err != nil {
		return DecisionDTO{}, err
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return DecisionDTO{}, errors.New("could not begin Idea decision update")
	}
	defer tx.Rollback()
	if err := lockDecisionWorkspace(ctx, tx, workspaceID); err != nil {
		return DecisionDTO{}, err
	}
	if _, err := workspace.RequireOwner(ctx, tx, subject, workspaceValue); err != nil {
		return DecisionDTO{}, err
	}
	if err := content.LockWorkspaceIdeasForMutation(ctx, tx, workspaceID); err != nil {
		return DecisionDTO{}, err
	}
	current, err := loadIdeaForDecision(ctx, tx, workspaceID, ideaID)
	if err != nil {
		return DecisionDTO{}, err
	}
	update, err := calculateDecisionUpdate(current.Status, current.RejectionReason, input.NextState, input.RejectionReason)
	if err != nil {
		return DecisionDTO{}, err
	}
	noop := update.noop
	if !noop {
		now := s.clock.Now().UTC()
		if current.Status == update.status {
			_, err = tx.ExecContext(ctx, `UPDATE ideas SET rejection_reason = $3, updated_at = $4 WHERE id = $1::uuid AND batch_id IN (SELECT id FROM idea_generation_batches WHERE workspace_id = $2::uuid)`, ideaID, workspaceID, optionalValue(update.rejectionReason), now)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE ideas SET status = $3, rejection_reason = $4, status_changed_at = $5, updated_at = $5 WHERE id = $1::uuid AND batch_id IN (SELECT id FROM idea_generation_batches WHERE workspace_id = $2::uuid)`, ideaID, workspaceID, update.status, optionalValue(update.rejectionReason), now)
		}
		if err != nil {
			return DecisionDTO{}, errors.New("could not update Idea decision")
		}
		if current.Status != update.status {
			if err := content.ReconcileIdeaQueueAfterDecision(ctx, tx, workspaceID, ideaID); err != nil {
				return DecisionDTO{}, err
			}
		}
	}
	updated, err := loadIdeaForDecision(ctx, tx, workspaceID, ideaID)
	if err != nil {
		return DecisionDTO{}, err
	}
	if err := tx.Commit(); err != nil {
		return DecisionDTO{}, errors.New("could not commit Idea decision update")
	}
	return DecisionDTO{IdeaDTO: updated, IsNoop: noop}, nil
}

func lockDecisionWorkspace(ctx context.Context, tx *sql.Tx, workspaceID string) error {
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT id::text FROM workspaces WHERE id = $1::uuid FOR UPDATE`, workspaceID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workspace.ErrForbidden
		}
		return errors.New("could not lock workspace for Idea decision")
	}
	return nil
}

func loadIdeaForDecision(ctx context.Context, tx *sql.Tx, workspaceID, ideaID string) (IdeaDTO, error) {
	row := tx.QueryRowContext(ctx, `SELECT i.id::text, i.batch_id::text, i.position, i.title, i.description, i.category, i.language, i.status, i.rejection_reason, i.status_changed_at, i.created_at, i.updated_at, i.production_queue_position FROM ideas i JOIN idea_generation_batches b ON b.id = i.batch_id WHERE b.workspace_id = $1::uuid AND i.id = $2::uuid FOR UPDATE OF i, b`, workspaceID, ideaID)
	idea, err := scanIdea(row)
	if errors.Is(err, sql.ErrNoRows) {
		return IdeaDTO{}, ErrNotFound
	}
	if err != nil {
		return IdeaDTO{}, errors.New("could not load Idea decision")
	}
	return idea, nil
}

func normalizeRejectionReason(input *string) (*string, error) {
	if input == nil {
		return nil, nil
	}
	value := textnorm.TrimECMAScript(*input)
	if value == "" {
		return nil, nil
	}
	if len(utf16.Encode([]rune(value))) > 500 {
		return nil, ErrValidation
	}
	return &value, nil
}

type decisionChange struct {
	status          string
	rejectionReason *string
	noop            bool
}

func calculateDecisionUpdate(currentStatus string, currentReason *string, nextStatus string, rawReason *string) (decisionChange, error) {
	if !validIdeaState(currentStatus) || !validIdeaState(nextStatus) {
		return decisionChange{}, ErrValidation
	}
	reason, err := normalizeRejectionReason(rawReason)
	if err != nil {
		return decisionChange{}, err
	}
	if nextStatus != "REJECTED" {
		reason = nil
	}
	return decisionChange{
		status:          nextStatus,
		rejectionReason: reason,
		noop:            currentStatus == nextStatus && equalOptionalString(currentReason, reason),
	}, nil
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func optionalValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
