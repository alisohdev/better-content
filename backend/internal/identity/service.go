package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"bettercontent/backend/internal/identity/password"
	"bettercontent/backend/internal/platform/clock"
	"bettercontent/backend/internal/transport"
	"bettercontent/backend/internal/workspace"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

const (
	sessionDuration = 7 * 24 * time.Hour
	renewalWindow   = 6 * 24 * time.Hour
	passwordMinimum = 8
	passwordMaximum = 128
)

var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

var errInvalidCredentials = errors.New("invalid email or password")

type UserID string
type SessionID string

type Principal struct {
	userID    UserID
	sessionID SessionID
}

func (p Principal) AuthenticatedUserID() (string, bool) {
	if p.userID == "" || p.sessionID == "" {
		return "", false
	}
	return string(p.userID), true
}

func (p Principal) UserID() (UserID, bool) {
	if p.userID == "" || p.sessionID == "" {
		return "", false
	}
	return p.userID, true
}

type Service struct {
	database    *sql.DB
	clock       clock.Clock
	secret      string
	origin      string
	cookieName  string
	secure      bool
	production  bool
	rateLimiter *rateLimiter
}

func NewService(database *sql.DB, authURL, secret string, sourceClock clock.Clock, production bool) *Service {
	if sourceClock == nil {
		sourceClock = clock.System{}
	}
	parsed, _ := url.Parse(authURL)
	origin := ""
	secure := false
	if parsed != nil {
		origin = normalizeOrigin(parsed)
		secure = strings.EqualFold(parsed.Scheme, "https")
	}
	cookieName := "better-auth.session_token"
	if secure {
		cookieName = "__Secure-" + cookieName
	}
	return &Service{
		database: database, clock: sourceClock, secret: secret, origin: origin,
		cookieName: cookieName, secure: secure, production: production, rateLimiter: newRateLimiter(),
	}
}

func (s *Service) SignUp(w http.ResponseWriter, r *http.Request) {
	if !s.allowRequest(w, r) {
		return
	}
	var request transport.SignUpRequest
	if err := transport.DecodeJSON(r, &request); err != nil {
		transport.HandleDecodeError(w, err)
		return
	}
	if !validEmail(string(request.Email)) {
		transport.WriteError(w, http.StatusBadRequest, transport.VALIDATIONERROR, map[string]string{"email": "INVALID_EMAIL"})
		return
	}
	if strings.TrimSpace(request.Name) == "" {
		transport.WriteError(w, http.StatusBadRequest, transport.INVALIDNAME, map[string]string{"name": "INVALID_NAME"})
		return
	}
	if !validPasswordLength(request.Password) {
		transport.WriteError(w, http.StatusBadRequest, transport.VALIDATIONERROR, map[string]string{"password": "INVALID_PASSWORD"})
		return
	}
	passwordHash, err := password.Hash(request.Password)
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	now := s.clock.Now().UTC()
	userID := uuid.NewString()
	sessionToken, err := newSessionToken()
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	tx, err := s.database.BeginTx(r.Context(), nil)
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	defer tx.Rollback()
	user, err := createUser(r.Context(), tx, userID, strings.ToLower(string(request.Email)), request.Name, passwordHash, now)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" && pgError.ConstraintName == "user_email_unique" {
			transport.WriteError(w, http.StatusConflict, transport.CONFLICT, nil)
			return
		}
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	if _, err := workspace.GetOrCreateDefaultWorkspace(r.Context(), tx, user.ID); err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	session, err := createSession(r.Context(), tx, user.ID, sessionToken, requestMetadata(r), now)
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	if err := tx.Commit(); err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	s.setSessionCookie(w, session.Token, session.ExpiresAt)
	writeJSON(w, http.StatusOK, sessionResponse(user, session.ExpiresAt))
}

