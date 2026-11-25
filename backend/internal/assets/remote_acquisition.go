package assets

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type RemoteAcquisitionLimits struct {
	MaxRedirects       int
	DNSTimeout         time.Duration
	ConnectTimeout     time.Duration
	ResponseTimeout    time.Duration
	IdleTimeout        time.Duration
	TotalTimeout       time.Duration
	MaxResponseHeaders int64
}

var defaultRemoteAcquisitionLimits = RemoteAcquisitionLimits{
	MaxRedirects:       3,
	DNSTimeout:         5 * time.Second,
	ConnectTimeout:     10 * time.Second,
	ResponseTimeout:    15 * time.Second,
	IdleTimeout:        30 * time.Second,
	TotalTimeout:       5 * time.Minute,
	MaxResponseHeaders: 16 * 1024,
}

type RemoteAcquisitionError struct {
	FailureCode string
	Retryable   bool
}

func (e *RemoteAcquisitionError) Error() string { return e.FailureCode }

type remoteAddressResolver interface {
	Resolve(context.Context, string) ([]netip.Addr, error)
}

type remoteHTTPRequester interface {
	Request(context.Context, *url.URL, netip.Addr) (*http.Response, error)
}

type systemRemoteAddressResolver struct{}

func (systemRemoteAddressResolver) Resolve(ctx context.Context, hostname string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", hostname)
}

type pinnedRemoteHTTPRequester struct{ limits RemoteAcquisitionLimits }

func (requester pinnedRemoteHTTPRequester) Request(ctx context.Context, source *url.URL, address netip.Addr) (*http.Response, error) {
	servername := source.Hostname()
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: requester.limits.ConnectTimeout}
			return dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.String(), "443"))
		},
		TLSClientConfig: &tls.Config{
			ServerName: servername,
			MinVersion: tls.VersionTLS12,
		},
		TLSHandshakeTimeout:    requester.limits.ConnectTimeout,
		ResponseHeaderTimeout:  requester.limits.ResponseTimeout,
		MaxResponseHeaderBytes: requester.limits.MaxResponseHeaders,
		DisableKeepAlives:      true,
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.String(), nil)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	request.Header.Set("Accept", "image/*,video/*,audio/*")
	response, err := client.Do(request)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	return response, nil
}

type controlledRemoteAcquirer struct {
	resolver  remoteAddressResolver
	requester remoteHTTPRequester
	limits    RemoteAcquisitionLimits
}

func newControlledRemoteAcquirer(resolver remoteAddressResolver, requester remoteHTTPRequester, limits RemoteAcquisitionLimits) controlledRemoteAcquirer {
	if resolver == nil {
		resolver = systemRemoteAddressResolver{}
	}
	if limits == (RemoteAcquisitionLimits{}) {
		limits = defaultRemoteAcquisitionLimits
	}
	if requester == nil {
		requester = pinnedRemoteHTTPRequester{limits: limits}
	}
	return controlledRemoteAcquirer{resolver: resolver, requester: requester, limits: limits}
}

