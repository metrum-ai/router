// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package buildinfo

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

var (
	binaryDigestOnce sync.Once
	binaryDigest     string
)

func init() {
	CurrentBinarySHA256()
}

type Info struct {
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	BuildDate    string `json:"build_date"`
	GoVersion    string `json:"go_version"`
	GOOS         string `json:"goos"`
	GOARCH       string `json:"goarch"`
	BinarySHA256 string `json:"binary_sha256"`
}

func Current() Info {
	return Info{
		Version:      Version,
		Commit:       Commit,
		BuildDate:    BuildDate,
		GoVersion:    runtime.Version(),
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		BinarySHA256: CurrentBinarySHA256(),
	}
}

// CurrentBinarySHA256 returns the SHA-256 digest of the running executable.
// It is computed once so reporting build metadata does not reread the binary
// on every request.
func CurrentBinarySHA256() string {
	binaryDigestOnce.Do(func() {
		executable, err := os.Executable()
		if err != nil {
			return
		}
		file, err := os.Open(executable)
		if err != nil {
			return
		}
		defer file.Close()
		digest := sha256.New()
		if _, err := io.Copy(digest, file); err != nil {
			return
		}
		binaryDigest = fmt.Sprintf("%x", digest.Sum(nil))
	})
	return binaryDigest
}

func Text() string {
	info := Current()
	return fmt.Sprintf("Metrum AI Router version=%s commit=%s build_date=%s go=%s %s/%s", info.Version, info.Commit, info.BuildDate, info.GoVersion, info.GOOS, info.GOARCH)
}

func JSON() string {
	raw, err := json.MarshalIndent(Current(), "", "  ")
	if err != nil {
		return "{}"
	}
	return string(raw)
}
