package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	latestReleaseAPI = "https://api.github.com/repos/atharvwasthere/classrec/releases/latest"
	updateEvery      = 24 * time.Hour
	exitUpdated      = 3 // a failure exit, so the scheduled task restarts the agent on the new exe
)

// errUpdated means a new classrec.exe is in place and passed its selftest; the agent exits so
// Task Scheduler restarts it on the new exe.
var errUpdated = errors.New("updated to a new release; restarting")

// updater replaces exe with the latest GitHub release when that is newer than version.
type updater struct {
	api      string // the latest-release endpoint
	exe      string // the running classrec.exe
	version  string
	client   *http.Client
	selftest func(ctx context.Context, exe string) error
	logf     func(string, ...any)
}

func newUpdater(logf func(string, ...any)) (*updater, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &updater{api: latestReleaseAPI, exe: exe, version: version, client: &http.Client{Timeout: 5 * time.Minute},
		selftest: runSelftestOf, logf: logf}, nil
}

// runSelftestOf runs exe's own selftest.
func runSelftestOf(ctx context.Context, exe string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "selftest").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// update installs the latest release if it is newer, keeping the old exe as classrec.prev.exe.
// The download must match the release's sha256 and the new exe must pass its selftest; otherwise
// the old exe stays (or is put back) and the new one is kept as classrec.bad.exe to look at.
func (u *updater) update(ctx context.Context) (updated bool, err error) {
	if u.version == "dev" {
		return false, nil // a dev build is never replaced
	}
	var rel release
	if err := u.getJSON(ctx, u.api, &rel); err != nil {
		return false, err
	}
	if !newer(rel.Tag, u.version) {
		return false, nil
	}
	urls := map[string]string{}
	for _, a := range rel.Assets {
		urls[a.Name] = a.URL
	}
	if urls["classrec.exe"] == "" || urls["classrec.exe.sha256"] == "" {
		return false, fmt.Errorf("release %s lacks classrec.exe or its sha256", rel.Tag)
	}
	dir := filepath.Dir(u.exe)
	fresh, prev, bad := filepath.Join(dir, "classrec.new.exe"), filepath.Join(dir, "classrec.prev.exe"), filepath.Join(dir, "classrec.bad.exe")
	if err := u.download(ctx, urls["classrec.exe"], urls["classrec.exe.sha256"], fresh); err != nil {
		_ = os.Remove(fresh) // a partial or unverified download of ours
		return false, err
	}
	_ = os.Remove(prev) // the release before last; only the one before this is kept
	if err := os.Rename(u.exe, prev); err != nil {
		return false, err
	}
	if err := os.Rename(fresh, u.exe); err != nil {
		return false, errors.Join(err, os.Rename(prev, u.exe))
	}
	if err := u.selftest(ctx, u.exe); err != nil {
		_ = os.Remove(bad)
		return false, errors.Join(fmt.Errorf("%s failed its selftest, rolled back: %w", rel.Tag, err),
			os.Rename(u.exe, bad), os.Rename(prev, u.exe))
	}
	u.logf("updated from %s to %s", u.version, rel.Tag)
	return true, nil
}

// download saves the exe at exeURL to path and checks it against the sha256 at sumURL.
func (u *updater) download(ctx context.Context, exeURL, sumURL, path string) error {
	sumText, err := u.get(ctx, sumURL)
	if err != nil {
		return err
	}
	want, _, _ := strings.Cut(strings.TrimSpace(string(sumText)), " ")
	exe, err := u.get(ctx, exeURL)
	if err != nil {
		return err
	}
	got := sha256.Sum256(exe)
	if !strings.EqualFold(hex.EncodeToString(got[:]), want) {
		return errors.New("the downloaded classrec.exe does not match its sha256")
	}
	return os.WriteFile(path, exe, 0o755)
}

func (u *updater) getJSON(ctx context.Context, url string, into any) error {
	body, err := u.get(ctx, url)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, into)
}

func (u *updater) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "classrec-updater")
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// newer reports whether tag (v1.2.3) is a later version than current.
func newer(tag, current string) bool {
	a, b := versionParts(tag), versionParts(current)
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func versionParts(v string) [3]int {
	var parts [3]int
	for i, s := range strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3) {
		parts[i], _ = strconv.Atoi(strings.SplitN(s, "-", 2)[0])
	}
	return parts
}
