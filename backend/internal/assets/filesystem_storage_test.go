package assets

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFilesystemStorageUsesPrivateOpaqueKeysAndPublishesImmutableObjects(t *testing.T) {
	t.Parallel()

	storage, err := newFilesystemStorage(t.TempDir(), "http://localhost:3000", []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.close() })
	ctx := context.Background()
	staging, err := newStagingKey()
	if err != nil {
		t.Fatal(err)
	}
	permanent, err := newPermanentKey()
	if err != nil {
		t.Fatal(err)
	}

	if err := storage.putStagingFromStream(ctx, staging, strings.NewReader("quarantined")); err != nil {
		t.Fatal(err)
	}
	reader, err := storage.openStagingRead(ctx, staging)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(bytes) != "quarantined" {
		t.Fatalf("staging read = %q, %v", bytes, err)
	}

	if err := storage.copyStagingToPermanent(ctx, staging, permanent); err != nil {
		t.Fatal(err)
	}
	if err := storage.putPermanentFromStream(ctx, permanent, strings.NewReader("replacement")); !errors.Is(err, errStorageObjectExists) {
		t.Fatal(err)
	}
	reader, err = storage.openPermanentRead(ctx, permanent)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err = io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(bytes) != "quarantined" {
		t.Fatalf("permanent object was replaced: %q, %v", bytes, err)
	}

	metadata, found, err := storage.getObjectMetadata(ctx, permanent.String())
	if err != nil || !found || metadata.sizeBytes != int64(len("quarantined")) {
		t.Fatalf("metadata = %#v, found=%v, err=%v", metadata, found, err)
	}
	page, err := storage.listManagedObjects(ctx, permanentNamespace, "", 1)
	if err != nil || len(page.objects) != 1 || page.objects[0].key != permanent.String() {
		t.Fatalf("managed objects = %#v, err=%v", page, err)
	}

	if err := storage.deletePermanentObject(ctx, permanent); err != nil {
		t.Fatal(err)
	}
	if err := storage.deletePermanentObject(ctx, permanent); err != nil {
		t.Fatalf("delete should be idempotent: %v", err)
	}
	if _, err := storage.openPermanentRead(ctx, permanent); !errors.Is(err, errStorageNotFound) {
		t.Fatalf("missing read error = %v, want not found", err)
	}
}

func TestFilesystemStorageCapabilitiesAreSignedAndBoundToRequest(t *testing.T) {
	t.Parallel()

	storage, err := newFilesystemStorage(t.TempDir(), "http://localhost:3000", []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.close() })
	storage.clock = func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	staging, _ := newStagingKey()
	permanent, _ := newPermanentKey()
	expires := storage.clock().Add(time.Hour)
	putURL, err := storage.createStagingPutCapability(staging, expires, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.createStagingPutCapability(staging, storage.clock().Add(maxFilesystemCapabilityLifetime+time.Second), 64); !errors.Is(err, errStorageInvalidKey) {
		t.Fatalf("overlong staging capability error = %v, want invalid key", err)
	}
	parsedPut, err := url.Parse(putURL)
	if err != nil {
		t.Fatal(err)
	}
	putQuery := parsedPut.Query()
	putFields := []string{"PUT", staging.String(), putQuery.Get("expires"), putQuery.Get("size")}
	if !storage.verifyCapability(putQuery.Get("signature"), putFields...) {
		t.Fatal("staging capability signature did not verify")
	}
	if storage.verifyCapability(putQuery.Get("signature"), "PUT", staging.String(), putQuery.Get("expires"), "65") {
		t.Fatal("staging signature was not bound to content length")
	}

	if err := storage.putPermanentFromStream(ctx, permanent, strings.NewReader("private")); err != nil {
		t.Fatal(err)
	}
	readURL, err := storage.createPrivateReadCapability(ctx, permanent, expires, privateReadOptions{contentType: "image/png", contentDisposition: "inline", rangeAllowed: true})
	if err != nil {
		t.Fatal(err)
	}
	parsedRead, err := url.Parse(readURL)
	if err != nil {
		t.Fatal(err)
	}
	readQuery := parsedRead.Query()
	readFields := []string{"GET", permanent.String(), readQuery.Get("expires"), readQuery.Get("type"), readQuery.Get("disposition"), readQuery.Get("range")}
	if !storage.verifyCapability(readQuery.Get("signature"), readFields...) {
		t.Fatal("private-read capability signature did not verify")
	}
	if storage.verifyCapability(readQuery.Get("signature"), "GET", permanent.String(), readQuery.Get("expires"), "text/plain", readQuery.Get("disposition"), readQuery.Get("range")) {
		t.Fatal("read signature was not bound to content type")
	}
}

func TestFilesystemStorageRejectsSymlinkEscapes(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	rootDir := filepath.Join(base, "private")
	outsideDir := filepath.Join(base, "outside")
	if err := os.Mkdir(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "secret"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(rootDir, "staging")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	storage, err := newFilesystemStorage(rootDir, "http://localhost:3000", []byte(strings.Repeat("z", 32)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.close() })
	key, _ := newStagingKey()
	err = storage.putStagingFromStream(context.Background(), key, strings.NewReader("should fail"))
	if err == nil {
		t.Fatal("write through namespace symlink succeeded")
	}
	if contents, readErr := os.ReadFile(filepath.Join(outsideDir, "secret")); readErr != nil || string(contents) != "outside" {
		t.Fatalf("outside file changed: %q, %v", contents, readErr)
	}
}
