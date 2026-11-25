package assets

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

type remoteResolverFunc func(context.Context, string) ([]netip.Addr, error)

func (resolve remoteResolverFunc) Resolve(ctx context.Context, hostname string) ([]netip.Addr, error) {
	return resolve(ctx, hostname)
}

type remoteRequesterFunc func(context.Context, string, netip.Addr) (*http.Response, error)

func (request remoteRequesterFunc) Request(ctx context.Context, url *url.URL, address netip.Addr) (*http.Response, error) {
	return request(ctx, url.String(), address)
}

func TestControlledRemoteAcquirerPinsPublicDNSAnswersAndRechecksRedirects(t *testing.T) {
	var resolved []string
	var requests []string
	resolver := remoteResolverFunc(func(_ context.Context, hostname string) ([]netip.Addr, error) {
		resolved = append(resolved, hostname)
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	})
	requester := remoteRequesterFunc(func(_ context.Context, source string, address netip.Addr) (*http.Response, error) {
		requests = append(requests, source+"@"+address.String())
		if strings.HasSuffix(source, "/start") {
			return fakeRemoteResponse(http.StatusFound, "", "/media", ""), nil
		}
		return fakeRemoteResponse(http.StatusOK, "", "", "media"), nil
	})
	acquirer := newControlledRemoteAcquirer(resolver, requester, RemoteAcquisitionLimits{MaxRedirects: 3, DNSTimeout: time.Second, TotalTimeout: time.Second, IdleTimeout: time.Second})
	body, err := acquirer.Acquire(context.Background(), "https://media.example/start", "IMAGE")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "media" {
		t.Fatalf("acquired body = %q, err=%v", data, err)
	}
	if len(resolved) != 2 || resolved[0] != "media.example" || resolved[1] != "media.example" || len(requests) != 2 || !strings.Contains(requests[0], "@8.8.8.8") || !strings.Contains(requests[1], "@8.8.8.8") {
		t.Fatalf("DNS resolutions = %v, pinned requests = %v", resolved, requests)
	}
}

func TestControlledRemoteAcquirerRejectsMixedPublicAndPrivateDNSAnswers(t *testing.T) {
	called := false
	acquirer := newControlledRemoteAcquirer(
		remoteResolverFunc(func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.2")}, nil
		}),
		remoteRequesterFunc(func(context.Context, string, netip.Addr) (*http.Response, error) {
			called = true
			return fakeRemoteResponse(http.StatusOK, "", "", "media"), nil
		}),
		RemoteAcquisitionLimits{MaxRedirects: 3, DNSTimeout: time.Second, TotalTimeout: time.Second, IdleTimeout: time.Second},
	)
	_, err := acquirer.Acquire(context.Background(), "https://media.example/media", "IMAGE")
	assertRemoteAcquisitionError(t, err, "UNSAFE_MEDIA_URL", false)
	if called {
		t.Fatal("requester was called with a mixed public/private DNS answer set")
	}
}

