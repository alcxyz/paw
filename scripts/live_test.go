package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise failure paths without a cluster or credentials. No command in these
// tests can reach the real Kubernetes CLI.
func TestLiveConformanceCleanupOwnership(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		wantCreate  bool
		wantDestroy bool
	}{
		{name: "lookup-error"},
		{name: "existing-namespace"},
		{name: "create-error", wantCreate: true},
		{name: "owned-namespace", wantCreate: true, wantDestroy: true},
		{name: "replaced-namespace", wantCreate: true},
		{name: "cleanup-lookup-error", wantCreate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			writeExecutable := func(name, script string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!"+shell+"\n"+script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"curl", "git", "jq"} {
				writeExecutable(name, "exit 0\n")
			}
			writeExecutable("paw", `
printf '%s\n' "$*" >> "$PAW_TEST_DIR/actions"
case "$*" in
  'workspace create '*)
    test "$PAW_TEST_CASE" != create-error || exit 1
    : > "$PAW_TEST_DIR/created"
    ;;
  'workspace destroy '*) : > "$PAW_TEST_DIR/deleted" ;;
esac
`)
			writeExecutable("kubectl", `
case "$*" in
  *' get nodes') exit 0 ;;
  *' get namespace '*)
    case "$PAW_TEST_CASE" in
      lookup-error) exit 1 ;;
      existing-namespace) printf preexisting; exit 0 ;;
    esac
    test ! -f "$PAW_TEST_DIR/deleted" || exit 0
    test -f "$PAW_TEST_DIR/created" || exit 0
    if test -f "$PAW_TEST_DIR/rollout"; then
      case "$PAW_TEST_CASE" in
        replaced-namespace) printf other-uid; exit 0 ;;
        cleanup-lookup-error) exit 1 ;;
      esac
    fi
    printf test-uid
    ;;
  *' rollout status '*) : > "$PAW_TEST_DIR/rollout"; exit 1 ;;
  *) exit 99 ;;
esac
`)
			command := exec.Command("bash", "check-minikube-live.sh", "test-context", filepath.Join(dir, "paw"))
			command.Env = []string{
				"PATH=" + dir,
				"PAW_LIVE_TEST=1",
				"PAW_TEST_DIR=" + dir,
				"PAW_TEST_CASE=" + test.name,
			}
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("expected failure, got success: %s", output)
			}
			actions, err := os.ReadFile(filepath.Join(dir, "actions"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if got := strings.Contains(string(actions), "workspace create "); got != test.wantCreate {
				t.Errorf("create = %v, want %v; output: %s", got, test.wantCreate, output)
			}
			if got := strings.Contains(string(actions), "workspace destroy "); got != test.wantDestroy {
				t.Errorf("destroy = %v, want %v; output: %s", got, test.wantDestroy, output)
			}
		})
	}
}
