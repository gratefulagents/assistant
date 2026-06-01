// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGitHubReleaseURL(t *testing.T) {
	tests := map[string]string{
		"":       githubAPIBase + "/repos/gratefulagents/assistant/releases/latest",
		"latest": githubAPIBase + "/repos/gratefulagents/assistant/releases/latest",
		"0.8.0":  githubAPIBase + "/repos/gratefulagents/assistant/releases/tags/v0.8.0",
		"v0.8.0": githubAPIBase + "/repos/gratefulagents/assistant/releases/tags/v0.8.0",
	}
	for version, want := range tests {
		got, err := githubReleaseURL(defaultUpdateRepo, version)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("githubReleaseURL(%q) = %q, want %q", version, got, want)
		}
	}
}

func TestRunUpdateHelp(t *testing.T) {
	var stdout, stderr strings.Builder
	code := runUpdate([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if !strings.Contains(stdout.String(), "usage: assistant update") {
		t.Fatalf("stdout missing usage: %q", stdout.String())
	}
}

func TestUpdateAssetCandidates(t *testing.T) {
	if got, want := updateAssetCandidates("darwin", "arm64"), []string{"assistant-darwin-arm64"}; !sameStrings(got, want) {
		t.Fatalf("darwin candidates = %#v, want %#v", got, want)
	}
	if got, want := updateAssetCandidates("windows", "amd64"), []string{"assistant-windows-amd64.exe", "assistant-windows-amd64.exe.exe"}; !sameStrings(got, want) {
		t.Fatalf("windows candidates = %#v, want %#v", got, want)
	}
}

func TestFindReleaseAssetSupportsWindowsDoubleExe(t *testing.T) {
	asset, err := findReleaseAsset([]githubReleaseAsset{
		{Name: "assistant-windows-amd64.exe.exe", BrowserDownloadURL: "https://example.com/windows"},
	}, updateAssetCandidates("windows", "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if asset.Name != "assistant-windows-amd64.exe.exe" {
		t.Fatalf("asset = %#v", asset)
	}
}

func TestParseSHA256SumsAndVerifyReleaseAsset(t *testing.T) {
	data := []byte("new binary")
	sum := fmt.Sprintf("%x", sha256.Sum256(data))
	checksums := parseSHA256Sums(sum + "  assistant-darwin-arm64\n")
	if got := checksums["assistant-darwin-arm64"]; got != sum {
		t.Fatalf("checksum = %q, want %q", got, sum)
	}

	dir := t.TempDir()
	assetPath := filepath.Join(dir, "assistant")
	if err := os.WriteFile(assetPath, data, 0o755); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  assistant-darwin-arm64\n", sum)
	}))
	defer server.Close()

	err := verifyReleaseAsset(t.Context(), server.Client(), []githubReleaseAsset{
		{Name: githubChecksumAssetName, BrowserDownloadURL: server.URL},
	}, "assistant-darwin-arm64", assetPath)
	if err != nil {
		t.Fatal(err)
	}
}

func TestReplaceExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows cannot replace a running executable")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "assistant")
	tmp := filepath.Join(dir, "assistant.new")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceExecutable(tmp, target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("target = %q, want new", data)
	}
}

func TestReleaseVersionsEqual(t *testing.T) {
	if !releaseVersionsEqual("0.8.0", "v0.8.0") {
		t.Fatal("0.8.0 and v0.8.0 should compare equal")
	}
	if releaseVersionsEqual("dev", "v0.8.0") {
		t.Fatal("dev and v0.8.0 should not compare equal")
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
