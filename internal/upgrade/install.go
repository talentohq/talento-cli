package upgrade

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/talentohq/talento-cli/internal/buildinfo"
)

const (
	maxAssetBytes   = 256 << 20
	goInstallTarget = "github.com/talentohq/talento-cli/cmd/talento@latest"
)

type InstallResult struct {
	Current       string   `json:"current"`
	Installed     string   `json:"installed"`
	Executable    string   `json:"executable,omitempty"`
	AlreadyLatest bool     `json:"already_latest,omitempty"`
	Delegated     bool     `json:"delegated,omitempty"`
	Command       []string `json:"command,omitempty"`
}

func (r InstallResult) HumanText() string {
	if r.Delegated {
		return "This installation is managed by a package manager. Run: " + strings.Join(r.Command, " ")
	}
	if r.AlreadyLatest {
		return "talento " + r.Current + " is already the latest available version."
	}
	return fmt.Sprintf("Upgraded talento from %s to %s at %s.", r.Current, r.Installed, r.Executable)
}

func (c *Client) InstallLatest(ctx context.Context, current, executablePath, publicKey string) (InstallResult, error) {
	if buildinfo.Source == "go-install" {
		return c.installLatestWithGo(ctx, current, executablePath)
	}
	if command := PackageManagerCommand(executablePath); len(command) > 0 {
		return InstallResult{Current: current, Installed: current, Executable: executablePath, Delegated: true, Command: command}, nil
	}
	if strings.Contains(current, "dev") || buildinfo.Source == "development" {
		return InstallResult{}, fmt.Errorf("self-upgrade is disabled for development builds")
	}
	release, err := c.Latest(ctx, current)
	if err != nil {
		return InstallResult{}, err
	}
	latest := strings.TrimPrefix(release.TagName, "v")
	if compareVersions(latest, current) <= 0 {
		return InstallResult{Current: current, Installed: current, Executable: executablePath, AlreadyLatest: true}, nil
	}
	archiveName := artifactName(latest, runtime.GOOS, runtime.GOARCH)
	archiveAsset, ok := findAsset(release.Assets, archiveName)
	if !ok {
		return InstallResult{}, fmt.Errorf("release %s does not contain supported artifact %s", release.TagName, archiveName)
	}
	checksumsAsset, ok := findAsset(release.Assets, "checksums.txt")
	if !ok {
		return InstallResult{}, fmt.Errorf("release %s does not contain checksums.txt", release.TagName)
	}
	signatureAsset, ok := findAsset(release.Assets, "checksums.txt.sig")
	if !ok {
		return InstallResult{}, fmt.Errorf("release %s does not contain the signed checksum manifest", release.TagName)
	}
	checksums, err := c.download(ctx, checksumsAsset)
	if err != nil {
		return InstallResult{}, fmt.Errorf("download checksums: %w", err)
	}
	signature, err := c.download(ctx, signatureAsset)
	if err != nil {
		return InstallResult{}, fmt.Errorf("download checksum signature: %w", err)
	}
	if err := verifySignature(checksums, signature, publicKey); err != nil {
		return InstallResult{}, fmt.Errorf("verify release authenticity: %w", err)
	}
	expected, err := checksumFor(checksums, archiveName)
	if err != nil {
		return InstallResult{}, err
	}
	archiveData, err := c.download(ctx, archiveAsset)
	if err != nil {
		return InstallResult{}, fmt.Errorf("download release artifact: %w", err)
	}
	actual := sha256.Sum256(archiveData)
	if !strings.EqualFold(expected, hex.EncodeToString(actual[:])) {
		return InstallResult{}, fmt.Errorf("artifact checksum mismatch")
	}
	binary, err := extractBinary(archiveName, archiveData)
	if err != nil {
		return InstallResult{}, err
	}
	if err := replaceExecutable(ctx, executablePath, binary, latest); err != nil {
		return InstallResult{}, err
	}
	return InstallResult{Current: current, Installed: latest, Executable: executablePath}, nil
}

