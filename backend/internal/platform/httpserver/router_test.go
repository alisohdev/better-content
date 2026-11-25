package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"bettercontent/backend/internal/transport"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

type unavailableAuth struct{ transport.ServerInterface }

func (unavailableAuth) GetSession(w http.ResponseWriter, _ *http.Request) {
	transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
}
func (unavailableAuth) SignIn(w http.ResponseWriter, _ *http.Request) {
	transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
}
func (unavailableAuth) SignOut(w http.ResponseWriter, _ *http.Request) {
	transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
}
func (unavailableAuth) SignUp(w http.ResponseWriter, _ *http.Request) {
	transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
}
func (unavailableAuth) GetCurrentContentDna(w http.ResponseWriter, _ *http.Request, _ transport.WorkspaceId) {
	transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
}
func (unavailableAuth) SaveContentDna(w http.ResponseWriter, _ *http.Request, _ transport.WorkspaceId) {
	transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
}
func (unavailableAuth) ListContentDnaVersions(w http.ResponseWriter, _ *http.Request, _ transport.WorkspaceId) {
	transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
}
func (unavailableAuth) GetContentDnaVersion(w http.ResponseWriter, _ *http.Request, _ transport.WorkspaceId, _ openapi_types.UUID) {
	transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
}

func TestNewHandlerMountsOpenAPIAndHealthRoutes(t *testing.T) {
	handler := NewHandler(fakePinger{}, unavailableAuth{})
	tests := []struct {
		method     string
		path       string
		wantStatus int
	}{
		{method: http.MethodGet, path: "/health/live", wantStatus: http.StatusOK},
		{method: http.MethodGet, path: "/api/v1/auth/session", wantStatus: http.StatusServiceUnavailable},
		{method: http.MethodPost, path: "/api/v1/auth/sign-in", wantStatus: http.StatusServiceUnavailable},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if recorder.Header().Get("X-Request-ID") == "" {
				t.Fatal("response is missing request ID")
			}
			if test.wantStatus == http.StatusServiceUnavailable {
				var response transport.ErrorResponse
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatalf("decode API error response: %v", err)
				}
				if response.Code != transport.SERVICEUNAVAILABLE {
					t.Fatalf("error code = %q, want %q", response.Code, transport.SERVICEUNAVAILABLE)
				}
			}
		})
	}
}

func TestNewHandlerMountsDevelopmentAssetCapabilityRoute(t *testing.T) {
	capabilityHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	handler := NewHandler(fakePinger{}, unavailableAuth{}, capabilityHandler)
	request := httptest.NewRequest(http.MethodPut, "/_dev/assets/staging/capability", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("development capability route status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if recorder.Header().Get("X-Request-ID") == "" {
		t.Fatal("development capability response is missing request ID")
	}
}
