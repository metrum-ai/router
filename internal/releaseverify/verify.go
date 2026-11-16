// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// Package releaseverify verifies that a local router executable matches a
// binary in a signed GitHub release package.
package releaseverify

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	defaultRepo     = "metrum-ai/router"
	issuer          = "https://token.actions.githubusercontent.com"
	maxArchiveBytes = 1 << 30
	maxBundleBytes  = 8 << 20
	maxBinaryBytes  = 512 << 20
	commandTimeout  = 90 * time.Second
	requestTimeout  = 90 * time.Second
)

var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`)

// Options configures release verification. Empty platform fields use the
// current platform. Empty Binary uses the running executable.
type Options struct {
	Release      string
	Binary       string
	Repo         string
	GOOS         string
	GOARCH       string
	BaseURL      string
	CosignBinary string
	GHBinary     string
	HTTPClient   *http.Client
	RunCommand   func(context.Context, string, ...string) error
}

// Result describes the verified release binary without disclosing local paths.
type Result struct {
	Release      string `json:"release"`
	Platform     string `json:"platform"`
	BinarySHA256 string `json:"binary_sha256"`
	Verified     bool   `json:"verified"`
}

// Verify checks the GitHub provenance and Cosign signature for the matching
// release archive, then compares its router executable to the supplied file.
func Verify(ctx context.Context, options Options) (Result, error) {
	if !releaseTagPattern.MatchString(options.Release) {
		return Result{}, fmt.Errorf("invalid release tag")
	}
	repo := options.Repo
	if repo == "" {
		repo = defaultRepo
	}
	goos, goarch := options.GOOS, options.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	archiveName, binaryName, err := artifactNames(options.Release, goos, goarch)
	if err != nil {
		return Result{}, err
	}
	binaryPath := options.Binary
	if binaryPath == "" {
		binaryPath, err = LocalRouterBinaryPath()
		if err != nil {
			return Result{}, err
		}
	}
	localDigest, err := fileSHA256(binaryPath)
	if err != nil {
		return Result{}, fmt.Errorf("read local executable: %w", err)
	}

	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	baseURL := strings.TrimRight(options.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://github.com/" + repo + "/releases/download"
	}
	assetURL := baseURL + "/" + options.Release + "/" + archiveName
	archive, err := download(ctx, client, assetURL, maxArchiveBytes)
	if err != nil {
		return Result{}, fmt.Errorf("download release archive: %w", err)
	}
	defer os.Remove(archive)
	bundle, err := download(ctx, client, assetURL+".sigstore.json", maxBundleBytes)
	if err != nil {
		return Result{}, fmt.Errorf("download release signature bundle: %w", err)
	}
	defer os.Remove(bundle)

	run := options.RunCommand
	if run == nil {
		run = runCommand
	}
	cosign := options.CosignBinary
	if cosign == "" {
		cosign = "cosign"
	}
	identity := "https://github.com/" + repo + "/.github/workflows/release.yml@refs/tags/" + options.Release
	if err := run(ctx, cosign,
		"verify-blob", "--bundle", bundle,
		"--certificate-identity", identity,
		"--certificate-oidc-issuer", issuer,
		archive,
	); err != nil {
		return Result{}, fmt.Errorf("verify Cosign signature: %w", err)
	}
	gh := options.GHBinary
	if gh == "" {
		gh = "gh"
	}
	if err := run(ctx, gh, attestationVerificationArgs(archive, repo, options.Release)...); err != nil {
		return Result{}, fmt.Errorf("verify GitHub provenance: %w", err)
	}

	releaseDigest, err := digestFromArchive(archive, archiveName, binaryName)
	if err != nil {
		return Result{}, err
	}
	if releaseDigest != localDigest {
		return Result{}, fmt.Errorf("local executable does not match the signed %s release binary", options.Release)
	}
	return Result{
		Release:      options.Release,
		Platform:     goos + "/" + goarch,
		BinarySHA256: localDigest,
		Verified:     true,
	}, nil
}

func attestationVerificationArgs(archive, repo, release string) []string {
	return []string{
		"attestation", "verify", archive,
		"--repo", repo,
		"--signer-workflow", "github.com/" + repo + "/.github/workflows/release.yml",
		"--source-ref", "refs/tags/" + release,
	}
}

func artifactNames(release, goos, goarch string) (archive, binary string, err error) {
	base := "metrum-ai-router-" + release + "-" + goos + "-" + goarch
	if goos == "linux" {
		return base + ".tar.gz", "bin/metrum-ai-router", nil
	}
	if goos == "windows" {
		return base + ".zip", "bin/metrum-ai-router.exe", nil
	}
	return "", "", fmt.Errorf("unsupported release platform %s/%s", goos, goarch)
}

func download(ctx context.Context, client *http.Client, url string, limit int64) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release asset request returned HTTP %d", response.StatusCode)
	}
	file, err := os.CreateTemp("", "router-release-verify-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, limit+1))
	closeErr := file.Close()
	if copyErr != nil {
		os.Remove(name)
		return "", copyErr
	}
	if closeErr != nil {
		os.Remove(name)
		return "", closeErr
	}
	if written > limit {
		os.Remove(name)
		return "", fmt.Errorf("release asset exceeds size limit")
	}
	return name, nil
}

func fileSHA256(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	written, err := io.Copy(digest, io.LimitReader(file, maxBinaryBytes+1))
	if err != nil {
		return "", err
	}
	if written > maxBinaryBytes {
		return "", fmt.Errorf("local executable exceeds size limit")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func digestFromArchive(filename, archiveName, binaryName string) (string, error) {
	if strings.HasSuffix(archiveName, ".zip") {
		return digestFromZip(filename, binaryName)
	}
	return digestFromTarGzip(filename, binaryName)
}

func digestFromZip(filename, binaryName string) (string, error) {
	reader, err := zip.OpenReader(filename)
	if err != nil {
		return "", fmt.Errorf("open release zip: %w", err)
	}
	defer reader.Close()
	for _, file := range reader.File {
		name := path.Clean(strings.ReplaceAll(file.Name, "\\", "/"))
		if name != binaryName && !strings.HasSuffix(name, "/"+binaryName) {
			continue
		}
		if file.Mode()&os.ModeType != 0 || file.UncompressedSize64 > maxBinaryBytes {
			return "", fmt.Errorf("release package binary is not a bounded regular file")
		}
		stream, err := file.Open()
		if err != nil {
			return "", err
		}
		digest, copyErr := digestReader(stream)
		closeErr := stream.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		return digest, nil
	}
	return "", fmt.Errorf("release package is missing %s", binaryName)
}

func digestFromTarGzip(filename, binaryName string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("open release tarball: %w", err)
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read release tarball: %w", err)
		}
		name := path.Clean(strings.ReplaceAll(header.Name, "\\", "/"))
		if name != binaryName && !strings.HasSuffix(name, "/"+binaryName) {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA || header.Size < 0 || header.Size > maxBinaryBytes {
			return "", fmt.Errorf("release package binary is not a bounded regular file")
		}
		return digestReader(io.LimitReader(reader, maxBinaryBytes+1))
	}
	return "", fmt.Errorf("release package is missing %s", binaryName)
}

func digestReader(reader io.Reader) (string, error) {
	digest := sha256.New()
	written, err := io.Copy(digest, io.LimitReader(reader, maxBinaryBytes+1))
	if err != nil {
		return "", err
	}
	if written > maxBinaryBytes {
		return "", fmt.Errorf("release package binary exceeds size limit")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func runCommand(parent context.Context, binary string, args ...string) error {
	ctx, cancel := context.WithTimeout(parent, commandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("command timed out")
		}
		return fmt.Errorf("command failed")
	}
	return nil
}

// LocalRouterBinaryPath locates the router beside routerctl, then on PATH.
func LocalRouterBinaryPath() (string, error) {
	name := "metrum-ai-router"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if current, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(current), name)
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	if candidate, err := exec.LookPath(name); err == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("could not locate metrum-ai-router; use --binary to select it")
}
