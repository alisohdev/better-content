package assets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

func TestGetAssetLibraryAppliesWorkspaceFiltersSearchEscapingAndPagination(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	service := newService(database, nil, time.Now, nil)
	ids := make([]string, 0, assetLibraryPageSize+1)
	for index := 0; index < assetLibraryPageSize+1; index++ {
		mediaType, status := "IMAGE", "READY"
		if index%2 == 1 {
			mediaType = "VIDEO"
		}
		if index == 0 {
			status = "FAILED"
		}
		id := insertAttachmentFixture(t, database, workspaceID, userID, mediaType, status)
		ids = append(ids, id)
		name := fmt.Sprintf("Campaign item %02d", index)
		if index == 0 {
			name = `Campaign 100%_Ready \`
		}
		createdAt := time.Date(2026, 10, 25, 10, index, 0, 0, time.UTC)
		if _, err := database.Exec(`UPDATE assets SET display_name = $2, created_at = $3 WHERE id = $1::uuid`, id, name, createdAt); err != nil {
			t.Fatal(err)
		}
	}

	first, err := service.GetAssetLibrary(context.Background(), assetSubject(userID), AssetLibraryInput{WorkspaceID: workspaceID})
	if err != nil {
		t.Fatalf("GetAssetLibrary: %v", err)
	}
	if first.Page != 1 || first.PageSize != assetLibraryPageSize || !first.HasNextPage || len(first.Assets) != assetLibraryPageSize {
		t.Fatalf("first page metadata = %+v", first)
	}
	if first.Assets[0].ID != ids[len(ids)-1] || first.Assets[0].ReferenceCount != 0 {
		t.Fatalf("latest Asset or reference projection = %+v", first.Assets[0])
	}
	second, err := service.GetAssetLibrary(context.Background(), assetSubject(userID), AssetLibraryInput{WorkspaceID: workspaceID, Page: 2})
	if err != nil || second.HasNextPage || len(second.Assets) != 1 || second.Assets[0].ID != ids[0] {
		t.Fatalf("second page = %+v, err=%v", second, err)
	}

	imageType := "IMAGE"
	readyStatus := "READY"
	filtered, err := service.GetAssetLibrary(context.Background(), assetSubject(userID), AssetLibraryInput{WorkspaceID: workspaceID, MediaType: &imageType, Status: &readyStatus})
	if err != nil || len(filtered.Assets) != 12 {
		t.Fatalf("media/status filters returned %d records, err=%v", len(filtered.Assets), err)
	}
	escaped, err := service.GetAssetLibrary(context.Background(), assetSubject(userID), AssetLibraryInput{WorkspaceID: workspaceID, Search: "%_Ready \\"})
	if err != nil || len(escaped.Assets) != 1 || escaped.Assets[0].ID != ids[0] {
		t.Fatalf("escaped search returned %+v, err=%v", escaped.Assets, err)
	}
	if _, err := service.GetAssetLibrary(context.Background(), assetSubject(uuid.NewString()), AssetLibraryInput{WorkspaceID: workspaceID}); !errors.Is(err, workspace.ErrForbidden) {
		t.Fatalf("cross-Workspace library error = %v, want forbidden", err)
	}
}

func TestRenameAssetRequiresOwnerAndRejectsDeletingAssets(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	assetID := insertAttachmentFixture(t, database, workspaceID, userID, "IMAGE", "READY")
	updatedAt := time.Date(2026, 10, 25, 18, 0, 0, 0, time.UTC)
	service := newService(database, nil, func() time.Time { return updatedAt }, nil)
	result, err := service.RenameAsset(context.Background(), assetSubject(userID), AssetRenameInput{
		WorkspaceID: workspaceID, AssetID: assetID, DisplayName: "  Persian تصویر name  ",
	})
	if err != nil {
		t.Fatalf("RenameAsset: %v", err)
	}
	if result.DisplayName != "Persian تصویر name" || !result.UpdatedAt.Equal(updatedAt) || result.ReferenceCount != 0 {
		t.Fatalf("renamed Asset = %+v", result)
	}
	if _, err := service.RenameAsset(context.Background(), assetSubject(uuid.NewString()), AssetRenameInput{
		WorkspaceID: workspaceID, AssetID: assetID, DisplayName: "Forbidden",
	}); !errors.Is(err, workspace.ErrForbidden) {
		t.Fatalf("non-owner rename error = %v, want forbidden", err)
	}
	if _, err := service.RenameAsset(context.Background(), assetSubject(userID), AssetRenameInput{
		WorkspaceID: workspaceID, AssetID: assetID, DisplayName: "unsafe\u202Ename",
	}); !errors.Is(err, ErrAssetLibraryInvalid) {
		t.Fatalf("unsafe rename error = %v, want invalid request", err)
	}
	if _, err := database.Exec(`UPDATE assets SET status = 'DELETING' WHERE id = $1::uuid`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RenameAsset(context.Background(), assetSubject(userID), AssetRenameInput{
		WorkspaceID: workspaceID, AssetID: assetID, DisplayName: "No longer mutable",
	}); !errors.Is(err, ErrAssetConflict) {
		t.Fatalf("deleting Asset rename error = %v, want conflict", err)
	}
}

func TestDeleteAssetMarksForCleanupOnceAndRejectsProcessingAssets(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	assetID := insertAttachmentFixture(t, database, workspaceID, userID, "IMAGE", "READY")
	now := time.Date(2026, 10, 25, 19, 0, 0, 0, time.UTC)
	service := newService(database, nil, func() time.Time { return now }, nil)
	input := DeleteAssetInput{WorkspaceID: workspaceID, AssetID: assetID}
	result, err := service.DeleteAsset(context.Background(), assetSubject(userID), input)
	if err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	if result.AssetID != assetID || result.Status != "DELETING" || !result.CleanupWorkflowEstablished {
		t.Fatalf("DeleteAsset result = %+v", result)
	}
	var status string
	var deletingFromReady int
	var updatedAt time.Time
	if err := database.QueryRow(`SELECT status, deleting_from_ready, updated_at FROM assets WHERE id = $1::uuid`, assetID).Scan(&status, &deletingFromReady, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if status != "DELETING" || deletingFromReady != 1 || !updatedAt.Equal(now) {
		t.Fatalf("persisted deletion transition = %s %d %v", status, deletingFromReady, updatedAt)
	}
	for range 2 {
		if _, err := service.DeleteAsset(context.Background(), assetSubject(userID), input); err != nil {
			t.Fatalf("idempotent DeleteAsset: %v", err)
		}
	}
	var jobCount int
	if err := database.QueryRow(`SELECT count(*) FROM asset_jobs WHERE dedupe_key = $1`, "DELETE_ASSET:"+assetID).Scan(&jobCount); err != nil || jobCount != 1 {
		t.Fatalf("delete workflow count = %d, err=%v", jobCount, err)
	}
	if _, err := service.DeleteAsset(context.Background(), assetSubject(uuid.NewString()), input); !errors.Is(err, workspace.ErrForbidden) {
		t.Fatalf("non-owner Asset deletion error = %v, want forbidden", err)
	}

	processingID := insertAttachmentFixture(t, database, workspaceID, userID, "IMAGE", "READY")
	if _, err := database.Exec(`UPDATE assets SET status = 'PROCESSING' WHERE id = $1::uuid`, processingID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteAsset(context.Background(), assetSubject(userID), DeleteAssetInput{WorkspaceID: workspaceID, AssetID: processingID}); !errors.Is(err, ErrAssetConflict) {
		t.Fatalf("processing Asset deletion error = %v, want conflict", err)
	}
}

func TestDeleteAssetChecksCanonicalDraftAndVersionReferences(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	assetID := insertAttachmentFixture(t, database, workspaceID, userID, "IMAGE", "READY")
	insertAssetContentReferenceFixture(t, database, userID, workspaceID, assetID)
	t.Cleanup(func() {
		tx, err := database.Begin()
		if err != nil {
			t.Errorf("begin Asset Content fixture cleanup: %v", err)
			return
		}
		for _, statement := range []string{
			`DELETE FROM content_drafts WHERE content_id IN (SELECT id FROM contents WHERE workspace_id = $1::uuid)`,
			`DELETE FROM content_versions WHERE content_id IN (SELECT id FROM contents WHERE workspace_id = $1::uuid)`,
			`DELETE FROM contents WHERE workspace_id = $1::uuid`,
			`DELETE FROM content_generation_attempts WHERE workspace_id = $1::uuid`,
			`DELETE FROM ideas WHERE batch_id IN (SELECT id FROM idea_generation_batches WHERE workspace_id = $1::uuid)`,
			`DELETE FROM idea_generation_batches WHERE workspace_id = $1::uuid`,
			`DELETE FROM ai_runs WHERE workspace_id = $1::uuid`,
			`DELETE FROM content_dna_versions WHERE content_dna_id IN (SELECT id FROM content_dna WHERE workspace_id = $1::uuid)`,
			`DELETE FROM content_dna WHERE workspace_id = $1::uuid`,
		} {
			if _, err := tx.Exec(statement, workspaceID); err != nil {
				_ = tx.Rollback()
				t.Errorf("clean up Asset Content fixture: %v", err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			t.Errorf("commit Asset Content fixture cleanup: %v", err)
		}
	})
	service := newService(database, nil, time.Now, nil)
	_, err := service.DeleteAsset(context.Background(), assetSubject(userID), DeleteAssetInput{WorkspaceID: workspaceID, AssetID: assetID})
	if !errors.Is(err, ErrAssetInUse) {
		t.Fatalf("DeleteAsset with canonical reference error = %v, want Asset in use", err)
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM assets WHERE id = $1::uuid`, assetID).Scan(&status); err != nil || status != "READY" {
		t.Fatalf("referenced Asset status = %q, err=%v", status, err)
	}
	var deleteJobs int
	if err := database.QueryRow(`SELECT count(*) FROM asset_jobs WHERE dedupe_key = $1`, "DELETE_ASSET:"+assetID).Scan(&deleteJobs); err != nil || deleteJobs != 0 {
		t.Fatalf("referenced Asset delete job count = %d, err=%v", deleteJobs, err)
	}
	if _, err := database.Exec(`DELETE FROM asset_references WHERE asset_id = $1::uuid`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE content_drafts SET document = '{}'::jsonb WHERE content_id IN (SELECT id FROM contents WHERE workspace_id = $1::uuid)`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteAsset(context.Background(), assetSubject(userID), DeleteAssetInput{WorkspaceID: workspaceID, AssetID: assetID}); err == nil {
		t.Fatal("malformed canonical document was treated as proof that the Asset is unreferenced")
	}
	if err := database.QueryRow(`SELECT status FROM assets WHERE id = $1::uuid`, assetID).Scan(&status); err != nil || status != "READY" {
		t.Fatalf("malformed canonical document changed Asset status to %q, err=%v", status, err)
	}
}

func TestDeleteAssetSerializesWithContentAttachmentReferenceWrite(t *testing.T) {
	database := openAssetTestDatabase(t)
	userID, workspaceID := createAssetWorkspace(t, database)
	assetID := insertAttachmentFixture(t, database, workspaceID, userID, "IMAGE", "READY")
	versionAssetID := insertAttachmentFixture(t, database, workspaceID, userID, "IMAGE", "READY")
	insertAssetContentReferenceFixture(t, database, userID, workspaceID, versionAssetID)
	t.Cleanup(func() {
		tx, err := database.Begin()
		if err != nil {
			t.Errorf("begin Asset race fixture cleanup: %v", err)
			return
		}
		for _, statement := range []string{
			`DELETE FROM asset_references WHERE workspace_id = $1::uuid`,
			`DELETE FROM content_drafts WHERE content_id IN (SELECT id FROM contents WHERE workspace_id = $1::uuid)`,
			`DELETE FROM content_versions WHERE content_id IN (SELECT id FROM contents WHERE workspace_id = $1::uuid)`,
			`DELETE FROM contents WHERE workspace_id = $1::uuid`,
			`DELETE FROM content_generation_attempts WHERE workspace_id = $1::uuid`,
			`DELETE FROM ideas WHERE batch_id IN (SELECT id FROM idea_generation_batches WHERE workspace_id = $1::uuid)`,
			`DELETE FROM idea_generation_batches WHERE workspace_id = $1::uuid`,
			`DELETE FROM ai_runs WHERE workspace_id = $1::uuid`,
			`DELETE FROM content_dna_versions WHERE content_dna_id IN (SELECT id FROM content_dna WHERE workspace_id = $1::uuid)`,
			`DELETE FROM content_dna WHERE workspace_id = $1::uuid`,
		} {
			if _, err := tx.Exec(statement, workspaceID); err != nil {
				_ = tx.Rollback()
				t.Errorf("clean up Asset race fixture: %v", err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			t.Errorf("commit Asset race fixture cleanup: %v", err)
		}
	})
	attached := attachmentDocument(t, assetID, "BROLL_CUE")
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var lockedWorkspace string
	if err := tx.QueryRow(`SELECT id::text FROM workspaces WHERE id = $1::uuid FOR UPDATE`, workspaceID).Scan(&lockedWorkspace); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := ValidateAttachments(context.Background(), tx, workspaceID, attached); err != nil {
		_ = tx.Rollback()
		t.Fatalf("attachment validation: %v", err)
	}
	reference := attached.AssetReferences()[0]
	var contentID string
	if err := tx.QueryRow(`SELECT id::text FROM contents WHERE workspace_id = $1::uuid LIMIT 1`, workspaceID).Scan(&contentID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE content_drafts SET document = $2::jsonb WHERE content_id = $1::uuid`, contentID, attached.JSON()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM asset_references WHERE content_id = $1::uuid AND artifact_kind = 'DRAFT'`, contentID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO asset_references (workspace_id, asset_id, content_id, artifact_kind, direction_id) VALUES ($1::uuid, $2::uuid, $3::uuid, 'DRAFT', $4::uuid)`, workspaceID, reference.AssetID, contentID, reference.DirectionID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	service := newService(database, nil, time.Now, nil)
	started := make(chan struct{})
	deleteDone := make(chan error, 1)
	go func() {
		close(started)
		_, deleteErr := service.DeleteAsset(context.Background(), assetSubject(userID), DeleteAssetInput{WorkspaceID: workspaceID, AssetID: assetID})
		deleteDone <- deleteErr
	}()
	<-started
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-deleteDone; !errors.Is(err, ErrAssetInUse) {
		t.Fatalf("racing Asset deletion = %v, want ErrAssetInUse", err)
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM assets WHERE id = $1::uuid`, assetID).Scan(&status); err != nil || status != "READY" {
		t.Fatalf("racing deletion left Asset status=%q err=%v", status, err)
	}
}

func insertAssetContentReferenceFixture(t *testing.T, database *sql.DB, userID, workspaceID, assetID string) {
	t.Helper()
	ids := make([]string, 8)
	for index := range ids {
		ids[index] = uuid.NewString()
	}
	contentDNAID, contentDNAVersionID, ideaRunID, ideaBatchID := ids[0], ids[1], ids[2], ids[3]
	ideaID, contentID, attemptID, contentRunID := ids[4], ids[5], ids[6], ids[7]
	ctx := context.Background()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	queries := []struct {
		statement string
		args      []any
	}{
		{`INSERT INTO content_dna (id, workspace_id, current_version_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`, []any{contentDNAID, workspaceID, contentDNAVersionID}},
		{`INSERT INTO content_dna_versions (id, content_dna_id, version_number, payload, created_by_user_id) VALUES ($1::uuid, $2::uuid, 1, $3::jsonb, $4)`, []any{contentDNAVersionID, contentDNAID, `{"schemaVersion":1,"identity":{},"language":{"defaultContentLanguage":"en","contentLanguages":["en"]}}`, userID}},
		{`INSERT INTO ai_runs (id, workspace_id, kind, provider, model, prompt_version, generation_settings, status, output_snapshot, usage, started_at, completed_at) VALUES ($1::uuid, $2::uuid, 'IDEA_GENERATION', 'avalai', 'gpt-5.6-luna', 'idea-generation/v1', '{}'::jsonb, 'COMPLETED', '{"schemaVersion":1,"ideas":[]}'::jsonb, '{"inputTokens":1,"outputTokens":1,"totalTokens":2}'::jsonb, NOW(), NOW())`, []any{ideaRunID, workspaceID}},
		{`INSERT INTO idea_generation_batches (id, workspace_id, content_dna_version_id, ai_run_id, idempotency_key, request_fingerprint, requested_language, requested_count, status, started_at, completed_at) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 'en', 20, 'COMPLETED', NOW(), NOW())`, []any{ideaBatchID, workspaceID, contentDNAVersionID, ideaRunID, uuid.NewString(), "a" + strings.Repeat("0", 63)}},
		{`INSERT INTO ideas (id, batch_id, position, title, description, category, language, status) VALUES ($1::uuid, $2::uuid, 1, 'Asset reference fixture', 'Reference safety fixture', 'TEST', 'en', 'ACCEPTED')`, []any{ideaID, ideaBatchID}},
	}
	for _, query := range queries {
		if _, err := tx.ExecContext(ctx, query.statement, query.args...); err != nil {
			t.Fatalf("insert Asset Content reference fixture: %v", err)
		}
	}
	documentJSON := fmt.Sprintf(`{"schemaVersion":4,"script":{"blocks":[{"id":"%s","type":"paragraph","text":"Reference fixture","performanceDirections":[],"editDirections":[{"id":"%s","type":"BROLL_CUE","description":"A street","searchQuery":"city street","assetId":"%s"}]}]}}`, uuid.NewString(), uuid.NewString(), assetID)
	contentRunSQL := `INSERT INTO ai_runs (id, workspace_id, kind, provider, model, prompt_version, generation_settings, status, output_snapshot, usage, started_at, completed_at) VALUES ($1::uuid, $2::uuid, 'CONTENT_SCRIPT_GENERATION', 'avalai', 'gpt-5.6-luna', 'content-script-generation/v1', '{}'::jsonb, 'COMPLETED', $3::jsonb, '{"inputTokens":1,"outputTokens":1,"totalTokens":2}'::jsonb, NOW(), NOW())`
	if _, err := tx.ExecContext(ctx, contentRunSQL, contentRunID, workspaceID, documentJSON); err != nil {
		t.Fatalf("insert Content AI Run fixture: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO content_generation_attempts (id, workspace_id, source_idea_id, content_dna_version_id, requested_language, format, instructions, idempotency_key, request_fingerprint, ai_run_id, status, started_at, completed_at) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'en', 'SHORT_VIDEO', NULL, $5::uuid, $6, $7::uuid, 'COMPLETED', NOW(), NOW())`, attemptID, workspaceID, ideaID, contentDNAVersionID, uuid.NewString(), "b"+strings.Repeat("0", 63), contentRunID); err != nil {
		t.Fatalf("insert Content generation fixture: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO contents (id, workspace_id, source_idea_id, content_language, format, source_generation_attempt_id) VALUES ($1::uuid, $2::uuid, $3::uuid, 'en', 'SHORT_VIDEO', $4::uuid)`, contentID, workspaceID, ideaID, attemptID); err != nil {
		t.Fatalf("insert Content fixture: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO content_drafts (content_id, document, revision) VALUES ($1::uuid, $2::jsonb, 1)`,
		`INSERT INTO content_versions (content_id, version_number, document, source, ai_run_id, created_by_user_id) VALUES ($1::uuid, 1, $2::jsonb, 'AI_GENERATED', $3::uuid, $4)`,
	} {
		args := []any{contentID, documentJSON}
		if strings.Contains(statement, "content_versions") {
			args = append(args, contentRunID, userID)
		}
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			t.Fatalf("insert canonical Content Asset reference: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit Asset Content reference fixture: %v", err)
	}
}
