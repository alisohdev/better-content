package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type betterAuthCookieFixture struct {
	Secret               string `json:"secret"`
	DatabaseSessionToken string `json:"databaseSessionToken"`
	CookieValue          string `json:"cookieValue"`
}

func readBetterAuthCookieFixture(t *testing.T) betterAuthCookieFixture {
	t.Helper()
	fixturePath := filepath.Join("..", "..", "..", ".scratch", "phase-07-go-backend-migration", "fixtures", "better-auth-session-cookie-v1.json")
	contents, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read Better Auth cookie fixture: %v", err)
	}
	var expected betterAuthCookieFixture
	if err := json.Unmarshal(contents, &expected); err != nil {
		t.Fatalf("decode Better Auth cookie fixture: %v", err)
	}
	return expected
}

func TestSessionCookieMatchesCompatibilityAttributesAndSignature(t *testing.T) {
	now := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
	service := NewService(nil, "https://example.com/", strings.Repeat("s", 32), fixedClock{now}, false)
	recorder := httptest.NewRecorder()
	service.setSessionCookie(recorder, "database-token", now.Add(sessionDuration))

	cookie := recorder.Result().Cookies()[0]
	if cookie.Name != "__Secure-better-auth.session_token" || !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie attributes differ from compatibility contract: %+v", cookie)
	}
	if cookie.MaxAge != int(sessionDuration.Seconds()) {
		t.Fatalf("MaxAge = %d, want %d", cookie.MaxAge, int(sessionDuration.Seconds()))
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookie)
	if token, ok := service.sessionToken(request); !ok || token != "database-token" {
		t.Fatalf("signed cookie parsed as (%q, %v)", token, ok)
	}
	cookie.Value = strings.Replace(cookie.Value, "database-token", "changed-token", 1)
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookie)
	if _, ok := service.sessionToken(request); ok {
		t.Fatal("tampered session cookie was accepted")
	}
}

func TestSessionCookieVerifiesBetterAuthPinnedImplementationFixture(t *testing.T) {
	expected := readBetterAuthCookieFixture(t)
	service := NewService(nil, "http://localhost:3000", expected.Secret, fixedClock{time.Now().UTC()}, false)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: service.cookieName, Value: expected.CookieValue})
	if token, ok := service.sessionToken(request); !ok || token != expected.DatabaseSessionToken {
		t.Fatalf("Better Auth cookie fixture parsed as (%q, %v)", token, ok)
	}
}

func TestOriginAndPasswordLengthValidation(t *testing.T) {
	service := NewService(nil, "http://localhost:3000", strings.Repeat("s", 32), fixedClock{time.Now()}, false)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sign-in", nil)
	if service.validOrigin(request) {
		t.Fatal("missing Origin header was accepted")
	}
	request.Header.Set("Origin", "http://localhost:3001")
	if service.validOrigin(request) {
		t.Fatal("untrusted Origin header was accepted")
	}
	request.Header.Set("Origin", "http://localhost:3000")
	if !service.validOrigin(request) {
		t.Fatal("configured Origin header was rejected")
	}
	if validPasswordLength("1234567") || !validPasswordLength("12345678") || validPasswordLength(strings.Repeat("x", 129)) {
		t.Fatal("password length validation did not enforce the 8–128 code-unit contract")
	}
}

func TestOriginNormalizationMatchesURLOriginSemantics(t *testing.T) {
	service := NewService(nil, "HTTPS://EXAMPLE.COM:443/", strings.Repeat("s", 32), fixedClock{time.Now().UTC()}, false)
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Origin", "https://example.com")
	if !service.validOrigin(request) {
		t.Fatal("canonical browser origin was rejected")
	}
}

func TestRequireAuthenticatedBlocksCrossOriginMutationBeforeDatabaseAccess(t *testing.T) {
	service := NewService(nil, "http://localhost:3000", strings.Repeat("s", 32), fixedClock{time.Now().UTC()}, false)
	called := false
	handler := RequireAuthenticated(service, func(http.ResponseWriter, *http.Request, Principal) {
		called = true
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/content", nil)
	request.AddCookie(&http.Cookie{Name: "better-auth.session_token", Value: "bad"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || called {
		t.Fatalf("cross-origin mutation status/called = %d/%v", recorder.Code, called)
	}
}

func TestProductionSignInAndSignUpRateLimitMatchesBetterAuthSpecialRule(t *testing.T) {
	now := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
	service := NewService(nil, "http://localhost:3000", strings.Repeat("s", 32), fixedClock{now}, true)
	for attempt := 1; attempt <= authRateLimit; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sign-in", strings.NewReader("not json"))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://localhost:3000")
		recorder := httptest.NewRecorder()
		service.SignIn(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d status = %d, want validation error", attempt, recorder.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sign-in", strings.NewReader("not json"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:3000")
	recorder := httptest.NewRecorder()
	service.SignIn(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("X-Retry-After") != "10" || !strings.Contains(recorder.Body.String(), "RATE_LIMITED") {
		t.Fatalf("rate limit response = %d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestRateLimitWindowResetsDeterministically(t *testing.T) {
	limiter := newRateLimiter()
	now := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
	if allowed, _ := limiter.Consume("client", now, 1, defaultRateWindow); !allowed {
		t.Fatal("first request was rejected")
	}
	if allowed, _ := limiter.Consume("client", now.Add(9*time.Second), 1, defaultRateWindow); allowed {
		t.Fatal("request inside rate window was accepted")
	}
	if allowed, _ := limiter.Consume("client", now.Add(defaultRateWindow), 1, defaultRateWindow); !allowed {
		t.Fatal("request at next rate window was rejected")
	}
}
