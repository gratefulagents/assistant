// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	defaultUpdateRepo       = "gratefulagents/assistant"
	githubAPIBase           = "https://api.github.com"
	githubReleaseLatest     = "latest"
	githubChecksumAssetName = "SHA256SUMS"
)

type updateOptions struct {
	Repo           string
	Version        string
	Force          bool
	DryRun         bool
	GOOS           string
	GOARCH         string
	ExecutablePath string
	HTTPClient     *http.Client
}

type updateResult struct {
	CurrentVersion string
	NewVersion     string
	AssetName      string
	TargetPath     string
	Updated        bool
	DryRun         bool
}

type githubRelease struct {
	TagName string               `json:"tag_name"`
	Name    string               `json:"name"`
	Assets  []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

func runUpdate(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && isUpdateHelp(args[0]) {
		fmt.Fprintln(stdout, updateUsage())
		return 0
	}
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	repo := fs.String("repo", defaultUpdateRepo, "GitHub repository in owner/name form")
	version := fs.String("version", githubReleaseLatest, "release version/tag to install, or latest")
	force := fs.Bool("force", false, "reinstall even when already on the selected version")
	dryRun := fs.Bool("dry-run", false, "show what would be installed without replacing the binary")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, updateUsage())
		return 2
	}
	if len(fs.Args()) > 0 {
		fmt.Fprintf(stderr, "update: unexpected argument %q\n\n%s\n", fs.Args()[0], updateUsage())
		return 2
	}

	result, err := updateAssistant(context.Background(), updateOptions{
		Repo:       *repo,
		Version:    *version,
		Force:      *force,
		DryRun:     *dryRun,
		HTTPClient: defaultHTTPClient,
	})
	if err != nil {
		fmt.Fprintln(stderr, "update:", err)
		return 1
	}
	fmt.Fprintln(stdout, updateResultText(result))
	return 0
}

func isUpdateHelp(arg string) bool {
	switch strings.TrimSpace(strings.ToLower(arg)) {
	case "help", "-h", "--help":
		return true
	default:
		return false
	}
}

func updateAssistant(ctx context.Context, opts updateOptions) (updateResult, error) {
	opts = normalizeUpdateOptions(opts)
	release, err := fetchGitHubRelease(ctx, opts)
	if err != nil {
		return updateResult{}, err
	}
	if strings.TrimSpace(release.TagName) == "" {
		return updateResult{}, errors.New("release response is missing tag_name")
	}

	asset, err := findReleaseAsset(release.Assets, updateAssetCandidates(opts.GOOS, opts.GOARCH))
	if err != nil {
		return updateResult{}, err
	}
	targetPath, err := resolveUpdateTarget(opts.ExecutablePath)
	if err != nil {
		return updateResult{}, err
	}
	current := currentBuildDetails().Version
	result := updateResult{
		CurrentVersion: current,
		NewVersion:     normalizeReleaseVersion(release.TagName),
		AssetName:      asset.Name,
		TargetPath:     targetPath,
		DryRun:         opts.DryRun,
	}
	if !opts.Force && releaseVersionsEqual(current, release.TagName) {
		return result, nil
	}
	if opts.DryRun {
		result.Updated = true
		return result, nil
	}
	if opts.GOOS == "windows" {
		return updateResult{}, errors.New("self-update cannot replace a running Windows executable; download the selected release asset manually")
	}

	tmpPath, err := downloadReleaseAsset(ctx, opts.HTTPClient, asset.BrowserDownloadURL, targetPath)
	if err != nil {
		return updateResult{}, err
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := verifyReleaseAsset(ctx, opts.HTTPClient, release.Assets, asset.Name, tmpPath); err != nil {
		return updateResult{}, err
	}
	if err := replaceExecutable(tmpPath, targetPath); err != nil {
		return updateResult{}, err
	}
	installed = true
	result.Updated = true
	return result, nil
}

func normalizeUpdateOptions(opts updateOptions) updateOptions {
	opts.Repo = strings.TrimSpace(opts.Repo)
	if opts.Repo == "" {
		opts.Repo = defaultUpdateRepo
	}
	opts.Version = strings.TrimSpace(opts.Version)
	if opts.Version == "" {
		opts.Version = githubReleaseLatest
	}
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.GOARCH == "" {
		opts.GOARCH = runtime.GOARCH
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = defaultHTTPClient
	}
	return opts
}

func fetchGitHubRelease(ctx context.Context, opts updateOptions) (githubRelease, error) {
	url, err := githubReleaseURL(opts.Repo, opts.Version)
	if err != nil {
		return githubRelease{}, err
	}
	data, err := httpGetBytes(ctx, opts.HTTPClient, url, 8<<20)
	if err != nil {
		return githubRelease{}, err
	}
	var release githubRelease
	if err := json.Unmarshal(data, &release); err != nil {
		return githubRelease{}, fmt.Errorf("parse GitHub release response: %w", err)
	}
	return release, nil
}

func githubReleaseURL(repo, version string) (string, error) {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	if len(strings.Split(repo, "/")) != 2 {
		return "", fmt.Errorf("repo must be owner/name, got %q", repo)
	}
	version = strings.TrimSpace(version)
	if version == "" || strings.EqualFold(version, githubReleaseLatest) {
		return githubAPIBase + "/repos/" + repo + "/releases/latest", nil
	}
	return githubAPIBase + "/repos/" + repo + "/releases/tags/" + githubReleaseTag(version), nil
}

func githubReleaseTag(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || strings.EqualFold(version, githubReleaseLatest) {
		return githubReleaseLatest
	}
	if len(version) > 0 && (version[0] == 'v' || version[0] == 'V') {
		return "v" + version[1:]
	}
	if len(version) > 0 && version[0] >= '0' && version[0] <= '9' {
		return "v" + version
	}
	return version
}

func updateAssetCandidates(goos, goarch string) []string {
	base := "assistant-" + strings.TrimSpace(goos) + "-" + strings.TrimSpace(goarch)
	if goos == "windows" {
		return []string{base + ".exe", base + ".exe.exe"}
	}
	return []string{base}
}

func findReleaseAsset(assets []githubReleaseAsset, candidates []string) (githubReleaseAsset, error) {
	for _, candidate := range candidates {
		for _, asset := range assets {
			if asset.Name == candidate && strings.TrimSpace(asset.BrowserDownloadURL) != "" {
				return asset, nil
			}
		}
	}
	var names []string
	for _, asset := range assets {
		names = append(names, asset.Name)
	}
	return githubReleaseAsset{}, fmt.Errorf("no release asset matched %s; available assets: %s", strings.Join(candidates, ", "), strings.Join(names, ", "))
}

func resolveUpdateTarget(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve executable path: %w", err)
		}
		path = exe
	}
	path = strings.TrimSpace(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if strings.TrimSpace(path) == "" {
		return "", errors.New("executable path is empty")
	}
	return path, nil
}