func TestControlledRemoteAcquirerBoundsDNSAndTotalRequestTime(t *testing.T) {
	limits := RemoteAcquisitionLimits{MaxRedirects: 3, DNSTimeout: 10 * time.Millisecond, TotalTimeout: time.Second, IdleTimeout: time.Second}
	dnsTimeout := newControlledRemoteAcquirer(
		remoteResolverFunc(func(ctx context.Context, _ string) ([]netip.Addr, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
		remoteRequesterFunc(func(context.Context, string, netip.Addr) (*http.Response, error) {
			t.Fatal("requester called after DNS timeout")
			return nil, nil
		}),
		limits,
	)
	_, err := dnsTimeout.Acquire(context.Background(), "https://media.example/media", "IMAGE")
	assertRemoteAcquisitionError(t, err, "MEDIA_SOURCE_UNAVAILABLE", true)

	limits.DNSTimeout = time.Second
	limits.TotalTimeout = 10 * time.Millisecond
	totalTimeout := newControlledRemoteAcquirer(
		remoteResolverFunc(func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}),
		remoteRequesterFunc(func(ctx context.Context, _ string, _ netip.Addr) (*http.Response, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
		limits,
	)
	_, err = totalTimeout.Acquire(context.Background(), "https://media.example/media", "IMAGE")
	assertRemoteAcquisitionError(t, err, "MEDIA_SOURCE_UNAVAILABLE", true)
}

func TestControlledRemoteAcquirerRejectsUnsafeRedirectAndExcessRedirects(t *testing.T) {
	publicResolver := remoteResolverFunc(func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	})
	unsafeRedirect := newControlledRemoteAcquirer(publicResolver, remoteRequesterFunc(func(context.Context, string, netip.Addr) (*http.Response, error) {
		return fakeRemoteResponse(http.StatusFound, "", "http://example.com/media", ""), nil
	}), RemoteAcquisitionLimits{MaxRedirects: 3, DNSTimeout: time.Second, TotalTimeout: time.Second, IdleTimeout: time.Second})
	_, err := unsafeRedirect.Acquire(context.Background(), "https://example.com/start", "IMAGE")
	assertRemoteAcquisitionError(t, err, "UNSAFE_MEDIA_URL", false)

	requestCount := 0
	tooMany := newControlledRemoteAcquirer(publicResolver, remoteRequesterFunc(func(_ context.Context, source string, _ netip.Addr) (*http.Response, error) {
		requestCount++
		return fakeRemoteResponse(http.StatusFound, "", "/hop-"+strconv.Itoa(requestCount), ""), nil
	}), RemoteAcquisitionLimits{MaxRedirects: 3, DNSTimeout: time.Second, TotalTimeout: time.Second, IdleTimeout: time.Second})
	_, err = tooMany.Acquire(context.Background(), "https://example.com/start", "IMAGE")
	assertRemoteAcquisitionError(t, err, "UNSAFE_MEDIA_URL", false)
	if requestCount != 4 {
		t.Fatalf("redirect request count = %d, want 4", requestCount)
	}
}

func TestControlledRemoteAcquirerNormalizesHTTPFailuresAndMediaLimits(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		content   string
		length    int64
		wantCode  string
		wantRetry bool
	}{
		{name: "server failure", status: http.StatusBadGateway, wantCode: "MEDIA_SOURCE_UNAVAILABLE", wantRetry: true},
		{name: "not found", status: http.StatusNotFound, wantCode: "MEDIA_SOURCE_UNAVAILABLE"},
		{name: "html response", status: http.StatusOK, content: "text/html; charset=utf-8", wantCode: "UNSUPPORTED_MEDIA"},
		{name: "known oversized response", status: http.StatusOK, length: 10*1024*1024 + 1, wantCode: "MEDIA_TOO_LARGE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			acquirer := newControlledRemoteAcquirer(
				remoteResolverFunc(func(context.Context, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
				}),
				remoteRequesterFunc(func(context.Context, string, netip.Addr) (*http.Response, error) {
					response := fakeRemoteResponse(test.status, test.content, "", "media")
					response.ContentLength = test.length
					return response, nil
				}),
				RemoteAcquisitionLimits{MaxRedirects: 3, DNSTimeout: time.Second, TotalTimeout: time.Second, IdleTimeout: time.Second},
			)
			_, err := acquirer.Acquire(context.Background(), "https://media.example/media", "IMAGE")
			assertRemoteAcquisitionError(t, err, test.wantCode, test.wantRetry)
		})
	}
}

func TestBoundedRemoteResponseBodyRejectsChunkOverflowAndEmptyBody(t *testing.T) {
	body := &boundedRemoteResponseBody{body: io.NopCloser(strings.NewReader("123456")), cancel: func() {}, maximum: 5, idleTimeout: time.Second}
	data, err := io.ReadAll(body)
	assertRemoteAcquisitionError(t, err, "MEDIA_TOO_LARGE", false)
	if string(data) != "12345" {
		t.Fatalf("bounded body yielded %q past its maximum", data)
	}

	empty := &boundedRemoteResponseBody{body: io.NopCloser(strings.NewReader("")), cancel: func() {}, maximum: 5, idleTimeout: time.Second}
	_, err = io.ReadAll(empty)
	assertRemoteAcquisitionError(t, err, "UNSUPPORTED_MEDIA", false)
}

func fakeRemoteResponse(status int, contentType, location, body string) *http.Response {
	header := make(http.Header)
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	if location != "" {
		header.Set("Location", location)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), ContentLength: -1}
}

func assertRemoteAcquisitionError(t *testing.T, err error, failureCode string, retryable bool) {
	t.Helper()
	remoteErr, ok := err.(*RemoteAcquisitionError)
	if !ok || remoteErr.FailureCode != failureCode || remoteErr.Retryable != retryable {
		t.Fatalf("remote acquisition error = %#v, want %s retryable=%v", err, failureCode, retryable)
	}
}
