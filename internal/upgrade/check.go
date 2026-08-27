package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const releasesURL = "https://api.github.com/repos/talentohq/talento-cli/releases?per_page=100"

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type Release struct {
	TagName    string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

type Check struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
}

type Client struct {
	HTTPClient  *http.Client
	ReleasesURL string
}

func NewClient() *Client {
	return &Client{HTTPClient: &http.Client{Timeout: 15 * time.Second}, ReleasesURL: releasesURL}
}

// Latest returns the highest SemVer release in the current binary's channel.
// GitHub's prerelease flag is authoritative because preview v0 tags do not
// necessarily contain a SemVer prerelease suffix.
func (c *Client) Latest(ctx context.Context, current string) (Release, error) {
	currentVersion, err := parseSemanticVersion(current, false)
	if err != nil {
		return Release{}, fmt.Errorf("invalid current version %q: %w", current, err)
	}
	wantsPreview := currentVersion.major == 0 || len(currentVersion.prerelease) > 0

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ReleasesURL, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "talento-updater")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Release{}, fmt.Errorf("release service returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var releases []Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&releases); err != nil {
		return Release{}, err
	}

	var latest Release
	var latestVersion semanticVersion
	found := false
	for _, release := range releases {
		if release.Draft || release.Prerelease != wantsPreview {
			continue
		}
		version, err := parseSemanticVersion(release.TagName, true)
		if err != nil {
			continue
		}
		// Stable releases must be semantically stable as well as marked stable by
		// GitHub. This prevents a mismarked RC or v0 preview from crossing channels.
		if !wantsPreview && (version.major == 0 || len(version.prerelease) > 0) {
			continue
		}
		if !found || version.compare(latestVersion) > 0 {
			latest, latestVersion, found = release, version, true
		}
	}
	if !found {
		channel := "stable"
		if wantsPreview {
			channel = "preview"
		}
		return Release{}, fmt.Errorf("no compatible %s release is available", channel)
	}
	return latest, nil
}

func (c *Client) Check(ctx context.Context, current string) (Check, error) {
	release, err := c.Latest(ctx, current)
	if err != nil {
		return Check{Current: current}, err
	}
	latest := strings.TrimPrefix(release.TagName, "v")
	return Check{Current: current, Latest: latest, Available: compareVersions(latest, current) > 0}, nil
}

type semanticVersion struct {
	major      uint64
	minor      uint64
	patch      uint64
	prerelease []string
}

func parseSemanticVersion(value string, requireV bool) (semanticVersion, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "v") {
		value = strings.TrimPrefix(value, "v")
	} else if requireV {
		return semanticVersion{}, fmt.Errorf("release tag must start with v")
	}
	if value == "" || strings.Count(value, "+") > 1 {
		return semanticVersion{}, fmt.Errorf("invalid semantic version")
	}
	versionAndPrerelease, build, hasBuild := strings.Cut(value, "+")
	if hasBuild && !validIdentifiers(build, false) {
		return semanticVersion{}, fmt.Errorf("invalid build metadata")
	}
	core, prerelease, hasPrerelease := strings.Cut(versionAndPrerelease, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semanticVersion{}, fmt.Errorf("semantic version must contain major, minor, and patch")
	}
	numbers := make([]uint64, 3)
	for index, part := range parts {
		if !validNumericIdentifier(part, false) {
			return semanticVersion{}, fmt.Errorf("invalid numeric version component")
		}
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return semanticVersion{}, fmt.Errorf("invalid numeric version component")
		}
		numbers[index] = number
	}
	result := semanticVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}
	if hasPrerelease {
		if !validIdentifiers(prerelease, true) {
			return semanticVersion{}, fmt.Errorf("invalid prerelease")
		}
		result.prerelease = strings.Split(prerelease, ".")
	}
	return result, nil
}

func validIdentifiers(value string, enforceNumericLeadingZero bool) bool {
	if value == "" {
		return false
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		allNumeric := true
		for _, char := range identifier {
			if (char < '0' || char > '9') && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && char != '-' {
				return false
			}
			if char < '0' || char > '9' {
				allNumeric = false
			}
		}
		if enforceNumericLeadingZero && allNumeric && !validNumericIdentifier(identifier, false) {
			return false
		}
	}
	return true
}

func validNumericIdentifier(value string, allowLeadingZeros bool) bool {
	if value == "" || (!allowLeadingZeros && len(value) > 1 && value[0] == '0') {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func (version semanticVersion) compare(other semanticVersion) int {
	for _, values := range [][2]uint64{{version.major, other.major}, {version.minor, other.minor}, {version.patch, other.patch}} {
		if values[0] < values[1] {
			return -1
		}
		if values[0] > values[1] {
			return 1
		}
	}
	if len(version.prerelease) == 0 && len(other.prerelease) == 0 {
		return 0
	}
	if len(version.prerelease) == 0 {
		return 1
	}
	if len(other.prerelease) == 0 {
		return -1
	}
	limit := min(len(version.prerelease), len(other.prerelease))
	for index := 0; index < limit; index++ {
		left, right := version.prerelease[index], other.prerelease[index]
		if left == right {
			continue
		}
		leftNumeric := validNumericIdentifier(left, true)
		rightNumeric := validNumericIdentifier(right, true)
		if leftNumeric && rightNumeric {
			if len(left) < len(right) {
				return -1
			}
			if len(left) > len(right) {
				return 1
			}
			if left < right {
				return -1
			}
			return 1
		}
		if leftNumeric {
			return -1
		}
		if rightNumeric {
			return 1
		}
		if left < right {
			return -1
		}
		return 1
	}
	if len(version.prerelease) < len(other.prerelease) {
		return -1
	}
	if len(version.prerelease) > len(other.prerelease) {
		return 1
	}
	return 0
}

func compareVersions(left, right string) int {
	leftVersion, leftErr := parseSemanticVersion(left, false)
	rightVersion, rightErr := parseSemanticVersion(right, false)
	if leftErr != nil || rightErr != nil {
		return 0
	}
	return leftVersion.compare(rightVersion)
}
