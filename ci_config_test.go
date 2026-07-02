package main

import (
	"os"
	"strings"
	"testing"
)

func TestCIConfigUsesNonMutatingStyleAndCoreGates(t *testing.T) {
	workflow, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	ci := string(workflow)
	for _, want := range []string{
		"scripts/check-gofmt.sh",
		"go vet ./...",
		"go test -race -coverprofile=coverage.out -covermode=atomic -count=1 ./...",
		"go build -o onwatch .",
	} {
		if !strings.Contains(ci, want) {
			t.Fatalf("CI workflow missing %q", want)
		}
	}
	if strings.Contains(ci, "go fmt ./...") {
		t.Fatal("CI should check formatting instead of mutating files with go fmt")
	}
}

func TestMakefileExposesLocalQualityTargets(t *testing.T) {
	data, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	makefile := string(data)
	for _, want := range []string{
		"fmt-check:",
		"lint: fmt-check vet",
		"test-race:",
		"ci: lint test-race build",
	} {
		if !strings.Contains(makefile, want) {
			t.Fatalf("Makefile missing %q", want)
		}
	}
}
