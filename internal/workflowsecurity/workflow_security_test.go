package workflowsecurity

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func workflow(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", ".github", "workflows", name)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestPublicPullRequestsStayOnHostedRunners(t *testing.T) {
	ci := workflow(t, "ci.yml")
	trusted := regexp.MustCompile(`(?s)test:\s+if: \(github\.event_name == 'push' \|\| github\.event_name == 'workflow_dispatch'\) && github\.ref == 'refs/heads/main'\s+runs-on: \[self-hosted, Linux, X64, server1, onwatch\]`)
	untrusted := regexp.MustCompile(`(?s)test-pr:\s+if: github\.event_name == 'pull_request'\s+runs-on: ubuntu-latest`)
	if !trusted.MatchString(ci) {
		t.Fatal("trusted main CI is not routed to the onWatch Server1 runner")
	}
	if !untrusted.MatchString(ci) {
		t.Fatal("public pull-request CI is not pinned to a hosted runner")
	}
	if strings.Contains(ci, "pull_request_target") {
		t.Fatal("pull_request_target must never reach the persistent runner")
	}
}

func TestReleaseLinuxJobsRequireMainAndServer1(t *testing.T) {
	release := workflow(t, "release.yml")
	linuxJobs := []string{
		"test",
		"build-standard",
		"build-linux-desktop",
		"release",
		"docker",
		"homebrew",
	}
	for _, name := range linuxJobs {
		pattern := regexp.MustCompile(
			`(?ms)^  ` + regexp.QuoteMeta(name) + `:\s+.*?` +
				`if: github\.ref == 'refs/heads/main'.*?` +
				`runs-on: \[self-hosted, Linux, X64, server1, onwatch\]`,
		)
		if !pattern.MatchString(release) {
			t.Fatalf("%s is not main-only on the onWatch runner", name)
		}
	}
	if strings.Count(release, "runs-on: macos-15") != 2 {
		t.Fatal("both native macOS release jobs must remain hosted")
	}
}

func TestSelfHostedActionsAreCommitPinned(t *testing.T) {
	for _, name := range []string{"ci.yml", "release.yml"} {
		source := workflow(t, name)
		if regexp.MustCompile(`uses:\s+[^#\s]+@v\d+`).MatchString(source) {
			t.Fatalf("%s contains a tag-pinned action", name)
		}
	}
}
