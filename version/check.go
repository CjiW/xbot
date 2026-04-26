package version

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	newRepoAPIURL = "https://api.github.com/repos/ai-pivot/xbot/releases/latest"
	oldRepoAPIURL = "https://api.github.com/repos/CjiW/xbot/releases/latest"
	checkTimeout  = 10 * time.Second
)

// githubRelease represents the GitHub API response for a release.
type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Name    string `json:"name"`
	Body    string `json:"body"`
}

// UpdateInfo holds the result of an update check.
type UpdateInfo struct {
	Current   string // local version
	Latest    string // remote latest version
	URL       string // release page URL
	HasUpdate bool
}

// semverRegex matches semantic versioning patterns like v1.2.3, 1.2.3, v1.2.3-rc1, etc.
var semverRegex = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-(.+))?$`)

// parseSemver extracts major, minor, patch from a version string.
// Returns -1,-1,-1 if the string doesn't match semver.
func parseSemver(v string) (major, minor, patch int) {
	v = strings.TrimSpace(v)
	m := semverRegex.FindStringSubmatch(v)
	if m == nil {
		return -1, -1, -1
	}
	fmt.Sscanf(m[1], "%d", &major)
	fmt.Sscanf(m[2], "%d", &minor)
	fmt.Sscanf(m[3], "%d", &patch)
	return
}

// isNewer returns true if b is newer than a (semver comparison).
// Falls back to string comparison if either version is not valid semver.
func isNewer(a, b string) bool {
	aMaj, aMin, aPat := parseSemver(a)
	bMaj, bMin, bPat := parseSemver(b)
	if aMaj < 0 || bMaj < 0 {
		// Can't compare as semver, just check if they're different
		return a != b
	}
	if bMaj != aMaj {
		return bMaj > aMaj
	}
	if bMin != aMin {
		return bMin > aMin
	}
	return bPat > aPat
}

// CheckUpdate queries GitHub Releases API for the latest version and compares
// with the local build version. Returns nil if the check fails or version is a
// dev build (in which case the caller should silently ignore).
//
// Migration: tries the new repo (ai-pivot/xbot) first. If the new repo has no
// releases yet, falls back to the old repo (CjiW/xbot) so existing users don't
// lose update notifications during the transition.
func CheckUpdate(ctx context.Context) *UpdateInfo {
	// Skip if version is completely empty
	if Version == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	release := fetchLatestRelease(ctx, newRepoAPIURL)
	if release == nil {
		release = fetchLatestRelease(ctx, oldRepoAPIURL)
	}
	if release == nil || release.TagName == "" {
		return nil
	}

	hasUpdate := isNewer(Version, release.TagName)
	return &UpdateInfo{
		Current:   Version,
		Latest:    release.TagName,
		URL:       release.HTMLURL,
		HasUpdate: hasUpdate,
	}
}

// fetchLatestRelease queries a single GitHub repo's latest release API.
// Returns nil on any error (network, non-200, parse failure).
func fetchLatestRelease(ctx context.Context, apiURL string) *githubRelease {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "xbot-cli")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil
	}

	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return nil
	}

	return &release
}
