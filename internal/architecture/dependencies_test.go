// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package architecture_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	commercePackage = "github.com/metrum-ai/router/internal/commerce"
)

func TestRequestPathDoesNotDependOnInfrastructureSDKs(t *testing.T) {
	for _, target := range []string{"./cmd/metrum-ai-router", "./internal/router"} {
		t.Run(strings.TrimPrefix(target, "./"), func(t *testing.T) {
			dependencies := goListDependencies(t, target)
			for dependency := range dependencies {
				if packageOrChild(dependency, commercePackage) ||
					strings.HasPrefix(dependency, "k8s.io/") ||
					strings.HasPrefix(dependency, "github.com/aws/aws-sdk-go-v2/service/eks") ||
					strings.HasPrefix(dependency, "github.com/aws/aws-sdk-go-v2/service/rds") {
					t.Errorf("%s must not depend on %s", target, dependency)
				}
			}
		})
	}
}

func TestCommerceStaysOffRequestPath(t *testing.T) {
	for _, target := range []string{"./cmd/metrum-ai-router", "./internal/router", "./cmd/metrum-ai-routerctl"} {
		t.Run(strings.TrimPrefix(target, "./"), func(t *testing.T) {
			dependencies := goListDependencies(t, target)
			for dependency := range dependencies {
				if packageOrChild(dependency, commercePackage) {
					t.Errorf("%s must not depend on optional commerce package %s", target, dependency)
				}
			}
		})
	}
}

func goListDependencies(t *testing.T, target string) map[string]bool {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test source path")
	}
	command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", target)
	command.Dir = filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", target, err, output)
	}
	dependencies := make(map[string]bool)
	for _, dependency := range strings.Fields(string(output)) {
		dependencies[dependency] = true
	}
	return dependencies
}

func packageOrChild(dependency, root string) bool {
	return dependency == root || strings.HasPrefix(dependency, root+"/")
}
