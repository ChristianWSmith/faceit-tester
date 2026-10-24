package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// updateRepo is the GitHub repo whose releases are checked for updates.
	updateRepo = "ChristianWSmith/faceit-tester"
	// updateReleasesURL is the latest-release API endpoint (excludes drafts
	// and prereleases; no auth needed for a public repo).
	updateReleasesURL = "https://api.github.com/repos/" + updateRepo + "/releases/latest"
	// updateManualURL is shown in error dialogs as the manual-download path.
	updateManualURL = "https://github.com/" + updateRepo + "/releases/latest"
)

// releaseInfo is the subset of the GitHub release JSON the updater needs.
type releaseInfo struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// updateAssetName returns the release asset this platform downloads, or ""
// when the platform has no shipped asset (the update check is skipped).
func updateAssetName() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "faceit-tester-windows-amd64.exe.zip"
	case "linux/amd64":
		return "faceit-tester-linux-amd64.zip"
	case "darwin/arm64":
		return "faceit-tester-macos.app.zip"
	default:
		return ""
	}
}

// shouldCheckUpdate gates the startup update check: dev/unstamped builds,
// opted-out users, --test automation, and platforms without an asset never
// hit the network. force (--force-update-check) bypasses the dev, disabled,
// and --test gates so the dialog can be reviewed on a local build; only a
// missing platform asset still blocks, since there would be nothing to
// download.
func shouldCheckUpdate(current, testSpec string, disabled, force bool, assetName string) bool {
	if assetName == "" {
		return false
	}
	if force {
		return true
	}
	if current == "" || current == "dev" {
		return false
	}
	if disabled {
		return false
	}
	// --test runs must never block on a modal dialog.
	if testSpec != "" {
		return false
	}
	return true
}

// fetchLatestRelease queries the GitHub latest-release API.
func fetchLatestRelease(ctx context.Context, client *http.Client) (releaseInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, updateReleasesURL, nil)
	if err != nil {
		return releaseInfo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "faceit-tester/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return releaseInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return releaseInfo{}, fmt.Errorf("GitHub returned status %d", resp.StatusCode)
	}
	return fetchLatestReleaseFrom(resp.Body)
}

// fetchLatestReleaseFrom decodes a GitHub release JSON body. Split from the
// fetch so tests can drive the parsing without the network.
func fetchLatestReleaseFrom(r io.Reader) (releaseInfo, error) {
	var rel releaseInfo
	if err := json.NewDecoder(r).Decode(&rel); err != nil {
		return releaseInfo{}, err
	}
	return rel, nil
}

// assetDownloadURL finds the named asset's download URL in a release.
func assetDownloadURL(rel releaseInfo, assetName string) (string, bool) {
	for _, a := range rel.Assets {
		if a.Name == assetName {
			return a.BrowserDownloadURL, a.BrowserDownloadURL != ""
		}
	}
	return "", false
}

// downloadFile fetches url into dest, verifying the byte count against the
// response's Content-Length when present. progress, when non-nil, is called
// with bytes written so far and the total (-1 when unknown).
func downloadFile(ctx context.Context, client *http.Client, url, dest string, progress func(written, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "faceit-tester/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	var reader io.Reader = resp.Body
	if progress != nil {
		reader = &progressReader{r: resp.Body, total: resp.ContentLength, fn: progress}
	}
	n, copyErr := io.Copy(f, reader)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return fmt.Errorf("truncated download: got %d of %d bytes", n, resp.ContentLength)
	}
	return nil
}

// progressReader wraps a reader and reports cumulative progress.
type progressReader struct {
	r         io.Reader
	total     int64
	written   int64
	fn        func(written, total int64)
	lastCall  time.Time
	lastBytes int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.written += int64(n)
	// Throttle UI updates to ~10/s so a fast LAN download doesn't flood
	// the fyne.Do queue; always report the first and last chunks.
	now := time.Now()
	if p.fn != nil && (err != nil || now.Sub(p.lastCall) >= 100*time.Millisecond || p.written-p.lastBytes >= 1<<20) {
		p.fn(p.written, p.total)
		p.lastCall = now
		p.lastBytes = p.written
	}
	return n, err
}

// applyUpdate downloads the platform asset for rel, extracts it into a temp
// staging dir, and hands off to replaceAndRelaunch. progress, when non-nil,
// receives download fractions in [0,1] (total < 0 means unknown length);
// callers invoke this from a goroutine and marshal progress updates onto
// the UI thread themselves.
func applyUpdate(ctx context.Context, client *http.Client, rel releaseInfo, progress func(frac float64)) error {
	assetName := updateAssetName()
	if assetName == "" {
		return fmt.Errorf("this platform has no release asset")
	}
	url, ok := assetDownloadURL(rel, assetName)
	if !ok {
		return fmt.Errorf("release %s has no asset named %s", rel.TagName, assetName)
	}
	tmp, err := os.MkdirTemp("", "faceit-tester-update-*")
	if err != nil {
		return err
	}
	// Keep tmp on success: replaceAndRelaunch may still be reading the
	// staged file (windows copies it; linux copies; darwin moves the
	// bundle). The relaunched process's cleanupOlds sweeps *.old, and the
	// temp dir is removed by the OS eventually. On failure remove it now.
	okExit := false
	defer func() {
		if !okExit {
			os.RemoveAll(tmp)
		}
	}()

	zipPath := filepath.Join(tmp, assetName)
	if err := downloadFile(ctx, client, url, zipPath, func(written, total int64) {
		if progress != nil {
			if total > 0 {
				progress(float64(written) / float64(total))
			} else {
				progress(-1)
			}
		}
	}); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if progress != nil {
		progress(1)
	}

	extractDir := filepath.Join(tmp, "extracted")
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return err
	}
	if err := extractZip(zipPath, extractDir); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	stageDir := filepath.Join(tmp, "stage")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		return err
	}
	binaryName, err := stagePayload(extractDir, stageDir)
	if err != nil {
		return fmt.Errorf("unexpected update contents: %w", err)
	}
	if err := replaceAndRelaunch(stageDir, binaryName); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	okExit = true
	return nil
}