func (c *Client) installLatestWithGo(ctx context.Context, current, executablePath string) (InstallResult, error) {
	goExecutable, err := c.goExecutable()
	if err != nil {
		return InstallResult{}, fmt.Errorf("update Go installation: %w", err)
	}
	stagingDirectory, err := os.MkdirTemp("", "talento-go-install-*")
	if err != nil {
		return InstallResult{}, fmt.Errorf("create Go upgrade directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stagingDirectory) }()

	command := exec.CommandContext(ctx, goExecutable, "install", goInstallTarget)
	command.Env = environmentWith("GOBIN", stagingDirectory)
	var commandOutput bytes.Buffer
	command.Stdout = &commandOutput
	command.Stderr = &commandOutput
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return InstallResult{}, fmt.Errorf("update Go installation: %w", ctx.Err())
		}
		detail := strings.TrimSpace(commandOutput.String())
		if detail == "" {
			return InstallResult{}, fmt.Errorf("update Go installation: %w", err)
		}
		return InstallResult{}, fmt.Errorf("update Go installation: %w: %s", err, detail)
	}

	candidatePath := filepath.Join(stagingDirectory, executableName())
	latest, err := binaryVersion(ctx, candidatePath)
	if err != nil {
		return InstallResult{}, fmt.Errorf("validate Go-installed executable: %w", err)
	}
	if compareVersions(latest, current) <= 0 {
		return InstallResult{Current: current, Installed: current, Executable: executablePath, AlreadyLatest: true}, nil
	}
	candidate, err := os.ReadFile(candidatePath)
	if err != nil {
		return InstallResult{}, fmt.Errorf("read Go-installed executable: %w", err)
	}
	if len(candidate) > maxAssetBytes {
		return InstallResult{}, fmt.Errorf("Go-installed executable exceeds the size limit")
	}
	if err := replaceExecutable(ctx, executablePath, candidate, latest); err != nil {
		return InstallResult{}, err
	}
	return InstallResult{Current: current, Installed: latest, Executable: executablePath}, nil
}

func (c *Client) goExecutable() (string, error) {
	if c.goExecutablePath != "" {
		return c.goExecutablePath, nil
	}
	if path, err := exec.LookPath("go"); err == nil {
		return path, nil
	}
	path := filepath.Join(runtime.GOROOT(), "bin", executableFilename("go"))
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path, nil
	}
	return "", fmt.Errorf("the Go toolchain is unavailable; install Go and rerun `talento upgrade`")
}

func environmentWith(key, value string) []string {
	prefix := key + "="
	environment := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, prefix) {
			environment = append(environment, item)
		}
	}
	return append(environment, prefix+value)
}

func executableName() string {
	return executableFilename("talento")
}

func executableFilename(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func (c *Client) download(ctx context.Context, asset Asset) ([]byte, error) {
	u, err := url.Parse(asset.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("invalid HTTPS release asset URL")
	}
	if asset.Size > maxAssetBytes {
		return nil, fmt.Errorf("release asset %s exceeds the size limit", asset.Name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "talento-updater")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAssetBytes {
		return nil, fmt.Errorf("release asset %s exceeds the size limit", asset.Name)
	}
	return data, nil
}

func verifySignature(data, signature []byte, encodedPublicKey string) error {
	if strings.TrimSpace(encodedPublicKey) == "" {
		return fmt.Errorf("this binary does not contain the TalentoHQ release verification key")
	}
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedPublicKey))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid embedded release verification key")
	}
	rawSignature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil || len(rawSignature) != ed25519.SignatureSize {
		return fmt.Errorf("invalid checksum signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), data, rawSignature) {
		return fmt.Errorf("checksum signature is not valid")
	}
	return nil
}

