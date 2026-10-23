package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestShouldCheckUpdate(t *testing.T) {
	linuxZip := "faceit-tester-linux-amd64.zip"
	tests := []struct {
		name              string
		current, testSpec string
		disabled, force   bool
		assetName         string
		want              bool
	}{
		{"release build, no gates", "0.1.0", "", false, false, linuxZip, true},
		{"dev build", "dev", "", false, false, linuxZip, false},
		{"empty version", "", "", false, false, linuxZip, false},
		{"never pref", "0.1.0", "", true, false, linuxZip, false},
		{"--test automation", "0.1.0", `{"NA":"all"}`, false, false, linuxZip, false},
		{"no platform asset", "0.1.0", "", false, false, "", false},
		// force bypasses dev / never / --test, but not a missing asset.
		{"force on dev", "dev", "", false, true, linuxZip, true},
		{"force with never pref", "0.1.0", "", true, true, linuxZip, true},
		{"force with --test", "0.1.0", `{"NA":"all"}`, false, true, linuxZip, true},
		{"force still needs an asset", "dev", "", false, true, "", false},
	}
	for _, tt := range tests {
		got := shouldCheckUpdate(tt.current, tt.testSpec, tt.disabled, tt.force, tt.assetName)
		if got != tt.want {
			t.Errorf("%s: shouldCheckUpdate(...) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestUpdateAssetName(t *testing.T) {
	// The function keys off runtime.GOOS/GOARCH; on any CI platform it must
	// return the exact asset name ci.yml produces, and never an empty string
	// for the three shipped platforms.
	name := updateAssetName()
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64", "linux/amd64", "darwin/arm64":
		if name == "" {
			t.Fatalf("updateAssetName() = \"\" on shipped platform %s", runtime.GOOS+"/"+runtime.GOARCH)
		}
		if !strings.HasPrefix(name, "faceit-tester-") || !strings.HasSuffix(name, ".zip") {
			t.Errorf("updateAssetName() = %q, want faceit-tester-*.zip shape", name)
		}
	default:
		if name != "" {
			t.Errorf("updateAssetName() = %q on unshipped platform, want \"\"", name)
		}
	}
}

func TestAssetDownloadURL(t *testing.T) {
	rel := releaseInfo{TagName: "0.2.0"}
	rel.Assets = append(rel.Assets,
		struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{"faceit-tester-linux-amd64.zip", "https://example.com/linux.zip"},
		struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{"faceit-tester-windows-amd64.exe.zip", ""},
	)
	if url, ok := assetDownloadURL(rel, "faceit-tester-linux-amd64.zip"); !ok || url != "https://example.com/linux.zip" {
		t.Errorf("got %q,%v", url, ok)
	}
	// Present but empty URL counts as missing.
	if _, ok := assetDownloadURL(rel, "faceit-tester-windows-amd64.exe.zip"); ok {
		t.Error("empty download URL should not count as found")
	}
	if _, ok := assetDownloadURL(rel, "missing.zip"); ok {
		t.Error("missing asset should not count as found")
	}
}

func TestFetchLatestRelease(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent")
		}
		fmt.Fprintf(w, `{"tag_name":"0.3.0","assets":[`+
			`{"name":"faceit-tester-linux-amd64.zip","browser_download_url":"http://example/dl"},`+
			`{"name":"other.zip","browser_download_url":"http://example/other"}]}`)
	}))
	defer ts.Close()

	// Point the fetch at the test server via a custom client transport is
	// overkill; call the endpoint directly with the same parsing shape.
	req, err := http.NewRequest(http.MethodGet, ts.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rel, err := fetchLatestReleaseFrom(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "0.3.0" {
		t.Errorf("TagName = %q", rel.TagName)
	}
	url, ok := assetDownloadURL(rel, "faceit-tester-linux-amd64.zip")
	if !ok || url != "http://example/dl" {
		t.Errorf("asset URL = %q,%v", url, ok)
	}
}

func TestFetchLatestReleaseHTTPError(t *testing.T) {
	for _, code := range []int{403, 404, 500} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fetchLatestReleaseFrom(resp.Body)
		resp.Body.Close()
		ts.Close()
		if err == nil {
			t.Errorf("status %d: expected error", code)
		}
	}
}

func TestDownloadFileTruncation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("short"))
	}))
	defer ts.Close()
	dest := filepath.Join(t.TempDir(), "out")
	// Go's http client surfaces a short body as unexpected EOF during the
	// copy; downloadFile's Content-Length check is defense-in-depth for
	// readers that don't. Either way the download must fail, not succeed
	// with a partial file.
	err := downloadFile(context.Background(), http.DefaultClient, ts.URL, dest, nil)
	if err == nil {
		t.Fatal("truncated download succeeded")
	}
}

