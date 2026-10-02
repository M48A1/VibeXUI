package kernel

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body []byte) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}
}
func archive(t *testing.T, name string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("binary data"))
	z.Close()
	return b.Bytes()
}
func TestDownloadVerifiesAndExtractsOnlyXray(t *testing.T) {
	raw := archive(t, "xray")
	h := sha256.Sum256(raw)
	digest := hex.EncodeToString(h[:])
	c := NewCatalog()
	c.Client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" {
			t.Fatal("not HTTPS")
		}
		if r.URL.Host == "api.github.com" {
			return response([]byte(fmt.Sprintf(`{"tag_name":"v26.3.27","assets":[{"name":"Xray-linux-64.zip","digest":"sha256:%s"}]}`, digest))), nil
		}
		if r.URL.String() != "https://github.com/XTLS/Xray-core/releases/download/v26.3.27/Xray-linux-64.zip" {
			t.Fatal("unexpected download", r.URL)
		}
		return response(raw), nil
	})
	path, err := c.Download(context.Background(), "v26.3.27", "amd64", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm()&0100 == 0 {
		t.Fatal("binary not executable")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "binary data" {
		t.Fatal("wrong data")
	}
	digest = strings.Repeat("0", 64)
	if _, err = c.Download(context.Background(), "v26.3.27", "amd64", t.TempDir()); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
}
func TestLegacyDigestAndUnsafeArchiveRejected(t *testing.T) {
	raw := archive(t, "../xray")
	h := sha256.Sum256(raw)
	c := NewCatalog()
	c.Client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Host == "api.github.com":
			return response([]byte(`{"tag_name":"v1.8.24","assets":[{"name":"Xray-linux-arm64-v8a.zip"},{"name":"Xray-linux-arm64-v8a.zip.dgst"}]}`)), nil
		case strings.HasSuffix(r.URL.Path, ".dgst"):
			return response([]byte(fmt.Sprintf("SHA2-256= %x\n", h))), nil
		default:
			return response(raw), nil
		}
	})
	if err := c.Check(context.Background(), "v1.8.24", "arm64"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Download(context.Background(), "v1.8.24", "arm64", t.TempDir()); err == nil {
		t.Fatal("archive traversal accepted")
	}
	for _, v := range []string{"../bad", "latest", "v26.3.27;sh", "https://bad"} {
		if c.Check(context.Background(), v, "amd64") == nil {
			t.Fatal("invalid version", v)
		}
	}
	if c.Check(context.Background(), "v1.8.24", "unsupported") == nil {
		t.Fatal("unsupported architecture accepted")
	}
}
func TestReleaseListFiltersDraftsAndCaches(t *testing.T) {
	c := NewCatalog()
	calls := 0
	c.Client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		calls++
		return response([]byte(`[{"tag_name":"v26.3.27"},{"tag_name":"v26.9.30","prerelease":true},{"tag_name":"v1.0.0","draft":true},{"tag_name":"invalid"}]`)), nil
	})
	for i := 0; i < 2; i++ {
		releases, err := c.List(context.Background())
		if err != nil || len(releases) != 2 || !releases[1].Prerelease {
			t.Fatal(releases, err)
		}
	}
	if calls != 1 {
		t.Fatal("cache unused")
	}
}
