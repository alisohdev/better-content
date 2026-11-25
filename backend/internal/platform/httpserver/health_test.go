package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) PingContext(context.Context) error { return f.err }

func TestHealthEndpoints(t *testing.T) {
	server := httptest.NewServer(WithRequestID(NewHealthHandler(fakePinger{})))
	t.Cleanup(server.Close)

	for _, test := range []struct {
		path       string
		wantStatus int
	}{
		{path: "/health/live", wantStatus: http.StatusOK},
		{path: "/health/ready", wantStatus: http.StatusOK},
	} {
		response, err := http.Get(server.URL + test.path)
		if err != nil {
			t.Fatalf("GET %s failed: %v", test.path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != test.wantStatus {
			t.Errorf("GET %s status = %d, want %d", test.path, response.StatusCode, test.wantStatus)
		}
		if response.Header.Get("X-Request-ID") == "" {
			t.Errorf("GET %s did not return a request ID", test.path)
		}
	}
}

func TestReadyHealthHidesDatabaseError(t *testing.T) {
	server := httptest.NewServer(NewHealthHandler(fakePinger{err: errors.New("sensitive connection detail")}))
	t.Cleanup(server.Close)

	response, err := http.Get(server.URL + "/health/ready")
	if err != nil {
		t.Fatalf("GET readiness failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d, want %d", response.StatusCode, http.StatusServiceUnavailable)
	}
}
