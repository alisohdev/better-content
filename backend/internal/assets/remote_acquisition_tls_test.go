package assets

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// This exercises the production pinned requester over an actual TLS socket.
// The CI job runs this test with permission to bind 127.0.0.1:443, which is
// the fixed HTTPS port used by the production transport.
func TestPinnedRemoteHTTPRequesterUsesPinnedIPAndVerifiesTLSHost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the pinned transport uses port 443; the regression runs in Linux CI")
	}
	if os.Getenv("RUN_PRIVILEGED_TLS_TEST") != "1" {
		t.Skip("run with RUN_PRIVILEGED_TLS_TEST=1 to bind the production HTTPS port")
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.WriteString(w, "tls-media")
	}))
	defer server.Close()
	_ = server.Listener.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:443")
	if err != nil {
		t.Fatalf("listen on production HTTPS port: %v", err)
	}
	server.Listener = listener
	server.StartTLS()

	cert := server.Certificate()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if _, err := x509.ParseCertificate(cert.Raw); err != nil {
		t.Fatalf("parse test certificate: %v", err)
	}
	certPath := filepath.Join(t.TempDir(), "test-ca.pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write test CA: %v", err)
	}
	t.Setenv("SSL_CERT_FILE", certPath)

	if len(cert.DNSNames) == 0 {
		t.Fatal("test TLS certificate has no DNS subject")
	}
	source, err := url.Parse("https://" + cert.DNSNames[0] + "/media")
	if err != nil {
		t.Fatal(err)
	}
	response, err := (pinnedRemoteHTTPRequester{limits: defaultRemoteAcquisitionLimits}).Request(
		context.Background(), source, netip.MustParseAddr("127.0.0.1"),
	)
	if err != nil {
		t.Fatalf("pinned HTTPS request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "tls-media" {
		t.Fatalf("TLS response = status %d, body %q, err %v", response.StatusCode, body, err)
	}
}