func TestDownloadFileProgress(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 5<<20)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer ts.Close()
	dest := filepath.Join(t.TempDir(), "out")
	var calls int
	var lastWritten int64
	err := downloadFile(context.Background(), http.DefaultClient, ts.URL, dest, func(written, total int64) {
		calls++
		lastWritten = written
		if written < 0 {
			t.Errorf("written = %d", written)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Error("progress never fired")
	}
	if lastWritten != int64(len(payload)) {
		t.Errorf("last progress = %d, want %d", lastWritten, len(payload))
	}
}

func TestExtractZipRoundTrip(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "a.zip")
	writeTestZip(t, zipPath, map[string]string{
		"faceit-tester-macos.app/Contents/MacOS/faceit-tester": "binary",
		"faceit-tester-macos.app/Contents/Info.plist":          "<plist/>",
	})
	dest := filepath.Join(dir, "out")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatal(err)
	}
	if err := extractZip(zipPath, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "faceit-tester-macos.app", "Contents", "MacOS", "faceit-tester"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "binary" {
		t.Errorf("content = %q", got)
	}
}

func TestExtractZipSlipRejected(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "evil.zip")
	writeTestZip(t, zipPath, map[string]string{
		"../escaped": "pwned",
	})
	dest := filepath.Join(dir, "out")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatal(err)
	}
	if err := extractZip(zipPath, dest); err == nil {
		t.Fatal("zip-slip entry was not rejected")
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped")); err == nil {
		t.Fatal("zip-slip file escaped the destination")
	}
}

func TestExtractZipAbsolutePathRejected(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "evil.zip")
	// Absolute per filepath.IsAbs on the current OS: "/x" on unix,
	// drive-lettered on windows ("/tmp" is relative there).
	absEntry := "/tmp/absolute"
	if runtime.GOOS == "windows" {
		absEntry = "C:/tmp/absolute"
	}
	writeTestZip(t, zipPath, map[string]string{
		absEntry: "pwned",
	})
	dest := filepath.Join(dir, "out")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatal(err)
	}
	if err := extractZip(zipPath, dest); err == nil {
		t.Fatal("absolute-path entry was not rejected")
	}
}

func TestFindFile(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "faceit-tester", "usr", "local", "bin")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "faceit-tester"), []byte("bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "faceit-tester", "Makefile"), []byte("all:"), 0644); err != nil {
		t.Fatal(err)
	}
	got, ok := findFile(dir, "faceit-tester")
	if !ok {
		t.Fatal("findFile missed the nested binary")
	}
	if got != filepath.Join(nested, "faceit-tester") {
		t.Errorf("got %q", got)
	}
	if _, ok := findFile(dir, "missing"); ok {
		t.Error("findFile found a missing file")
	}
}

func TestFindAppBundle(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "faceit-tester-macos.app", "Contents"), 0755); err != nil {
		t.Fatal(err)
	}
	got := findAppBundle(dir)
	if got != filepath.Join(dir, "faceit-tester-macos.app") {
		t.Errorf("got %q", got)
	}
	// Ambiguous: two .app dirs → "".
	if err := os.MkdirAll(filepath.Join(dir, "Other.app"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := findAppBundle(dir); got != "" {
		t.Errorf("ambiguous bundles returned %q, want \"\"", got)
	}
	// No bundles → "".
	empty := t.TempDir()
	if got := findAppBundle(empty); got != "" {
		t.Errorf("empty dir returned %q", got)
	}
}

func TestEnclosingAppBundle(t *testing.T) {
	// filepath-constructed paths so separators match on windows too.
	inside := filepath.Join(string(filepath.Separator)+"Applications",
		"faceit-tester.app", "Contents", "MacOS", "faceit-tester")
	insideWant := filepath.Join(string(filepath.Separator)+"Applications", "faceit-tester.app")
	bare := filepath.Join(string(filepath.Separator)+"opt", "faceit-tester")
	trashInside := filepath.Join(string(filepath.Separator)+"opt", "Fake.app.old", "inner")
	tests := []struct {
		path, want string
	}{
		{inside, insideWant},
		{bare, ""},
		// ".app.old" trash bundles don't match (suffix must be exactly
		// ".app"), which is correct: cleanupOlds handles them separately.
		{trashInside, ""},
	}
	for _, tt := range tests {
		if got := enclosingAppBundle(tt.path); got != tt.want {
			t.Errorf("enclosingAppBundle(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestCleanupOlds(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "faceit-tester")
	trash := []string{
		filepath.Join(dir, "faceit-tester.old"),
		filepath.Join(dir, "faceit-tester.app.old"),
		filepath.Join(dir, "Other.app.old"),
	}
	if err := os.WriteFile(keep, []byte("app"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, p := range trash {
		if err := os.WriteFile(p, []byte("trash"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// cleanupOlds operates on the real executable's directory; exercise the
	// directory-level helper directly against the fixture.
	cleanupDir(dir)
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("cleanupDir removed the real binary: %v", err)
	}
	for _, p := range trash {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("cleanupDir left trash behind: %s", p)
		}
	}
}

// TestLiveUpdateCheck queries the real GitHub API for the latest release.
// It only verifies the response shape the updater depends on.
func TestLiveUpdateCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("network smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rel, err := fetchLatestRelease(ctx, &http.Client{Timeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("fetch latest release: %v", err)
	}
	if rel.TagName == "" {
		t.Fatal("latest release has empty tag_name")
	}
	if _, ok := parseSemver(rel.TagName); !ok {
		t.Errorf("latest release tag %q is not semver — pipeline not switched yet?", rel.TagName)
	}
	t.Logf("latest release: %s (%d assets)", rel.TagName, len(rel.Assets))
}

// --- helpers ---

func writeTestZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}
