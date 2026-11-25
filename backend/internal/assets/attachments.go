package assets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"bettercontent/backend/internal/content/document"
)

var ErrAttachmentInvalid = errors.New("Content Asset attachment is invalid")

// ValidateAttachments locks the referenced Asset rows in stable UUID order and
// verifies they are READY and compatible with their owning edit direction.
// The caller owns the transaction and remains responsible for reference writes.
func ValidateAttachments(ctx context.Context, tx *sql.Tx, workspaceID string, doc document.Document) error {
	references := doc.AssetReferences()
	if len(references) == 0 {
		return nil
	}
	assetIDs := make([]string, 0, len(references))
	for _, reference := range references {
		assetIDs = append(assetIDs, reference.AssetID)
	}
	sort.Strings(assetIDs)
	assetIDs = compactStrings(assetIDs)
	args := make([]any, 0, len(assetIDs)+1)
	args = append(args, workspaceID)
	placeholders := make([]string, 0, len(assetIDs))
	for index, assetID := range assetIDs {
		args = append(args, assetID)
		placeholders = append(placeholders, fmt.Sprintf("$%d::uuid", index+2))
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, media_type, status
		FROM assets
		WHERE workspace_id = $1::uuid AND id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY id
		FOR UPDATE
	`, args...)
	if err != nil {
		return errors.New("could not lock referenced Assets")
	}
	type assetState struct{ mediaType, status string }
	lockedAssets := make(map[string]assetState, len(assetIDs))
	for rows.Next() {
		var id string
		var state assetState
		if err := rows.Scan(&id, &state.mediaType, &state.status); err != nil {
			_ = rows.Close()
			return errors.New("could not read referenced Assets")
		}
		lockedAssets[id] = state
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return errors.New("could not read referenced Assets")
	}
	if err := rows.Close(); err != nil {
		return errors.New("could not close referenced Asset results")
	}
	if len(lockedAssets) != len(assetIDs) {
		return ErrAttachmentInvalid
	}
	for _, reference := range references {
		asset, exists := lockedAssets[reference.AssetID]
		compatible := reference.DirectionType == "BROLL_CUE" && (asset.mediaType == "IMAGE" || asset.mediaType == "VIDEO") ||
			reference.DirectionType == "SOUND_CUE" && asset.mediaType == "AUDIO"
		if !exists || asset.status != "READY" || !compatible {
			return ErrAttachmentInvalid
		}
	}
	return nil
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	write := 1
	for read := 1; read < len(values); read++ {
		if values[read] == values[write-1] {
			continue
		}
		values[write] = values[read]
		write++
	}
	return values[:write]
}
