package identity

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"bettercontent/backend/internal/identity/password"
	"bettercontent/backend/internal/platform/clock"
	"bettercontent/backend/internal/platform/postgres"
	"bettercontent/backend/internal/transport"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

type testClock struct{ now time.Time }

func (c testClock) Now() time.Time { return c.now }

func TestAuthPersistenceSessionRenewalAndRevocation(t *testing.T) {
	database := openIdentityTestDatabase(t)
	clock := testClock{now: time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)}
	service := NewService(database, "http://localhost:3000", strings.Repeat("s", 32), clock, false)
	userID := uuid.NewString()
	email := userID + "@example.com"
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DELETE FROM "user" WHERE email = $1`, email)
	})

	passwordValue := "Better Auth fixture - e\u0301 - 123"
	created := sendAuthRequest(t, service.SignUp, "POST", "/api/v1/auth/sign-up", "http://localhost:3000", transport.SignUpRequest{
		Name: "Test user", Email: openapi_types.Email(strings.ToUpper(email)), Password: passwordValue,
	})
	if created.Code != http.StatusOK {
		t.Fatalf("sign-up status = %d, body=%s", created.Code, created.Body.String())
	}
	duplicate := sendAuthRequest(t, service.SignUp, "POST", "/api/v1/auth/sign-up", "http://localhost:3000", transport.SignUpRequest{
		Name: "Duplicate user", Email: openapi_types.Email(email), Password: passwordValue,
	})
	if duplicate.Code != http.StatusConflict || bytes.Contains(duplicate.Body.Bytes(), []byte(email)) {
		t.Fatalf("duplicate sign-up response = %d %s", duplicate.Code, duplicate.Body.String())
	}
	var signedUp transport.SessionResponse
	if err := json.Unmarshal(created.Body.Bytes(), &signedUp); err != nil {
		t.Fatalf("decode sign-up response: %v", err)
	}
	if signedUp.User.Id == "" || string(signedUp.User.Email) != email {
		t.Fatalf("unexpected sign-up response: %+v", signedUp)
	}
	legacyCookie := readBetterAuthCookieFixture(t)
	legacyService := NewService(database, "http://localhost:3000", legacyCookie.Secret, clock, false)
	if _, err := database.ExecContext(context.Background(), `
		INSERT INTO session (id, expires_at, token, created_at, updated_at, user_id)
		VALUES ($1, $2, $3, $4, $4, $5)
	`, uuid.NewString(), clock.now.Add(sessionDuration), legacyCookie.DatabaseSessionToken, clock.now, signedUp.User.Id); err != nil {
		t.Fatalf("create Better Auth-compatible session row: %v", err)
	}
	legacyRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	legacyRequest.AddCookie(&http.Cookie{Name: legacyService.cookieName, Value: legacyCookie.CookieValue})
	legacyResponse := httptest.NewRecorder()
	legacyService.GetSession(legacyResponse, legacyRequest)
	if legacyResponse.Code != http.StatusOK || !bytes.Contains(legacyResponse.Body.Bytes(), []byte(signedUp.User.Id)) {
		t.Fatalf("existing Better Auth session was not preserved: %d %s", legacyResponse.Code, legacyResponse.Body.String())
	}
	createdCookie := created.Result().Cookies()[0]
	if createdCookie.Name != "better-auth.session_token" || !createdCookie.HttpOnly || createdCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected auth cookie attributes: %+v", createdCookie)
	}

	var storedHash string
	if err := database.QueryRowContext(context.Background(), `SELECT password FROM account WHERE user_id = $1 AND provider_id = 'credential'`, signedUp.User.Id).Scan(&storedHash); err != nil {
		t.Fatalf("read persisted password hash: %v", err)
	}
	if matched, err := password.Verify("Better Auth fixture - \u00e9 - 123", storedHash); err != nil || !matched {
		t.Fatalf("persisted hash did not preserve NFKC behavior: matched=%v err=%v", matched, err)
	}
	token, ok := service.sessionToken(requestWithCookie(createdCookie))
	if !ok {
		t.Fatal("sign-up cookie signature did not match the stored session token")
	}
	var sessionID string
	if err := database.QueryRowContext(context.Background(), `SELECT id FROM session WHERE token = $1 AND user_id = $2`, token, signedUp.User.Id).Scan(&sessionID); err != nil {
		t.Fatalf("session cookie token not found for created user: %v", err)
	}
	principal, err := service.AuthenticateRequest(httptest.NewRecorder(), requestWithCookie(createdCookie))
	if err != nil {
		t.Fatalf("authenticate signed-in request: %v", err)
	}
	principalUserID, authenticated := principal.UserID()
	if !authenticated || string(principalUserID) != signedUp.User.Id {
		t.Fatalf("authenticated principal identity = (%q, %v)", principalUserID, authenticated)
	}

	login := sendAuthRequest(t, service.SignIn, "POST", "/api/v1/auth/sign-in", "http://localhost:3000", transport.SignInRequest{
		Email: openapi_types.Email(strings.ToUpper(email)), Password: "Better Auth fixture - \u00e9 - 123",
	})
	if login.Code != http.StatusOK {
		t.Fatalf("sign-in with NFKC-equivalent password status = %d, body=%s", login.Code, login.Body.String())
	}
	loginCookie := login.Result().Cookies()[0]

	current := httptest.NewRecorder()
	currentRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	currentRequest.AddCookie(loginCookie)
	service.GetSession(current, currentRequest)
	if current.Code != http.StatusOK || !bytes.Contains(current.Body.Bytes(), []byte(`"user"`)) {
		t.Fatalf("session lookup status/body = %d/%s", current.Code, current.Body.String())
	}
	if len(current.Result().Cookies()) != 0 {
		t.Fatal("fresh session unexpectedly emitted a renewal cookie")
	}

	loginToken, ok := service.sessionToken(requestWithCookie(loginCookie))
	if !ok {
		t.Fatal("could not verify sign-in cookie")
	}
	if _, err := database.ExecContext(context.Background(), `UPDATE session SET expires_at = $1 WHERE token = $2`, clock.now.Add(5*24*time.Hour), loginToken); err != nil {
		t.Fatalf("prepare renewal threshold: %v", err)
	}
	renewed := httptest.NewRecorder()
	renewRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	renewRequest.AddCookie(loginCookie)
	service.GetSession(renewed, renewRequest)
	if renewed.Code != http.StatusOK || len(renewed.Result().Cookies()) != 1 {
		t.Fatalf("renewal status/cookies = %d/%d", renewed.Code, len(renewed.Result().Cookies()))
	}
	var renewedExpiry time.Time
	if err := database.QueryRowContext(context.Background(), `SELECT expires_at FROM session WHERE token = $1`, loginToken).Scan(&renewedExpiry); err != nil {
		t.Fatalf("read renewed expiry: %v", err)
	}
	if !renewedExpiry.Equal(clock.now.Add(sessionDuration)) {
		t.Fatalf("renewed expiry = %s, want %s", renewedExpiry, clock.now.Add(sessionDuration))
	}

	signedOut := httptest.NewRecorder()
	signOutRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sign-out", nil)
	signOutRequest.Header.Set("Origin", "http://localhost:3000")
	signOutRequest.AddCookie(renewed.Result().Cookies()[0])
	service.SignOut(signedOut, signOutRequest)
	if signedOut.Code != http.StatusOK || signedOut.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf("sign-out status/cookie = %d/%+v", signedOut.Code, signedOut.Result().Cookies())
	}
	var remaining int
	if err := database.QueryRowContext(context.Background(), `SELECT count(*) FROM session WHERE token = $1`, loginToken).Scan(&remaining); err != nil {
		t.Fatalf("check revoked session: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("revoked session count = %d, want 0", remaining)
	}

	expiredToken, err := newSessionToken()
	if err != nil {
		t.Fatalf("create expired session token: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `
		INSERT INTO session (id, expires_at, token, created_at, updated_at, user_id)
		VALUES ($1, $2, $3, $4, $4, $5)
	`, uuid.NewString(), clock.now.Add(-time.Hour), expiredToken, clock.now.Add(-8*24*time.Hour), signedUp.User.Id); err != nil {
		t.Fatalf("create expired session: %v", err)
	}
	expiredCookieResponse := httptest.NewRecorder()
	service.setSessionCookie(expiredCookieResponse, expiredToken, clock.now.Add(-time.Hour))
	expiredRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	expiredRequest.AddCookie(expiredCookieResponse.Result().Cookies()[0])
	expiredResponse := httptest.NewRecorder()
	service.GetSession(expiredResponse, expiredRequest)
	if expiredResponse.Code != http.StatusOK || strings.TrimSpace(expiredResponse.Body.String()) != "null" || expiredResponse.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf("expired session response = %d %s cookies=%+v", expiredResponse.Code, expiredResponse.Body.String(), expiredResponse.Result().Cookies())
	}
	var workspaces int
	if err := database.QueryRowContext(context.Background(), `SELECT count(*) FROM workspace_members WHERE user_id = $1`, signedUp.User.Id).Scan(&workspaces); err != nil {
		t.Fatalf("check default workspace: %v", err)
	}
	if workspaces != 1 {
		t.Fatalf("default workspace memberships = %d, want 1", workspaces)
	}
}

func TestAuthRejectsUntrustedOriginAndHidesInvalidCredentials(t *testing.T) {
	database := openIdentityTestDatabase(t)
	service := NewService(database, "http://localhost:3000", strings.Repeat("s", 32), testClock{time.Now().UTC()}, false)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sign-up", strings.NewReader(`{"name":"A","email":"a@example.com","password":"12345678"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://attacker.example")
	recorder := httptest.NewRecorder()
	service.SignUp(recorder, request)
	if recorder.Code != http.StatusForbidden || !bytes.Contains(recorder.Body.Bytes(), []byte(`"INVALID_ORIGIN"`)) {
		t.Fatalf("untrusted origin response = %d %s", recorder.Code, recorder.Body.String())
	}

	invalidLogin := sendAuthRequest(t, service.SignIn, "POST", "/api/v1/auth/sign-in", "http://localhost:3000", transport.SignInRequest{
		Email: openapi_types.Email("missing@example.com"), Password: "wrong password",
	})
	if invalidLogin.Code != http.StatusUnauthorized || strings.Contains(invalidLogin.Body.String(), "missing@example.com") || strings.Contains(invalidLogin.Body.String(), "wrong password") {
		t.Fatalf("invalid login response leaked data or wrong status: %d %s", invalidLogin.Code, invalidLogin.Body.String())
	}
}

func sendAuthRequest(t *testing.T, handler http.HandlerFunc, method, path, origin string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode auth request: %v", err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func requestWithCookie(cookie *http.Cookie) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookie)
	return request
}

func openIdentityTestDatabase(t *testing.T) *sql.DB {
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
		if parseErr != nil || strings.EqualFold(parsed.Host+parsed.Path, primary.Host+primary.Path) {
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

var _ clock.Clock = testClock{}
