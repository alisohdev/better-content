package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bettercontent/backend/internal/ai"
	"bettercontent/backend/internal/assets"
	"bettercontent/backend/internal/content"
	"bettercontent/backend/internal/content/document"
	"bettercontent/backend/internal/dna"
	"bettercontent/backend/internal/ideas"
	"bettercontent/backend/internal/identity"
	"bettercontent/backend/internal/platform/clock"
	"bettercontent/backend/internal/platform/httpserver"
	"bettercontent/backend/internal/platform/postgres"
	"bettercontent/backend/internal/transport"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

type ideaAPITestSubject string

func (s ideaAPITestSubject) AuthenticatedUserID() (string, bool) { return string(s), s != "" }

type ideaAPIProvider struct{}

func (ideaAPIProvider) GenerateIdeas(context.Context, ai.Request) ai.Result {
	output := &ai.Output{SchemaVersion: 1, Ideas: make([]ai.Idea, ai.IdeaGenerationCount)}
	for index := range output.Ideas {
		output.Ideas[index] = ai.Idea{Title: "HTTP idea " + string(rune('A'+index)), Description: "A useful HTTP generated idea."}
	}
	return ai.Result{Output: output}
}

type contentAPIProvider struct{ output *document.V4 }

func (p contentAPIProvider) GenerateContent(context.Context, ai.ContentRequest) ai.ContentResult {
	return ai.ContentResult{Output: p.output}
}

