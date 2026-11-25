package assets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
)

const privateReadCapabilityLifetime = 15 * time.Minute

type AssetReadCapabilityInput struct {
	WorkspaceID string
	AssetID     string
	Operation   string
}

type AssetReadCapability struct {
	URL                string    `json:"url"`
	ExpiresAt          time.Time `json:"expiresAt"`
	CacheControl       string    `json:"cacheControl"`
	ContentType        string    `json:"contentType"`
	ContentDisposition string    `json:"contentDisposition"`
	RangeAllowed       bool      `json:"rangeAllowed"`
}

// IssueReadCapability returns a short-lived private storage URL for a READY
// Asset after current Workspace membership and lifecycle checks.
func (s *Service) IssueReadCapability(ctx context.Context, subject workspace.MembershipSubject, input AssetReadCapabilityInput) (AssetReadCapability, error) {
	if _, authenticated := authenticatedUserID(subject); !authenticated {
		return AssetReadCapability{}, ErrUnauthenticated
	}
	workspaceID, err := workspace.ParseID(input.WorkspaceID)
	if err != nil {
		return AssetReadCapability{}, ErrAssetLibraryInvalid
	}
	assetID, err := uuid.Parse(input.AssetID)
	if err != nil || assetID == uuid.Nil || input.Operation != "PREVIEW" && input.Operation != "DOWNLOAD" {
		return AssetReadCapability{}, ErrAssetLibraryInvalid
	}
	if _, err := workspace.RequireMembership(ctx, s.database, subject, workspaceID); err != nil {
		return AssetReadCapability{}, err
	}
	var media, status, displayName string
	var permanentKey, detectedMIME, format sql.NullString
	err = s.database.QueryRowContext(ctx, `
		SELECT media_type, status, display_name, permanent_key, detected_mime_type, media_format
		FROM assets WHERE workspace_id = $1::uuid AND id = $2::uuid
	`, workspaceID.String(), assetID.String()).Scan(&media, &status, &displayName, &permanentKey, &detectedMIME, &format)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetReadCapability{}, ErrAssetNotFound
	}
	if err != nil {
		return AssetReadCapability{}, errors.New("could not read Asset media capability state")
	}
	if status != "READY" || !permanentKey.Valid || !detectedMIME.Valid || !format.Valid {
		return AssetReadCapability{}, ErrAssetConflict
	}
	key, err := parsePermanentKey(permanentKey.String)
	if err != nil {
		return AssetReadCapability{}, errors.New("Asset has invalid permanent storage key")
	}
	mediaType, err := parseMediaType(media)
	if err != nil {
		return AssetReadCapability{}, errors.New("Asset has invalid media type")
	}
	mediaFormat, ok := parseMediaFormat(format.String)
	if !ok || mediaFormat.mimeType() != detectedMIME.String {
		return AssetReadCapability{}, ErrAssetConflict
	}
	filename, err := safeDownloadFilename(displayName, mediaFormat)
	if err != nil {
		return AssetReadCapability{}, err
	}
	options := privateReadOptions{
		contentType:        detectedMIME.String,
		contentDisposition: "inline",
		rangeAllowed:       mediaType == mediaVideo || mediaType == mediaAudio,
	}
	if input.Operation == "DOWNLOAD" {
		options.contentDisposition = attachmentContentDisposition(filename)
	}
	expiresAt := s.clock().UTC().Add(privateReadCapabilityLifetime)
	capabilityURL, err := s.storage.createPrivateReadCapability(ctx, key, expiresAt, options)
	if err != nil {
		if errors.Is(err, errStorageNotFound) {
			return AssetReadCapability{}, ErrAssetConflict
		}
		return AssetReadCapability{}, ErrStorageUnavailable
	}
	return AssetReadCapability{URL: capabilityURL, ExpiresAt: expiresAt, CacheControl: "private, no-store", ContentType: options.contentType, ContentDisposition: options.contentDisposition, RangeAllowed: options.rangeAllowed}, nil
}

func parseMediaFormat(value string) (mediaFormat, bool) {
	for candidate := mediaJPEG; candidate <= mediaWAV; candidate++ {
		if candidate.String() == value {
			return candidate, true
		}
	}
	return mediaFormatInvalid, false
}

func safeDownloadFilename(displayName string, format mediaFormat) (string, error) {
	extension := map[mediaFormat]string{
		mediaJPEG: ".jpg", mediaPNG: ".png", mediaWebP: ".webp", mediaMP4: ".mp4",
		mediaMP3: ".mp3", mediaM4A: ".m4a", mediaWAV: ".wav",
	}[format]
	if extension == "" {
		return "", ErrAssetConflict
	}
	var safe strings.Builder
	for _, character := range displayName {
		if character < 0x20 || character >= 0x7f && character <= 0x9f || character == '"' {
			continue
		}
		safe.WriteRune(character)
	}
	filename := strings.TrimSpace(safe.String())
	if filename == "" {
		filename = "media"
	}
	if !strings.HasSuffix(strings.ToLower(filename), extension) {
		filename += extension
	}
	return filename, nil
}

func attachmentContentDisposition(filename string) string {
	var ascii strings.Builder
	for _, value := range filename {
		if value < 0x20 || value >= 0x7f || value == '"' || value == '\\' {
			ascii.WriteByte('_')
		} else {
			ascii.WriteByte(byte(value))
		}
	}
	fallback := ascii.String()
	if fallback == "" {
		fallback = "media"
	}
	return fmt.Sprintf("attachment; filename=\"%s\"; filename*=UTF-8''%s", fallback, encodeURIComponent(filename))
}

func encodeURIComponent(value string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for _, value := range []byte(value) {
		if value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || strings.ContainsRune("-_.!~*'()", rune(value)) {
			encoded.WriteByte(value)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hex[value>>4])
		encoded.WriteByte(hex[value&0x0f])
	}
	return encoded.String()
}