func (s *Service) SignIn(w http.ResponseWriter, r *http.Request) {
	if !s.allowRequest(w, r) {
		return
	}
	var request transport.SignInRequest
	if err := transport.DecodeJSON(r, &request); err != nil {
		transport.HandleDecodeError(w, err)
		return
	}
	if !validEmail(string(request.Email)) || !validPasswordLength(request.Password) {
		transport.WriteError(w, http.StatusBadRequest, transport.VALIDATIONERROR, nil)
		return
	}
	user, storedHash, err := findCredential(r.Context(), s.database, strings.ToLower(string(request.Email)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, errInvalidCredentials) {
			_, _ = password.Verify(request.Password, dummyPasswordHash)
			transport.WriteError(w, http.StatusUnauthorized, transport.INVALIDEMAILORPASSWORD, nil)
			return
		}
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	matched, err := password.Verify(request.Password, storedHash)
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	if !matched {
		transport.WriteError(w, http.StatusUnauthorized, transport.INVALIDEMAILORPASSWORD, nil)
		return
	}
	now := s.clock.Now().UTC()
	sessionToken, err := newSessionToken()
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	tx, err := s.database.BeginTx(r.Context(), nil)
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	defer tx.Rollback()
	if _, err := workspace.GetOrCreateDefaultWorkspace(r.Context(), tx, user.ID); err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	session, err := createSession(r.Context(), tx, user.ID, sessionToken, requestMetadata(r), now)
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	if err := tx.Commit(); err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	s.setSessionCookie(w, session.Token, session.ExpiresAt)
	writeJSON(w, http.StatusOK, sessionResponse(user, session.ExpiresAt))
}

func (s *Service) SignOut(w http.ResponseWriter, r *http.Request) {
	if !s.allowRequest(w, r) {
		return
	}
	if token, ok := s.sessionToken(r); ok {
		if _, err := s.database.ExecContext(r.Context(), `DELETE FROM session WHERE token = $1`, token); err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
	}
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, struct {
		Success bool `json:"success"`
	}{Success: true})
}

func (s *Service) GetSession(w http.ResponseWriter, r *http.Request) {
	if !s.allowRequest(w, r) {
		return
	}
	loaded, err := s.loadSession(r)
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
		return
	}
	if loaded == nil {
		s.clearSessionCookie(w)
		writeJSON(w, http.StatusOK, nil)
		return
	}
	if loaded.ExpiresAt.Sub(s.clock.Now()) <= renewalWindow {
		newExpiry := s.clock.Now().UTC().Add(sessionDuration)
		if _, err := s.database.ExecContext(r.Context(), `UPDATE session SET expires_at = $1, updated_at = $2 WHERE id = $3`, newExpiry, s.clock.Now().UTC(), loaded.ID); err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		loaded.ExpiresAt = newExpiry
		s.setSessionCookie(w, loaded.Token, loaded.ExpiresAt)
	}
	writeJSON(w, http.StatusOK, sessionResponse(loaded.User, loaded.ExpiresAt))
}

func (s *Service) AuthenticateRequest(w http.ResponseWriter, r *http.Request) (Principal, error) {
	loaded, err := s.loadSession(r)
	if err != nil {
		return Principal{}, err
	}
	if loaded == nil {
		return Principal{}, workspace.ErrUnauthenticated
	}
	if loaded.ExpiresAt.Sub(s.clock.Now()) <= renewalWindow {
		newExpiry := s.clock.Now().UTC().Add(sessionDuration)
		if _, err := s.database.ExecContext(r.Context(), `UPDATE session SET expires_at = $1, updated_at = $2 WHERE id = $3`, newExpiry, s.clock.Now().UTC(), loaded.ID); err != nil {
			return Principal{}, errors.New("could not renew authenticated session")
		}
		s.setSessionCookie(w, loaded.Token, newExpiry)
	}
	return Principal{userID: UserID(loaded.User.ID), sessionID: SessionID(loaded.ID)}, nil
}

func RequireAuthenticated(service *Service, next func(http.ResponseWriter, *http.Request, Principal)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !service.allowRequest(w, r) {
			return
		}
		principal, err := service.AuthenticateRequest(w, r)
		if err != nil {
			if errors.Is(err, workspace.ErrUnauthenticated) {
				transport.WriteError(w, http.StatusUnauthorized, transport.UNAUTHORIZED, nil)
				return
			}
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		next(w, r, principal)
	})
}

type loadedSession struct {
	ID        string
	Token     string
	ExpiresAt time.Time
	User      authUser
}

type authUser struct {
	ID    string
	Name  string
	Email string
}

func createUser(ctx context.Context, tx *sql.Tx, id, email, name, passwordHash string, now time.Time) (authUser, error) {
	user := authUser{ID: id, Name: name, Email: email}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO "user" (id, name, email, email_verified, created_at, updated_at)
		VALUES ($1, $2, $3, false, $4, $4)
	`, id, name, email, now); err != nil {
		return authUser{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO account (id, account_id, provider_id, user_id, password, created_at, updated_at)
		VALUES ($1, $2, 'credential', $2, $3, $4, $4)
	`, uuid.NewString(), id, passwordHash, now); err != nil {
		return authUser{}, err
	}
	return user, nil
}