func checksumFor(manifest []byte, name string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(manifest)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && strings.TrimPrefix(fields[len(fields)-1], "*") == name {
			if len(fields[0]) != sha256.Size*2 {
				return "", fmt.Errorf("invalid checksum for %s", name)
			}
			if _, err := hex.DecodeString(fields[0]); err != nil {
				return "", fmt.Errorf("invalid checksum for %s", name)
			}
			return fields[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("checksums.txt does not cover %s", name)
}

func artifactName(version, goos, goarch string) string {
	extension := ".tar.gz"
	if goos == "windows" {
		extension = ".zip"
	}
	return fmt.Sprintf("talento_%s_%s_%s%s", version, goos, goarch, extension)
}

func findAsset(assets []Asset, name string) (Asset, bool) {
	for _, asset := range assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return Asset{}, false
}

func extractBinary(name string, data []byte) ([]byte, error) {
	wanted := "talento"
	if strings.HasSuffix(name, ".zip") {
		wanted += ".exe"
		reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, fmt.Errorf("open release ZIP: %w", err)
		}
		for _, file := range reader.File {
			if filepath.Base(file.Name) != wanted || file.FileInfo().IsDir() {
				continue
			}
			stream, err := file.Open()
			if err != nil {
				return nil, err
			}
			binary, err := io.ReadAll(io.LimitReader(stream, maxAssetBytes+1))
			_ = stream.Close()
			if len(binary) > maxAssetBytes {
				return nil, fmt.Errorf("executable exceeds the size limit")
			}
			return binary, err
		}
		return nil, fmt.Errorf("release archive does not contain %s", wanted)
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != wanted {
			continue
		}
		binary, err := io.ReadAll(io.LimitReader(tarReader, maxAssetBytes+1))
		if len(binary) > maxAssetBytes {
			return nil, fmt.Errorf("executable exceeds the size limit")
		}
		return binary, err
	}
	return nil, fmt.Errorf("release archive does not contain %s", wanted)
}

func replaceExecutable(ctx context.Context, path string, binary []byte, expectedVersion string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect current executable: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".talento-upgrade-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(info.Mode().Perm() | 0o111); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(binary); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := validateBinary(ctx, tmpPath, expectedVersion); err != nil {
		return err
	}
	rollback := path + ".rollback"
	_ = os.Remove(rollback)
	if err := os.Rename(path, rollback); err != nil {
		return fmt.Errorf("prepare executable rollback: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Rename(rollback, path)
		return fmt.Errorf("replace executable: %w", err)
	}
	if err := validateBinary(ctx, path, expectedVersion); err != nil {
		_ = os.Remove(path)
		_ = os.Rename(rollback, path)
		return fmt.Errorf("validate installed executable: %w", err)
	}
	_ = os.Remove(rollback)
	return nil
}

func validateBinary(ctx context.Context, path, expectedVersion string) error {
	version, err := binaryVersion(ctx, path)
	if err != nil {
		return err
	}
	if version != expectedVersion {
		return fmt.Errorf("candidate executable reports version %q, expected %q", version, expectedVersion)
	}
	return nil
}

func binaryVersion(ctx context.Context, path string) (string, error) {
	command := exec.CommandContext(ctx, path, "--agent", "version")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("run candidate executable: %w", err)
	}
	var response struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return "", fmt.Errorf("parse candidate executable version: %w", err)
	}
	if strings.TrimSpace(response.Version) == "" {
		return "", fmt.Errorf("candidate executable did not report a version")
	}
	return response.Version, nil
}

func PackageManagerCommand(executablePath string) []string {
	normalized := filepath.ToSlash(strings.ToLower(executablePath))
	switch {
	case strings.Contains(normalized, "/cellar/") || strings.Contains(normalized, "/homebrew/"):
		return []string{"brew", "upgrade", "talentohq/tap/talento"}
	case strings.Contains(normalized, "/scoop/apps/"):
		return []string{"scoop", "update", "talento"}
	case strings.HasPrefix(normalized, "/nix/store/"):
		return []string{"nix", "profile", "upgrade", "talento"}
	}
	if runtime.GOOS == "linux" {
		if commandOwns("dpkg-query", "-S", executablePath) {
			return []string{"sudo", "apt-get", "install", "--only-upgrade", "talento"}
		}
		if commandOwns("rpm", "-qf", executablePath) {
			return []string{"sudo", "dnf", "upgrade", "talento"}
		}
		if commandOwns("apk", "info", "-W", executablePath) {
			return []string{"sudo", "apk", "upgrade", "talento"}
		}
	}
	return nil
}

func commandOwns(name string, arguments ...string) bool {
	path, err := exec.LookPath(name)
	if err != nil {
		return false
	}
	command := exec.Command(path, arguments...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run() == nil
}
