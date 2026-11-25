package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDecodeJSONRequiresOneBoundedKnownObject(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantErr     bool
	}{
		{name: "valid", contentType: "application/json", body: `{"name":"Ada"}`},
		{name: "unknown field", contentType: "application/json", body: `{"name":"Ada","secret":"do-not-echo"}`, wantErr: true},
		{name: "malformed", contentType: "application/json", body: `{"name":`, wantErr: true},
		{name: "multiple values", contentType: "application/json", body: `{"name":"Ada"}{"name":"Grace"}`, wantErr: true},
		{name: "wrong type", contentType: "text/plain", body: `{"name":"Ada"}`, wantErr: true},
		{name: "large body", contentType: "application/json", body: `{"name":"` + strings.Repeat("x", maxJSONBodyBytes) + `"}`, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			var decoded struct {
				Name string `json:"name"`
			}
			err := DecodeJSON(request, &decoded)
			if (err != nil) != test.wantErr {
				t.Fatalf("DecodeJSON error = %v, wantErr %v", err, test.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "do-not-echo") {
				t.Fatalf("decoder error leaked request content: %v", err)
			}
		})
	}
}

func TestHandleDecodeErrorUsesStableContractEnvelope(t *testing.T) {
	recorder := httptest.NewRecorder()
	HandleDecodeError(recorder, errInvalidJSON)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	var response ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Code != VALIDATIONERROR {
		t.Fatalf("error code = %q, want %q", response.Code, VALIDATIONERROR)
	}
}

func TestGeneratedSessionResponseMatchesContractJSON(t *testing.T) {
	encoded, err := json.Marshal(SessionResponse{
		ExpiresAt: time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC),
		User:      SessionUser{Id: "user_1", Name: "Ada", Email: "ada@example.com"},
	})
	if err != nil {
		t.Fatalf("marshal generated session response: %v", err)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatalf("decode generated response: %v", err)
	}
	for _, property := range []string{"expiresAt", "user"} {
		if _, ok := shape[property]; !ok {
			t.Errorf("generated response omitted contract property %q: %s", property, encoded)
		}
	}
	if len(shape) != 2 {
		t.Fatalf("generated response has unexpected properties: %s", encoded)
	}
}
