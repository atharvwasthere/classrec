package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fakeRelease serves a release with a new classrec.exe and a sha256 that may be wrong.
func fakeRelease(t *testing.T, tag string, exe []byte, sum string) string {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":"classrec.exe","browser_download_url":"%s/exe"},`+
			`{"name":"classrec.exe.sha256","browser_download_url":"%s/sum"}]}`, tag, srv.URL, srv.URL)
	})
	mux.HandleFunc("/exe", func(w http.ResponseWriter, _ *http.Request) { w.Write(exe) })
	mux.HandleFunc("/sum", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "%s  classrec.exe\n", sum) })
	return srv.URL + "/latest"
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Promise: an update is installed only if it matches its sha256 and passes its selftest; otherwise
// the running version stays, and the old exe is always kept as classrec.prev.exe.
func TestSelfUpdate(t *testing.T) {
	newExe := []byte("new classrec")
	for _, tt := range []struct {
		name        string
		tag, sum    string
		selftestErr error
		wantUpdated bool
		wantFiles   map[string]string // file -> content afterwards; "" means absent
	}{
		{"newer and healthy", "v0.2.0", sum(newExe), nil, true,
			map[string]string{"classrec.exe": "new classrec", "classrec.prev.exe": "old classrec"}},
		{"fails its selftest", "v0.2.0", sum(newExe), errors.New("obs FAIL"), false,
			map[string]string{"classrec.exe": "old classrec", "classrec.bad.exe": "new classrec", "classrec.prev.exe": ""}},
		{"wrong sha256", "v0.2.0", sum([]byte("tampered")), nil, false,
			map[string]string{"classrec.exe": "old classrec", "classrec.new.exe": "", "classrec.prev.exe": ""}},
		{"not newer", "v0.1.0", sum(newExe), nil, false,
			map[string]string{"classrec.exe": "old classrec", "classrec.prev.exe": ""}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "classrec.exe")
			os.WriteFile(exe, []byte("old classrec"), 0o755)
			u := &updater{api: fakeRelease(t, tt.tag, newExe, tt.sum), exe: exe, version: "v0.1.0", client: http.DefaultClient,
				selftest: func(context.Context, string) error { return tt.selftestErr }, logf: t.Logf}
			updated, err := u.update(context.Background())
			if updated != tt.wantUpdated {
				t.Fatalf("updated %v (%v), want %v", updated, err, tt.wantUpdated)
			}
			for name, want := range tt.wantFiles {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if want == "" && err == nil || want != "" && string(got) != want {
					t.Errorf("%s holds %q (%v), want %q", name, got, err, want)
				}
			}
		})
	}
	if newer("v0.10.0", "v0.9.3") != true || newer("v0.1.0", "v0.1.0") || newer("v0.0.9", "v0.1.0") {
		t.Error("version order is wrong")
	}
}
