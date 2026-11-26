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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3StorageOperationsAndPresignedCapabilities(t *testing.T) {
	var requests []http.Request
	var sawContinuationToken bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		copyRequest := *r
		requests = append(requests, copyRequest)
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" && r.URL.Query().Get("continuation-token") == "next-token":
			sawContinuationToken = true
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>false</IsTruncated></ListBucketResult>`)
		case r.Method == http.MethodHead:
			w.Header().Set("Content-Length", "12")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>true</IsTruncated><NextContinuationToken>next-token</NextContinuationToken><Contents><Key>staging/550e8400-e29b-41d4-a716-446655440000</Key><Size>12</Size><LastModified>2026-10-25T00:00:00Z</LastModified></Contents></ListBucketResult>`)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, "private object")
		case r.Method == http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected operation", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client := s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test-access", "test-secret", "")}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(server.URL)
		options.UsePathStyle = true
	})
	storage := &s3Storage{client: client, presign: s3.NewPresignClient(client), bucket: "media"}
	ctx := context.Background()
	staging, err := parseStagingKey("staging/550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	permanent, err := parsePermanentKey("permanent/550e8400-e29b-41d4-a716-446655440001")
	if err != nil {
		t.Fatal(err)
	}

	page, err := storage.listManagedObjects(ctx, stagingNamespace, "", 10)
	if err != nil || len(page.objects) != 1 || page.nextCursor != "next-token" || page.objects[0].sizeBytes != 12 {
		t.Fatalf("list returned page=%#v err=%v", page, err)
	}
	if _, err := storage.listManagedObjects(ctx, stagingNamespace, page.nextCursor, 10); err != nil || !sawContinuationToken {
		t.Fatalf("list did not pass through S3 continuation token: err=%v seen=%v", err, sawContinuationToken)
	}
	metadata, found, err := storage.getObjectMetadata(ctx, staging.String())
	if err != nil || !found || metadata.sizeBytes != 12 {
		t.Fatalf("metadata=%#v found=%v err=%v", metadata, found, err)
	}
	reader, err := storage.openStagingRead(ctx, staging)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(body) != "private object" {
		t.Fatalf("read body=%q readErr=%v closeErr=%v", body, readErr, closeErr)
	}
	if err := storage.putStagingFromStream(ctx, staging, strings.NewReader("staged")); err != nil {
		t.Fatal(err)
	}
	if err := storage.putPermanentFromStream(ctx, permanent, strings.NewReader("permanent")); err != nil {
		t.Fatal(err)
	}
	if err := storage.deleteStagingObject(ctx, staging); err != nil {
		t.Fatal(err)
	}
	if err := storage.deletePermanentObject(ctx, permanent); err != nil {
		t.Fatal(err)
	}

	putURL, err := storage.createStagingPutCapability(staging, time.Now().Add(time.Hour), 21)
	if err != nil {
		t.Fatal(err)
	}
	parsedPut, err := url.Parse(putURL)
	if err != nil || parsedPut.Query().Get("X-Amz-Signature") == "" || !strings.Contains(parsedPut.Query().Get("X-Amz-SignedHeaders"), "content-length") {
		t.Fatalf("PUT capability is not signed for content length: %q err=%v", putURL, err)
	}
	readURL, err := storage.createPrivateReadCapability(ctx, permanent, time.Now().Add(time.Hour), privateReadOptions{contentType: "image/png", contentDisposition: `inline; filename="asset.png"`})
	if err != nil {
		t.Fatal(err)
	}
	parsedRead, err := url.Parse(readURL)
	if err != nil || parsedRead.Query().Get("X-Amz-Signature") == "" || parsedRead.Query().Get("response-content-type") != "image/png" || parsedRead.Query().Get("response-cache-control") != "private, no-store" {
		t.Fatalf("GET capability has missing signed response controls: %q err=%v", readURL, err)
	}

	var sawConditionalPut bool
	for _, request := range requests {
		if request.Method != http.MethodPut {
			continue
		}
		if strings.HasPrefix(request.URL.Path, "/media/permanent/") {
			sawConditionalPut = request.Header.Get("If-None-Match") == "*"
		}
	}
	if !sawConditionalPut {
		t.Fatal("immutable PutObject must be conditional")
	}
}