func TestContentDNARoutesAuthenticateAndPreserveSnapshotContract(t *testing.T) {
	database := openHTTPAPITestDatabase(t)
	secret := strings.Repeat("h", 32)
	identityService := identity.NewService(database, "http://localhost:3000", secret, clock.System{}, false)
	server := httpserver.NewHandler(database, New(identityService, dna.NewService(database), ideas.NewService(database, nil, clock.System{}, nil), content.NewService(database, nil, clock.System{}, nil), content.NewQueueService(database)))
	userID := ""
	email := uuid.NewString() + "@example.com"
	workspaceID := ""
	t.Cleanup(func() {
		if workspaceID != "" {
			_, _ = database.ExecContext(context.Background(), `DELETE FROM content_dna_versions WHERE content_dna_id IN (SELECT id FROM content_dna WHERE workspace_id = $1::uuid)`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM content_dna WHERE workspace_id = $1::uuid`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM workspace_members WHERE workspace_id = $1::uuid`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM workspaces WHERE id = $1::uuid`, workspaceID)
		}
		_, _ = database.ExecContext(context.Background(), `DELETE FROM "user" WHERE email = $1`, email)
	})
	signUpBody, _ := json.Marshal(transport.SignUpRequest{Name: "DNA API user", Email: openapi_types.Email(email), Password: "secure password 123"})
	signUpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sign-up", bytes.NewReader(signUpBody))
	signUpRequest.Header.Set("Content-Type", "application/json")
	signUpRequest.Header.Set("Origin", "http://localhost:3000")
	signUpResponse := httptest.NewRecorder()
	server.ServeHTTP(signUpResponse, signUpRequest)
	if signUpResponse.Code != http.StatusOK {
		t.Fatalf("sign-up status/body = %d %s", signUpResponse.Code, signUpResponse.Body.String())
	}
	cookies := signUpResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("sign-up cookies = %d", len(cookies))
	}
	var session transport.SessionResponse
	if err := json.Unmarshal(signUpResponse.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	userID = session.User.Id
	if err := database.QueryRowContext(context.Background(), `SELECT workspace_id::text FROM workspace_members WHERE user_id = $1`, userID).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}

	noSession := httptest.NewRecorder()
	server.ServeHTTP(noSession, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+workspaceID+"/content-dna", nil))
	if noSession.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated DNA read status = %d", noSession.Code)
	}
	protectedRoutes := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/workspaces/" + workspaceID + "/ideas/generations"},
		{http.MethodGet, "/api/v1/workspaces/" + workspaceID + "/ideas/generations"},
		{http.MethodGet, "/api/v1/workspaces/" + workspaceID + "/ideas"},
		{http.MethodPatch, "/api/v1/workspaces/" + workspaceID + "/ideas/" + uuid.NewString() + "/decision"},
		{http.MethodGet, "/api/v1/workspaces/" + workspaceID + "/production-queue"},
		{http.MethodPut, "/api/v1/workspaces/" + workspaceID + "/production-queue"},
		{http.MethodPost, "/api/v1/workspaces/" + workspaceID + "/contents/generations"},
		{http.MethodPost, "/api/v1/workspaces/" + workspaceID + "/content-generation-attempts/" + uuid.NewString() + "/retry"},
		{http.MethodGet, "/api/v1/workspaces/" + workspaceID + "/content-generation-attempts/" + uuid.NewString()},
		{http.MethodGet, "/api/v1/workspaces/" + workspaceID + "/content-generation-attempts/" + uuid.NewString() + "/result"},
		{http.MethodGet, "/api/v1/workspaces/" + workspaceID + "/content-ideas/" + uuid.NewString() + "/content-history"},
		{http.MethodGet, "/api/v1/workspaces/" + workspaceID + "/content-ideas/" + uuid.NewString()},
		{http.MethodGet, "/api/v1/workspaces/" + workspaceID + "/content-ideas/" + uuid.NewString() + "/content-usage"},
		{http.MethodGet, "/api/v1/workspaces/" + workspaceID + "/contents"},
		{http.MethodGet, "/api/v1/workspaces/" + workspaceID + "/contents/" + uuid.NewString()},
		{http.MethodPut, "/api/v1/workspaces/" + workspaceID + "/contents/" + uuid.NewString() + "/draft"},
		{http.MethodPost, "/api/v1/workspaces/" + workspaceID + "/contents/" + uuid.NewString() + "/acceptance"},
		{http.MethodGet, "/api/v1/workspaces/" + workspaceID + "/contents/" + uuid.NewString() + "/teleprompter"},
		{http.MethodGet, "/api/v1/workspaces/" + workspaceID + "/contents/" + uuid.NewString() + "/edit-guide"},
	}
	for _, route := range protectedRoutes {
		request := httptest.NewRequest(route.method, route.path, nil)
		request.Header.Set("Origin", "http://localhost:3000")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %s status = %d, want 401; body=%s", route.method, route.path, response.Code, response.Body.String())
		}
	}

	payload := `{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"Private DNA"}}`
	requestBody := `{"baseVersionId":null,"payload":` + payload + `}`
	save := httptest.NewRequest(http.MethodPut, "/api/v1/workspaces/"+workspaceID+"/content-dna", strings.NewReader(requestBody))
	save.Header.Set("Content-Type", "application/json")
	save.Header.Set("Origin", "http://localhost:3000")
	save.AddCookie(cookies[0])
	saved := httptest.NewRecorder()
	server.ServeHTTP(saved, save)
	if saved.Code != http.StatusOK || !bytes.Contains(saved.Body.Bytes(), []byte(`"versionNumber":1`)) {
		t.Fatalf("DNA save response = %d %s", saved.Code, saved.Body.String())
	}
	var firstVersion transport.ContentDnaVersion
	if err := json.Unmarshal(saved.Body.Bytes(), &firstVersion); err != nil {
		t.Fatal(err)
	}

	currentRequest := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+workspaceID+"/content-dna", nil)
	currentRequest.AddCookie(cookies[0])
	current := httptest.NewRecorder()
	server.ServeHTTP(current, currentRequest)
	if current.Code != http.StatusOK || !bytes.Contains(current.Body.Bytes(), []byte(`"status":"INCOMPLETE"`)) {
		t.Fatalf("current DNA response = %d %s", current.Code, current.Body.String())
	}

	updateBody := `{"baseVersionId":"` + firstVersion.Id.String() + `","payload":{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"New private DNA"}}}`
	update := httptest.NewRequest(http.MethodPut, "/api/v1/workspaces/"+workspaceID+"/content-dna", strings.NewReader(updateBody))
	update.Header.Set("Content-Type", "application/json")
	update.Header.Set("Origin", "http://localhost:3000")
	update.AddCookie(cookies[0])
	updated := httptest.NewRecorder()
	server.ServeHTTP(updated, update)
	if updated.Code != http.StatusOK || !bytes.Contains(updated.Body.Bytes(), []byte(`"versionNumber":2`)) {
		t.Fatalf("DNA update response = %d %s", updated.Code, updated.Body.String())
	}

	historyRequest := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+workspaceID+"/content-dna/versions", nil)
	historyRequest.AddCookie(cookies[0])
	history := httptest.NewRecorder()
	server.ServeHTTP(history, historyRequest)
	if history.Code != http.StatusOK || !bytes.Contains(history.Body.Bytes(), []byte(`"versionNumber":2`)) || !bytes.Contains(history.Body.Bytes(), []byte(`"versionNumber":1`)) {
		t.Fatalf("DNA history response = %d %s", history.Code, history.Body.String())
	}

	versionRequest := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+workspaceID+"/content-dna/versions/"+firstVersion.Id.String(), nil)
	versionRequest.AddCookie(cookies[0])
	versionResponse := httptest.NewRecorder()
	server.ServeHTTP(versionResponse, versionRequest)
	if versionResponse.Code != http.StatusOK || !bytes.Contains(versionResponse.Body.Bytes(), []byte(`"creatorOrBrandDescription":"Private DNA"`)) {
		t.Fatalf("historical DNA response = %d %s", versionResponse.Code, versionResponse.Body.String())
	}

	foreignOrigin := httptest.NewRequest(http.MethodPut, "/api/v1/workspaces/"+workspaceID+"/content-dna", strings.NewReader(requestBody))
	foreignOrigin.Header.Set("Content-Type", "application/json")
	foreignOrigin.Header.Set("Origin", "https://attacker.example")
	foreignOrigin.AddCookie(cookies[0])
	denied := httptest.NewRecorder()
	server.ServeHTTP(denied, foreignOrigin)
	if denied.Code != http.StatusForbidden || !bytes.Contains(denied.Body.Bytes(), []byte(`"INVALID_ORIGIN"`)) {
		t.Fatalf("foreign-origin DNA save = %d %s", denied.Code, denied.Body.String())
	}
}

func TestBeginAssetUploadUsesAuthenticatedWorkspaceAndReturnsPrivateCapability(t *testing.T) {
	database := openHTTPAPITestDatabase(t)
	secret := strings.Repeat("u", 32)
	identityService := identity.NewService(database, "http://localhost:3000", secret, clock.System{}, false)
	storageRoot := t.TempDir()
	developmentStorage, err := assets.OpenDevelopmentFilesystemStorage(storageRoot, "http://localhost:8080", []byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = developmentStorage.Close() })
	assetService := developmentStorage.NewService(database, time.Now, nil)
	application := New(identityService, dna.NewService(database), ideas.NewService(database, nil, clock.System{}, nil), content.NewService(database, nil, clock.System{}, nil), content.NewQueueService(database), assetService)
	server := httpserver.NewHandler(database, application, developmentStorage.Handler())
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/workspaces/" + uuid.NewString() + "/assets/uploads"},
		{http.MethodPost, "/api/v1/workspaces/" + uuid.NewString() + "/assets/external-url"},
		{http.MethodGet, "/api/v1/workspaces/" + uuid.NewString() + "/assets"},
		{http.MethodPatch, "/api/v1/workspaces/" + uuid.NewString() + "/assets/" + uuid.NewString()},
		{http.MethodDelete, "/api/v1/workspaces/" + uuid.NewString() + "/assets/" + uuid.NewString()},
	} {
		request := httptest.NewRequest(route.method, route.path, nil)
		request.Header.Set("Origin", "http://localhost:3000")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %s status = %d; body=%s", route.method, route.path, response.Code, response.Body.String())
		}
	}
	email := uuid.NewString() + "@example.com"
	var workspaceID, userID string
	t.Cleanup(func() {
		if workspaceID != "" {
			_, _ = database.Exec(`DELETE FROM asset_jobs WHERE payload->>'assetId' IN (SELECT id::text FROM assets WHERE workspace_id = $1::uuid)`, workspaceID)
			_, _ = database.Exec(`DELETE FROM assets WHERE workspace_id = $1::uuid`, workspaceID)
			_, _ = database.Exec(`DELETE FROM asset_admission_events WHERE workspace_id = $1::uuid`, workspaceID)
			_, _ = database.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1::uuid`, workspaceID)
			_, _ = database.Exec(`DELETE FROM workspaces WHERE id = $1::uuid`, workspaceID)
		}
		if userID != "" {
			_, _ = database.Exec(`DELETE FROM "session" WHERE user_id = $1`, userID)
			_, _ = database.Exec(`DELETE FROM account WHERE user_id = $1`, userID)
			_, _ = database.Exec(`DELETE FROM "user" WHERE id = $1`, userID)
		}
	})
	signUpBody, _ := json.Marshal(transport.SignUpRequest{Name: "Asset API user", Email: openapi_types.Email(email), Password: "secure password 123"})
	signUpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sign-up", bytes.NewReader(signUpBody))
	signUpRequest.Header.Set("Content-Type", "application/json")
	signUpRequest.Header.Set("Origin", "http://localhost:3000")
	signUpResponse := httptest.NewRecorder()
	server.ServeHTTP(signUpResponse, signUpRequest)
	if signUpResponse.Code != http.StatusOK {
		t.Fatalf("sign-up status/body = %d %s", signUpResponse.Code, signUpResponse.Body.String())
	}
	var session transport.SessionResponse
	if err := json.Unmarshal(signUpResponse.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	userID = session.User.Id
	if err := database.QueryRow(`SELECT workspace_id::text FROM workspace_members WHERE user_id = $1`, userID).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(transport.BeginAssetUploadRequest{
		MediaType: transport.BeginAssetUploadRequestMediaTypeIMAGE, DisplayName: "A portrait", OriginalFilename: "portrait.png", ExpectedUploadSizeBytes: 512,
	})
	response := sendJSONWithCookie(t, server, http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/assets/uploads", body, signUpResponse.Result().Cookies()[0])
	if response.Code != http.StatusCreated {
		t.Fatalf("begin upload status/body = %d %s", response.Code, response.Body.String())
	}
	var result transport.BeginAssetUploadResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != transport.BeginAssetUploadResultStatusPENDING || result.SourceType != transport.BeginAssetUploadResultSourceTypeUPLOAD || result.ExpectedUploadSizeBytes != 512 {
		t.Fatalf("begin upload result = %+v", result)
	}
	if !strings.HasPrefix(result.Capability.Url, "http://localhost:8080/_dev/assets/staging/") || result.Capability.ContentLength != 512 {
		t.Fatalf("upload capability is not a bounded private local URL: %+v", result.Capability)
	}
	var storedWorkspaceID string
	if err := database.QueryRow(`SELECT workspace_id::text FROM assets WHERE id = $1::uuid`, result.AssetId.String()).Scan(&storedWorkspaceID); err != nil || storedWorkspaceID != workspaceID {
		t.Fatalf("Asset workspace = %q, err=%v", storedWorkspaceID, err)
	}
	libraryRequest := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+workspaceID+"/assets?status=PENDING&search=portrait", nil)
	libraryRequest.AddCookie(signUpResponse.Result().Cookies()[0])
	libraryResponse := httptest.NewRecorder()
	server.ServeHTTP(libraryResponse, libraryRequest)
	if libraryResponse.Code != http.StatusOK {
		t.Fatalf("Asset Library status/body = %d %s", libraryResponse.Code, libraryResponse.Body.String())
	}
	var library transport.AssetLibraryResult
	if err := json.Unmarshal(libraryResponse.Body.Bytes(), &library); err != nil {
		t.Fatal(err)
	}
	if len(library.Assets) != 1 || library.Assets[0].Id != result.AssetId || library.Assets[0].DisplayName != "A portrait" || library.Assets[0].ReferenceCount != 0 {
		t.Fatalf("Asset Library response = %+v", library)
	}
	renameBody, _ := json.Marshal(transport.RenameAssetRequest{DisplayName: "Renamed portrait"})
	renamedResponse := sendJSONWithCookie(t, server, http.MethodPatch, "/api/v1/workspaces/"+workspaceID+"/assets/"+result.AssetId.String(), renameBody, signUpResponse.Result().Cookies()[0])
	if renamedResponse.Code != http.StatusOK {
		t.Fatalf("rename Asset status/body = %d %s", renamedResponse.Code, renamedResponse.Body.String())
	}
	var renamed transport.AssetLibraryItem
	if err := json.Unmarshal(renamedResponse.Body.Bytes(), &renamed); err != nil {
		t.Fatal(err)
	}
	if renamed.Id != result.AssetId || renamed.DisplayName != "Renamed portrait" {
		t.Fatalf("renamed Asset response = %+v", renamed)
	}
	capabilityURL, err := url.Parse(result.Capability.Url)
	if err != nil {
		t.Fatal(err)
	}
	putRequest := httptest.NewRequest(http.MethodPut, capabilityURL.RequestURI(), bytes.NewReader(make([]byte, result.Capability.ContentLength)))
	putResponse := httptest.NewRecorder()
	server.ServeHTTP(putResponse, putRequest)
	if putResponse.Code != http.StatusCreated {
		t.Fatalf("private staging PUT status/body = %d %s", putResponse.Code, putResponse.Body.String())
	}
	finalize := sendJSONWithCookie(t, server, http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/assets/"+result.AssetId.String()+"/finalize-upload", nil, signUpResponse.Result().Cookies()[0])
	if finalize.Code != http.StatusOK {
		t.Fatalf("finalize upload status/body = %d %s", finalize.Code, finalize.Body.String())
	}
	var finalized transport.FinalizeAssetUploadResult
	if err := json.Unmarshal(finalize.Body.Bytes(), &finalized); err != nil {
		t.Fatal(err)
	}
	if finalized.AssetId != result.AssetId || !bool(finalized.WorkflowEstablished) {
		t.Fatalf("finalize upload result = %+v", finalized)
	}
	permanentKey := "permanent/" + uuid.NewString()
	permanentPath := filepath.Join(storageRoot, "permanent", strings.TrimPrefix(permanentKey, "permanent/"))
	if err := os.MkdirAll(filepath.Dir(permanentPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(permanentPath, make([]byte, 512), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE assets SET status = 'READY', permanent_key = $2, byte_size = 512, detected_mime_type = 'image/png', media_format = 'PNG' WHERE id = $1::uuid`, result.AssetId.String(), permanentKey); err != nil {
		t.Fatal(err)
	}
	readBody, _ := json.Marshal(transport.AssetReadCapabilityRequest{Operation: transport.PREVIEW})
	readCapability := sendJSONWithCookie(t, server, http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/assets/"+result.AssetId.String()+"/read-capability", readBody, signUpResponse.Result().Cookies()[0])
	if readCapability.Code != http.StatusOK {
		t.Fatalf("private read capability status/body = %d %s", readCapability.Code, readCapability.Body.String())
	}
	var preview transport.AssetReadCapability
	if err := json.Unmarshal(readCapability.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.ContentDisposition != "inline" || preview.CacheControl != transport.PrivateNoStore || preview.RangeAllowed || !strings.HasPrefix(preview.Url, "http://localhost:8080/_dev/assets/private/") {
		t.Fatalf("private preview capability = %+v", preview)
	}
	deletedResponse := sendJSONWithCookie(t, server, http.MethodDelete, "/api/v1/workspaces/"+workspaceID+"/assets/"+result.AssetId.String(), nil, signUpResponse.Result().Cookies()[0])
	if deletedResponse.Code != http.StatusAccepted {
		t.Fatalf("delete Asset status/body = %d %s", deletedResponse.Code, deletedResponse.Body.String())
	}
	var deleted transport.DeleteAssetResult
	if err := json.Unmarshal(deletedResponse.Body.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	if deleted.AssetId != result.AssetId || deleted.Status != transport.DeleteAssetResultStatusDELETING || !deleted.CleanupWorkflowEstablished {
		t.Fatalf("delete Asset result = %+v", deleted)
	}
	externalBody, _ := json.Marshal(transport.CreateExternalURLAssetRequest{
		MediaType: transport.CreateExternalURLAssetRequestMediaTypeIMAGE, DisplayName: "External reference", SourceUrl: "https://BÜCHER.example:443/media?id=1#preview",
	})
	externalResponse := sendJSONWithCookie(t, server, http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/assets/external-url", externalBody, signUpResponse.Result().Cookies()[0])
	if externalResponse.Code != http.StatusCreated {
		t.Fatalf("create external Asset status/body = %d %s", externalResponse.Code, externalResponse.Body.String())
	}
	var external transport.CreateExternalURLAssetResult
	if err := json.Unmarshal(externalResponse.Body.Bytes(), &external); err != nil {
		t.Fatal(err)
	}
	if external.Status != transport.CreateExternalURLAssetResultStatusPENDING || external.SourceType != transport.CreateExternalURLAssetResultSourceTypeEXTERNALURL || external.SourceHost != "xn--bcher-kva.example" {
		t.Fatalf("create external Asset result = %+v", external)
	}
}

func TestIdeaAndProductionQueueRoutesUseGoApplicationServices(t *testing.T) {
	database := openHTTPAPITestDatabase(t)
	secret := strings.Repeat("q", 32)
	identityService := identity.NewService(database, "http://localhost:3000", secret, clock.System{}, false)
	ideaService := ideas.NewService(database, func(string) (ai.GenerateIdeasProvider, error) { return ideaAPIProvider{}, nil }, clock.System{}, nil)
	contentOutput, err := document.ParseGenerated([]byte(`{"schemaVersion":1,"script":{"blocks":[{"type":"paragraph","text":"HTTP generated Content","performanceDirections":[],"editDirections":[]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	contentProvider := contentAPIProvider{output: &contentOutput}
	contentService := content.NewService(database, func(string) (ai.GenerateContentProvider, error) { return contentProvider, nil }, clock.System{}, nil)
	server := httpserver.NewHandler(database, New(identityService, dna.NewService(database), ideaService, contentService, content.NewQueueService(database)))
	email := uuid.NewString() + "@example.com"
	workspaceID := ""
	userID := ""
	t.Cleanup(func() {
		if workspaceID != "" {
			_, _ = database.ExecContext(context.Background(), `UPDATE contents SET accepted_version_id = NULL WHERE workspace_id = $1::uuid`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM content_versions WHERE content_id IN (SELECT id FROM contents WHERE workspace_id = $1::uuid)`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM content_drafts WHERE content_id IN (SELECT id FROM contents WHERE workspace_id = $1::uuid)`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM contents WHERE workspace_id = $1::uuid`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM workspace_content_generation_quota_reservations WHERE workspace_id = $1::uuid`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM content_generation_attempts WHERE workspace_id = $1::uuid`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM workspace_generation_quota_reservations WHERE workspace_id = $1::uuid`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM ideas WHERE batch_id IN (SELECT id FROM idea_generation_batches WHERE workspace_id = $1::uuid)`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM idea_generation_batches WHERE workspace_id = $1::uuid`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM ai_runs WHERE workspace_id = $1::uuid`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM content_dna_versions WHERE content_dna_id IN (SELECT id FROM content_dna WHERE workspace_id = $1::uuid)`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM content_dna WHERE workspace_id = $1::uuid`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM workspace_members WHERE workspace_id = $1::uuid`, workspaceID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM workspaces WHERE id = $1::uuid`, workspaceID)
		}
		if userID != "" {
			_, _ = database.ExecContext(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
		}
	})
	signUpBody, _ := json.Marshal(transport.SignUpRequest{Name: "Ideas API user", Email: openapi_types.Email(email), Password: "secure password 123"})
	signUpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sign-up", bytes.NewReader(signUpBody))
	signUpRequest.Header.Set("Content-Type", "application/json")
	signUpRequest.Header.Set("Origin", "http://localhost:3000")
	created := httptest.NewRecorder()
	identityService.SignUp(created, signUpRequest)
	if created.Code != http.StatusOK {
		t.Fatalf("sign-up status/body = %d %s", created.Code, created.Body.String())
	}
	var session transport.SessionResponse
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	userID = session.User.Id
	if err := database.QueryRowContext(context.Background(), `SELECT workspace_id::text FROM workspace_members WHERE user_id = $1`, userID).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	version, err := dna.NewService(database).Save(context.Background(), ideaAPITestSubject(userID), workspaceID, nil, []byte(`{"schemaVersion":1,"identity":{"creatorOrBrandDescription":"Creator"},"audience":{"targetAudienceDescription":"Readers"},"expertise":{"primaryTopics":["Education"]},"voice":{"toneTraits":["Clear"]},"goals":{"contentGoals":["Teach"]},"language":{"defaultContentLanguage":"en","contentLanguages":["en","fa"]}}`))
	if err != nil {
		t.Fatalf("save AI-ready DNA: %v", err)
	}
	cookie := created.Result().Cookies()[0]
	basePath := "/api/v1/workspaces/" + workspaceID
	encodedGenerate, _ := json.Marshal(map[string]any{"baseContentDnaVersionId": version.ID, "requestedLanguage": "en", "idempotencyKey": uuid.NewString()})
	generated := sendJSONWithCookie(t, server, http.MethodPost, basePath+"/ideas/generations", encodedGenerate, cookie)
	if generated.Code != http.StatusOK {
		t.Fatalf("generate status/body = %d %s", generated.Code, generated.Body.String())
	}
	var generation transport.IdeaGenerationResult
	if err := json.Unmarshal(generated.Body.Bytes(), &generation); err != nil {
		t.Fatal(err)
	}
	if generation.Batch.Status != transport.IdeaLifecycle("COMPLETED") || generation.Batch.RequestedCount != transport.IdeaGenerationBatchRequestedCountN20 || generation.Replayed {
		t.Fatalf("generation response = %+v", generation)
	}

	historyResponse := sendJSONWithCookie(t, server, http.MethodGet, basePath+"/ideas/generations", nil, cookie)
	var history transport.IdeaGenerationHistoryResult
	if historyResponse.Code != http.StatusOK || json.Unmarshal(historyResponse.Body.Bytes(), &history) != nil || len(history.Batches) != 1 || history.SelectedBatchId == nil || history.SelectedBatchId.String() != generation.Batch.Id.String() {
		t.Fatalf("history response = %d %s", historyResponse.Code, historyResponse.Body.String())
	}
	detailResponse := sendJSONWithCookie(t, server, http.MethodGet, basePath+"/ideas/generations/"+generation.Batch.Id.String(), nil, cookie)
	var detail transport.IdeaGenerationBatchDetail
	if detailResponse.Code != http.StatusOK || json.Unmarshal(detailResponse.Body.Bytes(), &detail) != nil || len(detail.Ideas) != ai.IdeaGenerationCount {
		t.Fatalf("detail response = %d %s", detailResponse.Code, detailResponse.Body.String())
	}
	libraryResponse := sendJSONWithCookie(t, server, http.MethodGet, basePath+"/ideas?statusFilter=ALL", nil, cookie)
	var library transport.IdeaLibrary
	if libraryResponse.Code != http.StatusOK || json.Unmarshal(libraryResponse.Body.Bytes(), &library) != nil || len(library.Ideas) != ai.IdeaGenerationCount || library.StatusFilter != transport.IdeaLibraryStatusFilterALL {
		t.Fatalf("Idea Library response = %d %s", libraryResponse.Code, libraryResponse.Body.String())
	}

	decisionBody, _ := json.Marshal(transport.UpdateIdeaDecisionRequest{NextState: transport.UpdateIdeaDecisionRequestNextStateACCEPTED})
	decisionResponse := sendJSONWithCookie(t, server, http.MethodPatch, basePath+"/ideas/"+detail.Ideas[0].Id.String()+"/decision", decisionBody, cookie)
	var decision transport.IdeaDecision
	if decisionResponse.Code != http.StatusOK || json.Unmarshal(decisionResponse.Body.Bytes(), &decision) != nil || decision.Status != transport.IdeaDecisionStatusACCEPTED {
		t.Fatalf("decision response = %d %s", decisionResponse.Code, decisionResponse.Body.String())
	}
	queueResponse := sendJSONWithCookie(t, server, http.MethodGet, basePath+"/production-queue", nil, cookie)
	var queue []transport.ProductionQueueItem
	if queueResponse.Code != http.StatusOK || json.Unmarshal(queueResponse.Body.Bytes(), &queue) != nil || len(queue) != 1 || queue[0].Id != detail.Ideas[0].Id {
		t.Fatalf("queue response = %d %s", queueResponse.Code, queueResponse.Body.String())
	}
	reorderBody, _ := json.Marshal(transport.ReorderProductionQueueRequest{OrderedIdeaIds: []openapi_types.UUID{queue[0].Id}})
	reordered := sendJSONWithCookie(t, server, http.MethodPut, basePath+"/production-queue", reorderBody, cookie)
	if reordered.Code != http.StatusOK {
		t.Fatalf("queue reorder status/body = %d %s", reordered.Code, reordered.Body.String())
	}
	idempotencyKey := uuid.NewString()
	contentRequest, _ := json.Marshal(map[string]any{
		"sourceIdeaId":            detail.Ideas[0].Id,
		"baseContentDnaVersionId": version.ID,
		"requestedLanguage":       "en",
		"format":                  "SHORT_VIDEO",
		"idempotencyKey":          idempotencyKey,
	})
	contentResponse := sendJSONWithCookie(t, server, http.MethodPost, basePath+"/contents/generations", contentRequest, cookie)
	var generatedContent transport.ContentGenerationResult
	if contentResponse.Code != http.StatusOK || json.Unmarshal(contentResponse.Body.Bytes(), &generatedContent) != nil || generatedContent.Attempt.Status != transport.ContentGenerationLifecycleCOMPLETED || generatedContent.ContentId == nil || generatedContent.Replayed {
		t.Fatalf("Content generation response = %d %s", contentResponse.Code, contentResponse.Body.String())
	}
	ideaPath := basePath + "/content-ideas/" + detail.Ideas[0].Id.String()
	contentHistoryResponse := sendJSONWithCookie(t, server, http.MethodGet, ideaPath+"/content-history", nil, cookie)
	var contentHistory transport.IdeaContentGenerationHistory
	if contentHistoryResponse.Code != http.StatusOK || json.Unmarshal(contentHistoryResponse.Body.Bytes(), &contentHistory) != nil || !contentHistory.IsUsed || len(contentHistory.Attempts) != 1 || contentHistory.Attempts[0].Id.String() != generatedContent.Attempt.Id.String() {
		t.Fatalf("Content generation history = %d %s", contentHistoryResponse.Code, contentHistoryResponse.Body.String())
	}
	contentByIdeaResponse := sendJSONWithCookie(t, server, http.MethodGet, ideaPath, nil, cookie)
	var contentByIdea transport.ContentByIdea
	if contentByIdeaResponse.Code != http.StatusOK || json.Unmarshal(contentByIdeaResponse.Body.Bytes(), &contentByIdea) != nil || contentByIdea.SourceIdea.Id != detail.Ideas[0].Id || len(contentByIdea.Content) != 1 || len(contentByIdea.History.Attempts) != 1 {
		t.Fatalf("Content by Idea = %d %s", contentByIdeaResponse.Code, contentByIdeaResponse.Body.String())
	}
	contentUsageResponse := sendJSONWithCookie(t, server, http.MethodGet, ideaPath+"/content-usage", nil, cookie)
	var contentUsage transport.IdeaContentUsage
	if contentUsageResponse.Code != http.StatusOK || json.Unmarshal(contentUsageResponse.Body.Bytes(), &contentUsage) != nil || !contentUsage.IsUsed || contentUsage.IdeaId != detail.Ideas[0].Id {
		t.Fatalf("Content usage = %d %s", contentUsageResponse.Code, contentUsageResponse.Body.String())
	}
	attemptPath := basePath + "/content-generation-attempts/" + generatedContent.Attempt.Id.String()
	attemptDetailResponse := sendJSONWithCookie(t, server, http.MethodGet, attemptPath, nil, cookie)
	var attemptDetail transport.ContentGenerationAttemptDetail
	if attemptDetailResponse.Code != http.StatusOK || json.Unmarshal(attemptDetailResponse.Body.Bytes(), &attemptDetail) != nil || attemptDetail.Attempt.Id != generatedContent.Attempt.Id || attemptDetail.SourceIdea.Id != detail.Ideas[0].Id {
		t.Fatalf("Content Attempt detail = %d %s", attemptDetailResponse.Code, attemptDetailResponse.Body.String())
	}
	attemptResultResponse := sendJSONWithCookie(t, server, http.MethodGet, attemptPath+"/result", nil, cookie)
	var attemptResult transport.ContentDetail
	if attemptResultResponse.Code != http.StatusOK || json.Unmarshal(attemptResultResponse.Body.Bytes(), &attemptResult) != nil || attemptResult.Id != *generatedContent.ContentId {
		t.Fatalf("Content Attempt result = %d %s", attemptResultResponse.Code, attemptResultResponse.Body.String())
	}
	retryCompletedResponse := sendJSONWithCookie(t, server, http.MethodPost, basePath+"/content-generation-attempts/"+generatedContent.Attempt.Id.String()+"/retry", nil, cookie)
	if retryCompletedResponse.Code != http.StatusConflict || !bytes.Contains(retryCompletedResponse.Body.Bytes(), []byte(`"CONFLICT"`)) {
		t.Fatalf("retry completed Attempt = %d %s", retryCompletedResponse.Code, retryCompletedResponse.Body.String())
	}
	contentPath := basePath + "/contents/" + generatedContent.ContentId.String()
	listResponse := sendJSONWithCookie(t, server, http.MethodGet, basePath+"/contents", nil, cookie)
	var listed []transport.ContentListItem
	if listResponse.Code != http.StatusOK || json.Unmarshal(listResponse.Body.Bytes(), &listed) != nil || len(listed) != 1 || listed[0].Id != *generatedContent.ContentId {
		t.Fatalf("Content list response = %d %s", listResponse.Code, listResponse.Body.String())
	}
	readResponse := sendJSONWithCookie(t, server, http.MethodGet, contentPath, nil, cookie)
	var contentDetail transport.ContentDetail
	if readResponse.Code != http.StatusOK || json.Unmarshal(readResponse.Body.Bytes(), &contentDetail) != nil || contentDetail.Draft.Revision != 1 || len(contentDetail.Versions) != 1 {
		t.Fatalf("Content detail response = %d %s", readResponse.Code, readResponse.Body.String())
	}
	draftDocument := json.RawMessage(`{"schemaVersion":4,"script":{"blocks":[{"id":"11111111-1111-4111-8111-111111111111","type":"paragraph","text":"Accepted through HTTP.","performanceDirections":[],"editDirections":[]}]}}`)
	draftRequest, _ := json.Marshal(map[string]any{"baseRevision": 1, "document": draftDocument})
	savedResponse := sendJSONWithCookie(t, server, http.MethodPut, contentPath+"/draft", draftRequest, cookie)
	var savedDraft transport.ContentDraft
	if savedResponse.Code != http.StatusOK || json.Unmarshal(savedResponse.Body.Bytes(), &savedDraft) != nil || savedDraft.Revision != 2 {
		t.Fatalf("Content Draft save response = %d %s", savedResponse.Code, savedResponse.Body.String())
	}
	acceptRequest, _ := json.Marshal(transport.AcceptContentRequest{ExpectedDraftRevision: 2})
	acceptedResponse := sendJSONWithCookie(t, server, http.MethodPost, contentPath+"/acceptance", acceptRequest, cookie)
	var accepted transport.ContentAcceptanceResult
	if acceptedResponse.Code != http.StatusOK || json.Unmarshal(acceptedResponse.Body.Bytes(), &accepted) != nil || accepted.Draft.Revision != 2 || accepted.AcceptedVersion.VersionNumber != 2 || !accepted.AcceptedVersion.IsCurrentAccepted {
		t.Fatalf("Content acceptance response = %d %s", acceptedResponse.Code, acceptedResponse.Body.String())
	}
	for _, route := range []struct {
		path string
		want string
	}{
		{path: contentPath + "/teleprompter", want: "Accepted through HTTP."},
		{path: contentPath + "/edit-guide", want: "Accepted through HTTP."},
	} {
		viewResponse := sendJSONWithCookie(t, server, http.MethodGet, route.path, nil, cookie)
		var view transport.ContentProductionView
		if viewResponse.Code != http.StatusOK || json.Unmarshal(viewResponse.Body.Bytes(), &view) != nil || view.Status != transport.ContentProductionViewStatus("READY") || !bytes.Contains(viewResponse.Body.Bytes(), []byte(route.want)) {
			t.Fatalf("Content production view %s = %d %s", route.path, viewResponse.Code, viewResponse.Body.String())
		}
	}
}

func TestContentGenerationErrorsMapToSafeStatusCodes(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{name: "validation", err: content.ErrGenerationValidation, status: http.StatusBadRequest},
		{name: "conflict", err: content.ErrGenerationConflict, status: http.StatusConflict},
		{name: "quota", err: content.ErrGenerationRateLimited, status: http.StatusTooManyRequests},
		{name: "invalid output", err: content.ErrGenerationInvalidOutput, status: http.StatusBadGateway},
		{name: "provider", err: content.ErrGenerationProvider, status: http.StatusServiceUnavailable},
		{name: "not found", err: content.ErrGenerationNotFound, status: http.StatusNotFound},
		{name: "draft validation", err: content.ErrDraftValidation, status: http.StatusBadRequest},
		{name: "draft conflict", err: content.ErrDraftConflict, status: http.StatusConflict},
		{name: "content not found", err: content.ErrContentNotFound, status: http.StatusNotFound},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeDomainError(response, fmt.Errorf("private provider diagnostic: %w", testCase.err))
			if response.Code != testCase.status {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, testCase.status, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private provider diagnostic") {
				t.Fatalf("internal diagnostic leaked in response: %s", response.Body.String())
			}
		})
	}
}

func sendJSONWithCookie(t *testing.T, handler http.Handler, method, path string, body []byte, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://localhost:3000")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func openHTTPAPITestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || !strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") {
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
