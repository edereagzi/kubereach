package service_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
)

func TestNewerRelease(t *testing.T) {
	var tag string
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://github.com/edereagzi/kubereach/releases/tag/%s"}`, tag, tag)
	}))
	t.Cleanup(srv.Close)
	svc := service.New(filepath.Join(t.TempDir(), "kubereach.yaml"), nil)
	svc.ReleasesURL = srv.URL

	for _, tc := range []struct {
		tag, current string
		newer        bool
	}{
		{"v0.2.0", "0.1.0", true},
		{"v0.1.10", "0.1.9", true},
		{"v1.0.0", "0.9.9", true},
		{"v0.1.0", "0.1.0", false},
		{"v0.1.0", "0.2.0", false},
		{"v0.1.9", "0.1.10", false},
		{"v0.2", "0.1.3", true},
	} {
		tag = tc.tag
		got := svc.NewerRelease(tc.current)
		if (got != nil) != tc.newer {
			t.Errorf("%s over %s: got %+v, want newer=%v", tc.tag, tc.current, got, tc.newer)
		}
		if got != nil && (got.Version != tc.tag || got.URL != "https://github.com/edereagzi/kubereach/releases/tag/"+tc.tag) {
			t.Errorf("%s over %s: got %+v", tc.tag, tc.current, got)
		}
	}

	requests.Store(0)
	if got := svc.NewerRelease("dev"); got != nil || requests.Load() != 0 {
		t.Errorf("dev build: got %+v after %d requests", got, requests.Load())
	}
	if err := svc.SetUpdateCheck(false); err != nil {
		t.Fatal(err)
	}
	if got := svc.NewerRelease("0.0.1"); got != nil || requests.Load() != 0 {
		t.Errorf("check off: got %+v after %d requests", got, requests.Load())
	}
	if err := svc.SetUpdateCheck(true); err != nil {
		t.Fatal(err)
	}
	if got := svc.NewerRelease("0.0.1"); got == nil {
		t.Error("check back on: no release")
	}

	srv.Close()
	if got := svc.NewerRelease("0.0.1"); got != nil {
		t.Errorf("unreachable: got %+v", got)
	}
}