func downloadReleaseAsset(ctx context.Context, client *http.Client, url, targetPath string) (string, error) {
	targetInfo, err := os.Stat(targetPath)
	if err != nil {
		return "", fmt.Errorf("stat current executable %s: %w", targetPath, err)
	}
	mode := targetInfo.Mode().Perm()
	if mode == 0 {
		mode = 0o755
	}
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, ".assistant-update-*")
	if err != nil {
		return "", fmt.Errorf("create update temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
	}()
	if err := downloadToWriter(ctx, client, url, tmp); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Chmod(mode | 0o111); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("chmod update temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("sync update temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close update temp file: %w", err)
	}
	closed = true
	return tmpPath, nil
}

func verifyReleaseAsset(ctx context.Context, client *http.Client, assets []githubReleaseAsset, assetName, path string) error {
	checksumAsset, err := findReleaseAsset(assets, []string{githubChecksumAssetName})
	if err != nil {
		return err
	}
	data, err := httpGetBytes(ctx, client, checksumAsset.BrowserDownloadURL, 1<<20)
	if err != nil {
		return err
	}
	checksums := parseSHA256Sums(string(data))
	expected := checksums[assetName]
	if expected == "" {
		return fmt.Errorf("%s does not contain a checksum for %s", githubChecksumAssetName, assetName)
	}
	actual, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", assetName, actual, expected)
	}
	return nil
}

func parseSHA256Sums(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sum := strings.ToLower(strings.TrimSpace(fields[0]))
		if len(sum) != sha256.Size*2 {
			continue
		}
		name := strings.TrimPrefix(filepath.Base(fields[len(fields)-1]), "*")
		if name != "" {
			out[name] = sum
		}
	}
	return out
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s for checksum: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func replaceExecutable(tmpPath, targetPath string) error {
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("replace %s: %w", targetPath, err)
	}
	return nil
}

func httpGetBytes(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	var b strings.Builder
	if err := downloadToWriterLimited(ctx, client, url, &b, limit); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func downloadToWriter(ctx context.Context, client *http.Client, url string, w io.Writer) error {
	return downloadToWriterLimited(ctx, client, url, w, 0)
}

func downloadToWriterLimited(ctx context.Context, client *http.Client, url string, w io.Writer, limit int64) error {
	if client == nil {
		client = defaultHTTPClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json, application/octet-stream")
	req.Header.Set("User-Agent", "gratefulagents-assistant-updater")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("GET %s: %s: %s", url, resp.Status, firstLine(string(data)))
	}
	reader := resp.Body
	if limit > 0 {
		reader = io.NopCloser(io.LimitReader(resp.Body, limit+1))
	}
	n, err := io.Copy(w, reader)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if limit > 0 && n > limit {
		return fmt.Errorf("download %s exceeded %d bytes", url, limit)
	}
	return nil
}

func releaseVersionsEqual(a, b string) bool {
	a = normalizeReleaseVersion(a)
	b = normalizeReleaseVersion(b)
	return a != "" && b != "" && a == b
}

func normalizeReleaseVersion(version string) string {
	version = strings.TrimSpace(version)
	if len(version) > 1 && (version[0] == 'v' || version[0] == 'V') && version[1] >= '0' && version[1] <= '9' {
		return version[1:]
	}
	return version
}

func updateResultText(result updateResult) string {
	if !result.Updated {
		return fmt.Sprintf("assistant is already up to date (%s)", normalizeReleaseVersion(result.CurrentVersion))
	}
	action := "updated"
	if result.DryRun {
		action = "would update"
	}
	return fmt.Sprintf("assistant %s %s -> %s using %s at %s",
		action,
		normalizeReleaseVersion(result.CurrentVersion),
		normalizeReleaseVersion(result.NewVersion),
		result.AssetName,
		result.TargetPath,
	)
}

func updateUsage() string {
	return strings.TrimSpace(`usage: assistant update [flags]

Download the latest GitHub release asset for this OS/architecture, verify it
against SHA256SUMS, and replace the current assistant binary.

flags:
  --version TAG  release version/tag to install, or latest (default latest)
  --repo REPO    GitHub repository in owner/name form
  --force        reinstall even when already on the selected version
  --dry-run      show what would be installed without replacing the binary`)
}
