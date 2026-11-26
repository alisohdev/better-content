package assets

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestS3AssetStorageLiveContract(t *testing.T) {
	if os.Getenv("ASSET_STORAGE_LIVE_CONTRACT") != "1" {
		t.Skip("S3-compatible storage contract is opt-in")
	}
	if os.Getenv("ASSET_STORAGE_VERSIONING") != "disabled" {
		t.Fatal("use a disposable private bucket with versioning disabled")
	}
	adapter, err := OpenS3AssetStorage(S3StorageConfig{
		Endpoint: os.Getenv("ASSET_S3_ENDPOINT"), Region: os.Getenv("ASSET_S3_REGION"), Bucket: os.Getenv("ASSET_S3_BUCKET"),
		AccessKeyID: os.Getenv("ASSET_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("ASSET_S3_SECRET_ACCESS_KEY"),
		ForcePathStyle: os.Getenv("ASSET_S3_FORCE_PATH_STYLE") == "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	storage := adapter.storage
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	staging, err := newStagingKey()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := newStagingKey()
	if err != nil {
		t.Fatal(err)
	}
	permanent, err := newPermanentKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for _, key := range []stagingKey{staging, snapshot} {
			if err := storage.deleteStagingObject(cleanup, key); err != nil {
				t.Error("could not clean up live staging fixture")
			}
		}
		if err := storage.deletePermanentObject(cleanup, permanent); err != nil {
			t.Error("could not clean up live permanent fixture")
		}
	})
	const data = "private managed media contract"
	expires := time.Now().Add(5 * time.Minute)
	capability, err := storage.createStagingPutCapability(staging, expires, int64(len(data)))
	if err != nil {
		t.Fatal("could not sign staging upload")
	}
	assertStorageHTTP(t, ctx, http.MethodPut, capability, data, "", http.StatusOK, "")
	unsigned, err := url.Parse(capability)
	if err != nil {
		t.Fatal("could not parse staging upload URL")
	}
	unsigned.RawQuery = ""
	assertStorageHTTP(t, ctx, http.MethodGet, unsigned.String(), "", "", http.StatusForbidden, "")

	assertLiveMetadata(t, ctx, storage, staging.String(), int64(len(data)))
	stream, err := storage.openStagingRead(ctx, staging)
	if err != nil {
		t.Fatal("could not open staging")
	}
	content, err := io.ReadAll(stream)
	stream.Close()
	if err != nil || string(content) != data {
		t.Fatal("staging bytes differ")
	}
	if err := storage.putStagingFromStream(ctx, snapshot, strings.NewReader(data)); err != nil {
		t.Fatal("could not write managed staging snapshot")
	}
	assertLiveMetadata(t, ctx, storage, snapshot.String(), int64(len(data)))

	for _, body := range []string{data, "different"} {
		if err := storage.putPermanentFromStream(ctx, permanent, strings.NewReader(body)); err != nil {
			t.Fatal("could not publish permanent object")
		}
	}
	assertLiveMetadata(t, ctx, storage, permanent.String(), int64(len(data)))
	preview, err := storage.createPrivateReadCapability(ctx, permanent, expires, privateReadOptions{
		contentType: "application/octet-stream", contentDisposition: "inline", rangeAllowed: true,
	})
	if err != nil {
		t.Fatal("could not sign permanent read")
	}
	assertStorageHTTP(t, ctx, http.MethodGet, preview, "", "", http.StatusOK, data)
	assertStorageHTTP(t, ctx, http.MethodGet, preview, "", "bytes=0-7", http.StatusPartialContent, data[:8])
	page, err := storage.listManagedObjects(ctx, permanentNamespace, "", 100)
	if err != nil {
		t.Fatal("could not list permanent objects")
	}
	found := false
	for _, object := range page.objects {
		found = found || object.key == permanent.String()
	}
	if !found {
		t.Fatal("permanent object missing from listing")
	}
	for range 2 {
		if err := storage.deletePermanentObject(ctx, permanent); err != nil {
			t.Fatal("permanent deletion was not idempotent")
		}
	}
	_, found, err = storage.getObjectMetadata(ctx, permanent.String())
	if err != nil || found {
		t.Fatal("permanent object remains after deletion")
	}
}

func assertStorageHTTP(t *testing.T, ctx context.Context, method, endpoint, body, readRange string, status int, expected string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		t.Fatal("could not construct storage request")
	}
	if readRange != "" {
		request.Header.Set("Range", readRange)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal("storage HTTP request failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<10))
	if err != nil || response.StatusCode != status || expected != "" && string(data) != expected {
		t.Fatalf("storage HTTP status %d, want %d, or body differed", response.StatusCode, status)
	}
}

func assertLiveMetadata(t *testing.T, ctx context.Context, storage assetStorage, key string, size int64) {
	t.Helper()
	metadata, found, err := storage.getObjectMetadata(ctx, key)
	if err != nil || !found || metadata.sizeBytes != size {
		t.Fatal("managed object has unexpected metadata")
	}
}
