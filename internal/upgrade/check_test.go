package upgrade

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLatestSelectsHighestPreviewBySemanticPrecedence(t *testing.T) {
	client := releaseClient(t, `[
		{"tag_name":"v0.9.0","prerelease":true},
		{"tag_name":"v1.0.0","prerelease":false},
		{"tag_name":"v0.10.0","prerelease":true},
		{"tag_name":"v0.11.0","prerelease":true,"draft":true},
		{"tag_name":"preview-latest","prerelease":true}
	]`)

	release, err := client.Latest(context.Background(), "0.8.0")
	if err != nil {
		t.Fatal(err)
	}
	if release.TagName != "v0.10.0" {
		t.Fatalf("latest preview = %q", release.TagName)
	}
}

func TestLatestOrdersPrereleaseIdentifiersSemantically(t *testing.T) {
	client := releaseClient(t, `[
		{"tag_name":"v1.0.0-rc.2","prerelease":true},
		{"tag_name":"v1.0.0-beta.20","prerelease":true},
		{"tag_name":"v1.0.0-rc.10","prerelease":true}
	]`)

	release, err := client.Latest(context.Background(), "1.0.0-rc.1")
	if err != nil {
		t.Fatal(err)
	}
	if release.TagName != "v1.0.0-rc.10" {
		t.Fatalf("latest preview = %q", release.TagName)
	}
}

func TestLatestKeepsStableBuildsOnStableChannel(t *testing.T) {
	client := releaseClient(t, `[
		{"tag_name":"v2.0.0-rc.1","prerelease":true},
		{"tag_name":"v1.9.0","prerelease":false},
		{"tag_name":"v1.10.0","prerelease":false},
		{"tag_name":"v3.0.0","prerelease":false,"draft":true},
		{"tag_name":"v2.0.0-rc.2","prerelease":false},
		{"tag_name":"unrelated","prerelease":false}
	]`)

	check, err := client.Check(context.Background(), "1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if check.Latest != "1.10.0" || !check.Available {
		t.Fatalf("check = %#v", check)
	}
}

func TestLatestReturnsErrorWhenChannelHasNoCompatibleRelease(t *testing.T) {
	client := releaseClient(t, `[
		{"tag_name":"v2.0.0-rc.1","prerelease":true},
		{"tag_name":"v2.0.0","prerelease":false,"draft":true},
		{"tag_name":"v0.9.0","prerelease":false}
	]`)

	_, err := client.Latest(context.Background(), "1.5.0")
	if err == nil || !strings.Contains(err.Error(), "no compatible stable release") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckNeverOffersDowngradeOrCrossChannelRelease(t *testing.T) {
	client := releaseClient(t, `[
		{"tag_name":"v3.0.0","prerelease":false},
		{"tag_name":"v0.8.0","prerelease":true}
	]`)

	check, err := client.Check(context.Background(), "0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	if check.Latest != "0.8.0" || check.Available {
		t.Fatalf("check = %#v", check)
	}
}

func TestLatestRejectsInvalidCurrentVersion(t *testing.T) {
	client := releaseClient(t, `[]`)
	if _, err := client.Latest(context.Background(), "development"); err == nil {
		t.Fatal("expected invalid current version to be rejected")
	}
}

func releaseClient(t *testing.T, response string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	return &Client{HTTPClient: server.Client(), ReleasesURL: server.URL}
}

func TestCompareVersionsUsesFullSemanticPrecedence(t *testing.T) {
	if compareVersions("1.0.0-rc.10", "1.0.0-rc.2") <= 0 {
		t.Fatal("numeric prerelease identifiers were not ordered numerically")
	}
	if compareVersions("1.0.0", "1.0.0-rc.10") <= 0 {
		t.Fatal("stable version must follow its prereleases")
	}
	if compareVersions("1.0.0+build.2", "1.0.0+build.1") != 0 {
		t.Fatal("build metadata must not affect precedence")
	}
}
