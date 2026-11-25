package assets

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFilesystemCapabilityHandlerRequiresDevelopmentAndEnforcesUploadBytes(t *testing.T) {
	storage, handler := newTestFilesystemCapabilityHandler(t)
	if _, err := newFilesystemCapabilityHandler(storage, false); err == nil {
		t.Fatal("production mode unexpectedly enabled local capability routes")
	}
	key, err := newStagingKey()
	if err != nil {
		t.Fatal(err)
	}
	expires := storage.clock().Add(time.Hour)
	capability, err := storage.createStagingPutCapability(key, expires, int64(len("payload")))
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(capability)
	request := httptest.NewRequest(http.MethodPut, parsed.RequestURI(), strings.NewReader("payload"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("valid PUT status = %d, body=%s", response.Code, response.Body.String())
	}
	reader, err := storage.openStagingRead(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(stored) != "payload" {
		t.Fatalf("stored upload = %q, %v", stored, err)
	}

	secondKey, _ := newStagingKey()
	shortCapability, _ := storage.createStagingPutCapability(secondKey, expires, 8)
	shortURL, _ := url.Parse(shortCapability)
	shortRequest := httptest.NewRequest(http.MethodPut, shortURL.RequestURI(), strings.NewReader("short"))
	shortRequest.ContentLength = 8
	shortResponse := httptest.NewRecorder()
	handler.ServeHTTP(shortResponse, shortRequest)
	if shortResponse.Code != http.StatusBadRequest {
		t.Fatalf("short PUT status = %d, want 400", shortResponse.Code)
	}
	if _, found, err := storage.getObjectMetadata(context.Background(), secondKey.String()); err != nil || found {
		t.Fatalf("short body was published: found=%v err=%v", found, err)
	}
}

func TestFilesystemCapabilityHandlerServesPrivateReadAndControlsRanges(t *testing.T) {
	storage, handler := newTestFilesystemCapabilityHandler(t)
	key, err := newPermanentKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.putPermanentFromStream(context.Background(), key, strings.NewReader("0123456789")); err != nil {
		t.Fatal(err)
	}
	expires := storage.clock().Add(time.Hour)
	capability, err := storage.createPrivateReadCapability(context.Background(), key, expires, privateReadOptions{contentType: "image/png", contentDisposition: "inline", rangeAllowed: true})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(capability)
	request := httptest.NewRequest(http.MethodGet, parsed.RequestURI(), nil)
	request.Header.Set("Range", "bytes=2-4")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "234" {
		t.Fatalf("range response = %d %q", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("private response headers missing: %#v", response.Header())
	}

	noRangeURL, err := storage.createPrivateReadCapability(context.Background(), key, expires, privateReadOptions{contentType: "image/png", contentDisposition: "inline", rangeAllowed: false})
	if err != nil {
		t.Fatal(err)
	}
	parsedNoRange, _ := url.Parse(noRangeURL)
	noRangeRequest := httptest.NewRequest(http.MethodGet, parsedNoRange.RequestURI(), nil)
	noRangeRequest.Header.Set("Range", "bytes=2-4")
	noRangeResponse := httptest.NewRecorder()
	handler.ServeHTTP(noRangeResponse, noRangeRequest)
	if noRangeResponse.Code != http.StatusOK || noRangeResponse.Body.String() != "0123456789" {
		t.Fatalf("range-disabled response = %d %q", noRangeResponse.Code, noRangeResponse.Body.String())
	}

	tamperedQuery := parsed.Query()
	tamperedQuery.Set("type", "text/html")
	parsed.RawQuery = tamperedQuery.Encode()
	tamperedResponse := httptest.NewRecorder()
	handler.ServeHTTP(tamperedResponse, httptest.NewRequest(http.MethodGet, parsed.RequestURI(), nil))
	if tamperedResponse.Code != http.StatusForbidden {
		t.Fatalf("tampered read status = %d, want 403", tamperedResponse.Code)
	}
}

func newTestFilesystemCapabilityHandler(t *testing.T) (*filesystemStorage, *filesystemCapabilityHandler) {
	t.Helper()
	storage, err := newFilesystemStorage(t.TempDir(), "http://localhost:3000", []byte(strings.Repeat("t", 32)))
	if err != nil {
		t.Fatal(err)
	}
	storage.clock = func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { _ = storage.close() })
	handler, err := newFilesystemCapabilityHandler(storage, true)
	if err != nil {
		t.Fatal(err)
	}
	return storage, handler
}