// stagePayload copies the executable payload out of the extracted archive
// into stageDir and returns the binary's file name (the name
// replaceAndRelaunch installs). Per-platform archive layouts (pinned by
// TestStagePayloadPublishedLayouts):
//   - windows: bare faceit-tester.exe at the zip root
//   - linux: faceit-tester/ tree containing usr/local/bin/faceit-tester
//   - darwin: faceit-tester-macos.app/ bundle at the zip root
func stagePayload(extractDir, stageDir string) (string, error) {
	return stagePayloadFor(runtime.GOOS, extractDir, stageDir)
}

// stagePayloadFor is stagePayload with an explicit GOOS so tests can pin
// every platform's published zip layout from any host. The windows leg
// must look for faceit-tester.exe — sharing the extensionless linux name
// there made every Windows self-update fail with "archive does not
// contain faceit-tester" (0.2.0 → 1.0.0).
func stagePayloadFor(goos, extractDir, stageDir string) (string, error) {
	switch goos {
	case "windows":
		return stageBinary(extractDir, stageDir, "faceit-tester.exe")
	case "linux":
		return stageBinary(extractDir, stageDir, "faceit-tester")
	case "darwin":
		app := findAppBundle(extractDir)
		if app == "" {
			return "", fmt.Errorf("archive does not contain a .app bundle")
		}
		// Move (not copy) the whole bundle into stageDir; darwin's
		// replaceAndRelaunch expects the bundle as stageDir's child.
		if err := os.Rename(app, filepath.Join(stageDir, filepath.Base(app))); err != nil {
			return "", err
		}
		return "faceit-tester", nil
	default:
		return "", fmt.Errorf("unsupported platform")
	}
}

// stageBinary finds binaryName anywhere under extractDir, copies it into
// stageDir under the same name, and returns that name.
func stageBinary(extractDir, stageDir, binaryName string) (string, error) {
	src, ok := findFile(extractDir, binaryName)
	if !ok {
		return "", fmt.Errorf("archive does not contain %s", binaryName)
	}
	if err := copyFile(src, filepath.Join(stageDir, binaryName)); err != nil {
		return "", err
	}
	if err := setExecutable(filepath.Join(stageDir, binaryName)); err != nil {
		return "", err
	}
	return binaryName, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0755)
}

// extractZip unpacks a zip archive into destDir with zip-slip rejection:
// every entry must resolve inside destDir. Files are written 0644 and
// directories 0755; callers chmod the binaries they care about.
func extractZip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if err := extractZipEntry(f, destDir); err != nil {
			return err
		}
	}
	return nil
}

func extractZipEntry(f *zip.File, destDir string) error {
	// Clean and require the entry to stay under destDir (zip-slip).
	name := filepath.Clean(f.Name)
	if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return fmt.Errorf("archive entry escapes destination: %q", f.Name)
	}
	dest := filepath.Join(destDir, name)
	if !strings.HasPrefix(dest, filepath.Clean(destDir)+string(filepath.Separator)) {
		return fmt.Errorf("archive entry escapes destination: %q", f.Name)
	}
	if f.FileInfo().IsDir() {
		return os.MkdirAll(dest, 0755)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, rc)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// findFile returns the path of the first regular file under root whose
// base name matches want (depth-first).
func findFile(root, want string) (string, bool) {
	found := ""
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" || d.IsDir() {
			return err
		}
		if d.Name() == want {
			found = path
			return io.EOF // stop walking; handled below
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return "", false
	}
	return found, found != ""
}

// findAppBundle returns the path of the single *.app directory under root,
// or "" if there isn't exactly one.
func findAppBundle(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var app string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".app") {
			if app != "" {
				return "" // ambiguous
			}
			app = filepath.Join(root, e.Name())
		}
	}
	return app
}

// setExecutable chmods path to 0755, ignoring a no-op on Windows.
func setExecutable(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	return os.Chmod(path, 0755)
}

// cleanupOlds deletes leftover self-replace trash from the previous
// generation: "<exe>.old" files next to the executable and, when the
// executable lives inside a *.app bundle (macOS), stale "*.app.old"
// bundles next to that bundle. Best-effort: a still-locked file just
// stays for the next launch.
func cleanupOlds() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	cleanupDir(filepath.Dir(exe))
	// .../Foo.app/Contents/MacOS/faceit-tester → also sweep .../ (bundle parent).
	if runtime.GOOS == "darwin" {
		if app := enclosingAppBundle(exe); app != "" {
			cleanupDir(filepath.Dir(app))
		}
	}
}

func cleanupDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".old") || strings.HasSuffix(name, ".app.old") {
			os.RemoveAll(filepath.Join(dir, name))
		}
	}
}

// enclosingAppBundle walks up from path to the nearest ancestor directory
// ending in ".app", or "" when path isn't inside a bundle.
func enclosingAppBundle(path string) string {
	dir := filepath.Dir(path)
	for {
		if strings.HasSuffix(dir, ".app") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