func createSession(ctx context.Context, tx *sql.Tx, userID, token string, metadata requestInfo, now time.Time) (loadedSession, error) {
	session := loadedSession{ID: uuid.NewString(), Token: token, ExpiresAt: now.Add(sessionDuration), User: authUser{ID: userID}}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session (id, expires_at, token, created_at, updated_at, ip_address, user_agent, user_id)
		VALUES ($1, $2, $3, $4, $4, $5, $6, $7)
	`, session.ID, session.ExpiresAt, token, now, metadata.ipAddress, metadata.userAgent, userID); err != nil {
		return loadedSession{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT name, email FROM "user" WHERE id = $1`, userID).Scan(&session.User.Name, &session.User.Email); err != nil {
		return loadedSession{}, err
	}
	return session, nil
}

func findCredential(ctx context.Context, database *sql.DB, email string) (authUser, string, error) {
	var user authUser
	var encodedPassword sql.NullString
	err := database.QueryRowContext(ctx, `
		SELECT u.id, u.name, u.email, a.password
		FROM "user" AS u
		JOIN account AS a ON a.user_id = u.id
		WHERE u.email = $1 AND a.provider_id = 'credential' AND a.account_id = u.id
	`, email).Scan(&user.ID, &user.Name, &user.Email, &encodedPassword)
	if err != nil {
		return authUser{}, "", err
	}
	if !encodedPassword.Valid || encodedPassword.String == "" {
		return authUser{}, "", errInvalidCredentials
	}
	return user, encodedPassword.String, nil
}

func (s *Service) loadSession(r *http.Request) (*loadedSession, error) {
	token, ok := s.sessionToken(r)
	if !ok {
		return nil, nil
	}
	var loaded loadedSession
	err := s.database.QueryRowContext(r.Context(), `
		SELECT s.id, s.token, s.expires_at, u.id, u.name, u.email
		FROM session AS s
		JOIN "user" AS u ON u.id = s.user_id
		WHERE s.token = $1 AND s.expires_at > $2
	`, token, s.clock.Now().UTC()).Scan(
		&loaded.ID, &loaded.Token, &loaded.ExpiresAt,
		&loaded.User.ID, &loaded.User.Name, &loaded.User.Email,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("could not read session")
	}
	return &loaded, nil
}

func (s *Service) sessionToken(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(s.cookieName)
	if err != nil {
		return "", false
	}
	decoded, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		return "", false
	}
	separator := strings.LastIndexByte(decoded, '.')
	if separator <= 0 || separator == len(decoded)-1 {
		return "", false
	}
	token := decoded[:separator]
	signature, err := base64.StdEncoding.DecodeString(decoded[separator+1:])
	if err != nil {
		return "", false
	}
	if !hmacMatches(s.secret, token, signature) {
		return "", false
	}
	return token, true
}

func (s *Service) setSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	value := url.QueryEscape(token + "." + signToken(s.secret, token))
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName, Value: value, Path: "/", Expires: expiresAt,
		MaxAge: int(expiresAt.Sub(s.clock.Now()).Seconds()), HttpOnly: true, Secure: s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Service) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName, Value: "", Path: "/", Expires: time.Unix(1, 0),
		MaxAge: -1, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Service) validOrigin(r *http.Request) bool {
	return r.Header.Get("Origin") != "" && r.Header.Get("Origin") == s.origin
}

func normalizeOrigin(parsed *url.URL) string {
	scheme := strings.ToLower(parsed.Scheme)
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	}
	return scheme + "://" + host
}

type requestInfo struct {
	ipAddress string
	userAgent string
}

func requestMetadata(r *http.Request) requestInfo {
	address, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		address = ""
	}
	return requestInfo{ipAddress: address, userAgent: r.UserAgent()}
}

func newSessionToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func validEmail(value string) bool {
	return len(value) <= 254 && emailPattern.MatchString(value)
}

func validPasswordLength(value string) bool {
	length := len(utf16.Encode([]rune(value)))
	return length >= passwordMinimum && length <= passwordMaximum
}

func sessionResponse(user authUser, expiresAt time.Time) transport.SessionResponse {
	return transport.SessionResponse{
		ExpiresAt: expiresAt,
		User:      transport.SessionUser{Id: user.ID, Name: user.Name, Email: openapi_types.Email(user.Email)},
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func signToken(secret, token string) string {
	return base64.StdEncoding.EncodeToString(hmacSHA256([]byte(secret), []byte(token)))
}

func hmacMatches(secret, token string, signature []byte) bool {
	return hmacEqual(hmacSHA256([]byte(secret), []byte(token)), signature)
}

func hmacSHA256(secret, value []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}

func hmacEqual(expected, actual []byte) bool { return hmac.Equal(expected, actual) }

const dummyPasswordHash = "00112233445566778899aabbccddeeff:e0a6a687d13142f638cee5bf0502f2fe9dcb1d891de34b869bb7ae70d0613cfa544d87e96a1bb1d32d24c4a085eafb6e55a0e6b9dd9e37e312f4d29cf07c2a9c"