func (acquirer controlledRemoteAcquirer) Acquire(ctx context.Context, sourceURL, mediaType string) (io.ReadCloser, error) {
	limit, validMedia := mediaByteLimit(mediaType)
	if !validMedia {
		return nil, ErrExternalURLInvalid
	}
	totalCtx, cancel := context.WithTimeout(ctx, acquirer.limits.TotalTimeout)
	fail := func(code string, retryable bool) (io.ReadCloser, error) {
		cancel()
		return nil, &RemoteAcquisitionError{FailureCode: code, Retryable: retryable}
	}
	normalized, err := normalizeExternalMediaURL(sourceURL)
	if err != nil {
		return fail("UNSAFE_MEDIA_URL", false)
	}
	current, _ := url.Parse(normalized.sourceURL)
	visited := make(map[string]struct{}, acquirer.limits.MaxRedirects+1)
	for redirects := 0; ; {
		if err := totalCtx.Err(); err != nil {
			return fail("MEDIA_SOURCE_UNAVAILABLE", true)
		}
		currentNormalized, err := normalizeExternalMediaURL(current.String())
		if err != nil {
			return fail("UNSAFE_MEDIA_URL", false)
		}
		current, _ = url.Parse(currentNormalized.sourceURL)
		if _, exists := visited[current.String()]; exists {
			return fail("UNSAFE_MEDIA_URL", false)
		}
		visited[current.String()] = struct{}{}
		addresses, err := acquirer.resolve(totalCtx, currentNormalized.sourceHost)
		if err != nil {
			return fail("MEDIA_SOURCE_UNAVAILABLE", true)
		}
		if len(addresses) == 0 {
			return fail("UNSAFE_MEDIA_URL", false)
		}
		for _, address := range addresses {
			if isProhibitedRemoteAddress(address.String()) {
				return fail("UNSAFE_MEDIA_URL", false)
			}
		}
		response, err := acquirer.requester.Request(totalCtx, current, addresses[0])
		if err != nil {
			return fail("MEDIA_SOURCE_UNAVAILABLE", true)
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			location := response.Header.Get("Location")
			_ = response.Body.Close()
			if location == "" || redirects >= acquirer.limits.MaxRedirects {
				return fail("UNSAFE_MEDIA_URL", false)
			}
			reference, err := url.Parse(location)
			if err != nil {
				return fail("UNSAFE_MEDIA_URL", false)
			}
			current = current.ResolveReference(reference)
			redirects++
			continue
		}
		if response.StatusCode >= 500 {
			_ = response.Body.Close()
			return fail("MEDIA_SOURCE_UNAVAILABLE", true)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			_ = response.Body.Close()
			return fail("MEDIA_SOURCE_UNAVAILABLE", false)
		}
		if response.ContentLength > limit {
			_ = response.Body.Close()
			return fail("MEDIA_TOO_LARGE", false)
		}
		contentType := strings.ToLower(response.Header.Get("Content-Type"))
		if strings.HasPrefix(contentType, "text/html") || strings.HasPrefix(contentType, "text/plain") || strings.HasPrefix(contentType, "application/json") {
			_ = response.Body.Close()
			return fail("UNSUPPORTED_MEDIA", false)
		}
		return &boundedRemoteResponseBody{body: response.Body, cancel: cancel, maximum: limit, idleTimeout: acquirer.limits.IdleTimeout}, nil
	}
}

func (acquirer controlledRemoteAcquirer) resolve(ctx context.Context, hostname string) ([]netip.Addr, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, acquirer.limits.DNSTimeout)
	defer cancel()
	return acquirer.resolver.Resolve(lookupCtx, hostname)
}

type boundedRemoteResponseBody struct {
	body        io.ReadCloser
	cancel      context.CancelFunc
	maximum     int64
	read        int64
	idleTimeout time.Duration
	closed      bool
	done        bool
}

func (body *boundedRemoteResponseBody) Read(buffer []byte) (int, error) {
	if body.closed || body.done {
		return 0, io.EOF
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if body.read >= body.maximum {
		var probe [1]byte
		n, err := body.readChunk(probe[:])
		if n > 0 {
			return 0, &RemoteAcquisitionError{FailureCode: "MEDIA_TOO_LARGE", Retryable: false}
		}
		if errors.Is(err, io.EOF) {
			body.done = true
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		return 0, nil
	}
	if remaining := body.maximum - body.read; int64(len(buffer)) > remaining {
		buffer = buffer[:remaining]
	}
	n, err := body.readChunk(buffer)
	body.read += int64(n)
	if err != nil {
		if errors.Is(err, io.EOF) {
			body.done = true
			if body.read == 0 {
				return 0, &RemoteAcquisitionError{FailureCode: "UNSUPPORTED_MEDIA", Retryable: false}
			}
			return n, io.EOF
		}
		return n, err
	}
	return n, nil
}

func (body *boundedRemoteResponseBody) readChunk(buffer []byte) (int, error) {
	timer := time.AfterFunc(body.idleTimeout, body.cancel)
	n, err := body.body.Read(buffer)
	if !timer.Stop() && err == nil {
		return n, &RemoteAcquisitionError{FailureCode: "MEDIA_SOURCE_UNAVAILABLE", Retryable: true}
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return n, &RemoteAcquisitionError{FailureCode: "MEDIA_SOURCE_UNAVAILABLE", Retryable: true}
	}
	return n, err
}

func (body *boundedRemoteResponseBody) Close() error {
	if body.closed {
		return nil
	}
	body.closed = true
	body.cancel()
	return body.body.Close()
}

func mediaByteLimit(mediaType string) (int64, bool) {
	media, err := parseMediaType(mediaType)
	if err != nil {
		return 0, false
	}
	return media.maxUploadBytes(), true
}
