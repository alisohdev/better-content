package httpserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"bettercontent/backend/internal/platform/httpserver"
	"bettercontent/backend/internal/platform/postgres"
)

func TestPostgresReadinessWithDedicatedTestDatabase(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || strings.TrimPrefix(parsed.Path, "/") == "" || !strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") {
		t.Fatal("TEST_DATABASE_URL must point to a database whose name ends in _test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	database, err := postgres.Open(ctx, databaseURL)
	cancel()
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer database.Close()

	server := httptest.NewServer(httpserver.NewHealthHandler(database))
	defer server.Close()

	requestCtx, cancelRequest := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRequest()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, server.URL+"/health/ready", nil)
	if err != nil {
		t.Fatalf("create readiness request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("readiness request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("readiness status = %d, want %d", response.StatusCode, http.StatusOK)
	}
}
