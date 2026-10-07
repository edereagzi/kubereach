package bindings

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/updater"
)

// The release script signs the files and Wails' updater, configured as Kubereach configures it, checks them.
func TestUpdateInstallsOnlyWhatItsKeySigned(t *testing.T) {
	for _, tool := range []string{"sh", "jq", "openssl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool, " is missing")
		}
	}
	if help, _ := exec.Command("openssl", "pkeyutl", "-help").CombinedOutput(); !bytes.Contains(help, []byte("-rawin")) {
		t.Skip("openssl cannot sign with pkeyutl -rawin")
	}

	// Each file holds the one entry the updater swaps for the running app.
	dist := t.TempDir()
	writeZip(t, filepath.Join(dist, "kubereach_darwin_universal.zip"), "Kubereach.app/Contents/MacOS/kubereach")
	writeZip(t, filepath.Join(dist, "kubereach_windows_amd64.zip"), "kubereach.exe")
	for _, arch := range []string{"amd64", "arm64"} {
		if err := os.WriteFile(filepath.Join(dist, "kubereach_linux_"+arch), []byte("kubereach"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	notes := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(notes, []byte("## What's Changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	signing, key := testKey(t)
	script := exec.Command("sh", "../../build/release/update-manifest.sh", "0.4.0", dist, notes, filepath.Join(dist, "update.json"))
	script.Env = append(os.Environ(), "UPDATE_SIGNING_KEY="+signing)
	if out, err := script.CombinedOutput(); err != nil {
		t.Fatalf("update-manifest.sh: %v\n%s", err, out)
	}

	// Every request, GitHub's included, is answered from dist.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(dist, path.Base(r.URL.Path)))
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	defaultTransport := http.DefaultTransport
	http.DefaultTransport = roundTripper(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return defaultTransport.RoundTrip(r)
	})
	t.Cleanup(func() { http.DefaultTransport = defaultTransport })

	installed := map[string]string{"darwin": "Kubereach.app", "windows": "kubereach.exe", "linux": "kubereach_linux_" + runtime.GOARCH}[runtime.GOOS]
	_, otherKey := testKey(t)
	for _, tc := range []struct {
		name, key, err string
	}{
		{"signed with its key", key, ""},
		{"signed with another key", otherKey, "signature did not verify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := updaterConfig("0.0.1", []byte(tc.key))
			if err != nil {
				t.Fatal(err)
			}
			u := updater.New(stubHost{})
			if err := u.Init(cfg); err != nil {
				t.Fatal(err)
			}
			rel, err := u.Check(context.Background())
			if err != nil || rel == nil || rel.Version != "0.4.0" {
				t.Fatalf("Check = %+v, %v; want 0.4.0", rel, err)
			}
			err = u.DownloadAndInstall(context.Background())
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("DownloadAndInstall = %v; want %q", err, tc.err)
				}
				return
			}
			if err != nil || filepath.Base(u.DownloadedPath()) != installed {
				t.Fatalf("DownloadAndInstall = %v, staged %q; want %s", err, u.DownloadedPath(), installed)
			}
		})
	}
}

// testKey is a new Ed25519 key pair as PEM: the private half for the script, the public one for the updater.
func testKey(t *testing.T) (private, public string) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
}

func writeZip(t *testing.T, name, entry string) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("kubereach")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubHost stands in for the app: no window opens and nothing restarts.
type stubHost struct{}

func (stubHost) Emit(string, ...any) bool                              { return true }
func (stubHost) OnEvent(string, func(any)) func()                      { return func() {} }
func (stubHost) OpenWindow(updater.WindowOptions) updater.WindowHandle { return nil }
func (stubHost) Quit()                                                 {}
