package main

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A server that offers HTTP/2 (most big sites, the product pictures' among them): files are
// downloaded from it over HTTP/1.1 (several connections, for a download in parts).
func TestDownloadTransportWithHTTP2Server(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello "+r.Proto)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	tr := downloadTransport.Clone()
	cfg := tr.TLSClientConfig.Clone()
	cfg.InsecureSkipVerify = true // the test server's own certificate
	tr.TLSClientConfig = cfg
	resp, err := (&http.Client{Transport: tr}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "HTTP/1.1") {
		t.Errorf("answer %q", b)
	}
	_ = tls.VersionTLS12
}
