package content

import (
	"context"
	"database/sql"
	"errors"

	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

func (s *Service) Retry(ctx context.Context, subject workspace.MembershipSubject, workspaceValue, attemptValue string) (ContentGenerationResult, error) {
	workspaceID, err := workspace.ParseID(workspaceValue)
	if err != nil {
		return ContentGenerationResult{}, ErrGenerationValidation
	}
	attemptID, err := parseContentUUID(attemptValue)
	if err != nil {
		return ContentGenerationResult{}, ErrGenerationValidation
	}
	if _, err := workspace.RequireOwner(ctx, s.database, subject, workspaceID); err != nil {
		return ContentGenerationResult{}, err
	}
	var input ContentGenerationInput
	var instructions sql.NullString
	var status string
	var currentVersionID sql.NullString
	err = s.database.QueryRowContext(ctx, `
		SELECT a.source_idea_id::text, a.requested_language, a.format, a.instructions, a.status, d.current_version_id::text
		FROM content_generation_attempts a
		JOIN ideas i ON i.id = a.source_idea_id
		JOIN idea_generation_batches b ON b.id = i.batch_id AND b.workspace_id = a.workspace_id
		LEFT JOIN content_dna d ON d.workspace_id = a.workspace_id
		WHERE a.workspace_id = $1::uuid AND a.id = $2::uuid`, workspaceID.String(), attemptID).Scan(
		&input.SourceIdeaID, &input.RequestedLanguage, &input.Format, &instructions, &status, &currentVersionID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ContentGenerationResult{}, ErrGenerationNotFound
	}
	if err != nil {
		return ContentGenerationResult{}, errors.New("could not load Content generation Attempt for retry")
	}
	if status != "FAILED" {
		return ContentGenerationResult{}, ErrGenerationConflict
	}
	if !currentVersionID.Valid {
		return ContentGenerationResult{}, ErrGenerationValidation
	}
	input.WorkspaceID = workspaceID.String()
	input.BaseContentDNAVersionID = currentVersionID.String
	input.IdempotencyKey = uuid.NewString()
	if instructions.Valid {
		input.Instructions = &instructions.String
	}
	return s.Generate(ctx, subject, input)
}
