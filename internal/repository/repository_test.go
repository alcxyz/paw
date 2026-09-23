package repository

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveSelectsNamedCommittedRef(t *testing.T) {
	repositoryPath := newTestRepository(t)
	commit := gitTestOutput(t, repositoryPath, "rev-parse", "HEAD")

	selected, err := resolve(repositoryPath, "refs/heads/main")
	if err != nil {
		t.Fatalf("resolve repository: %v", err)
	}
	if selected.source != repositoryPath {
		t.Fatalf("unexpected source %q", selected.source)
	}
	if selected.ref != "refs/heads/main" {
		t.Fatalf("unexpected ref %q", selected.ref)
	}
	if selected.commit != commit {
		t.Fatalf("expected commit %q, got %q", commit, selected.commit)
	}
	if selected.object != commit {
		t.Fatalf("expected branch object %q, got %q", commit, selected.object)
	}
}

func TestResolvePreservesAnnotatedTagObjectAndPeeledCommit(t *testing.T) {
	repositoryPath := newTestRepository(t)
	gitTestRun(
		t,
		repositoryPath,
		"-c", "user.name=PAW test",
		"-c", "user.email=paw-test.invalid",
		"tag", "--annotate", "release", "--message=release",
	)

	selected, err := resolve(repositoryPath, "refs/tags/release")
	if err != nil {
		t.Fatalf("resolve annotated tag: %v", err)
	}
	if selected.object == selected.commit {
		t.Fatal("annotated tag object was not distinguished from its peeled commit")
	}
	if selected.ref != "refs/tags/release" {
		t.Fatalf("unexpected ref %q", selected.ref)
	}
}

func TestResolveRejectsSubdirectoryRawCommitAndPseudoRefs(t *testing.T) {
	repositoryPath := newTestRepository(t)
	child := filepath.Join(repositoryPath, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(child, "refs/heads/main"); err == nil || !strings.Contains(err.Error(), "root exactly") {
		t.Fatalf("expected exact-root rejection, got %v", err)
	}

	commit := gitTestOutput(t, repositoryPath, "rev-parse", "HEAD")
	if _, err := resolve(repositoryPath, commit); err == nil || !strings.Contains(err.Error(), "branch, tag") {
		t.Fatalf("expected raw-commit rejection, got %v", err)
	}
	for _, pseudoRef := range []string{"HEAD", "@"} {
		if _, err := resolve(repositoryPath, pseudoRef); err == nil ||
			!strings.Contains(err.Error(), "explicit named ref") {
			t.Fatalf("expected %s rejection, got %v", pseudoRef, err)
		}
	}
}

func TestAddRejectsUnsafeDestinationNameBeforeExecution(t *testing.T) {
	err := Add(Request{Context: "paw-local", Name: "../ambient"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "repository name") {
		t.Fatalf("expected destination-name rejection, got %v", err)
	}
}

func TestAddDoesNotDeadlockWhenRemoteRejectsBeforeReadingBundle(t *testing.T) {
	source := newTestRepository(t)
	bin := t.TempDir()
	kubectl := filepath.Join(bin, "kubectl")
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nexit 42\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	result := make(chan error, 1)
	go func() {
		result <- Add(Request{
			Context:  "paw-local",
			Name:     "selected",
			Revision: "refs/heads/main",
			Source:   source,
		}, &bytes.Buffer{}, &bytes.Buffer{})
	}()

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "materialize repository") {
			t.Fatalf("expected remote rejection, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("repository import deadlocked after remote rejection")
	}
}

func TestAddCompletesLargeBundleThroughOSPipe(t *testing.T) {
	source := newTestRepository(t)
	if err := os.WriteFile(filepath.Join(source, "large.bin"), bytes.Repeat([]byte("paw"), 256*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, source, "add", "large.bin")
	gitTestRun(t, source, "-c", "user.name=PAW test", "-c", "user.email=paw-test.invalid", "commit", "--message=large")

	bin := t.TempDir()
	kubectl := filepath.Join(bin, "kubectl")
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout bytes.Buffer
	if err := Add(Request{
		Context:  "paw-local",
		Name:     "selected",
		Revision: "refs/heads/main",
		Source:   source,
	}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("large repository import failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "repository selected materialized at ") {
		t.Fatalf("missing successful materialization message: %q", stdout.String())
	}
}

func TestFinishAddPrefersWorkspaceFailureOverBundlePipeFailure(t *testing.T) {
	kubectlErr := errors.New("destination already exists")
	bundleErr := errors.New("broken pipe")
	err := finishAdd("selected", "commit", kubectlErr, bundleErr, &bytes.Buffer{})
	if !errors.Is(err, kubectlErr) || errors.Is(err, bundleErr) {
		t.Fatalf("expected workspace materialization error, got %v", err)
	}
}

func TestMaterializationImportsOnlySelectedCommittedContent(t *testing.T) {
	for _, ref := range []string{"refs/heads/main", "refs/tags/release"} {
		t.Run(ref, func(t *testing.T) {
			source := newTestRepository(t)
			gitTestRun(t, source, "-c", "user.name=PAW test", "-c", "user.email=paw-test.invalid",
				"tag", "--annotate", "release", "--message=release")
			gitTestRun(t, source, "remote", "add", "origin", "https://example.invalid/selected.git")
			writeTestFile(t, filepath.Join(source, "README.md"), "uncommitted\n")
			writeTestFile(t, filepath.Join(source, "untracked"), "not selected\n")
			selected, err := resolve(source, ref)
			if err != nil {
				t.Fatal(err)
			}
			harness := newMaterializationHarness(t, selected)
			if output, err := harness.command(bytes.NewReader(createTestBundle(t, selected))).CombinedOutput(); err != nil {
				t.Fatalf("materialization failed: %v: %s", err, output)
			}
			if got := gitTestOutput(t, harness.destination, "rev-parse", "HEAD"); got != selected.commit {
				t.Fatalf("expected selected commit %s, got %s", selected.commit, got)
			}
			if got := gitTestOutput(t, harness.destination, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
				t.Fatalf("expected detached checkout, got %s", got)
			}
			if got := gitTestOutput(t, harness.destination, "remote"); got != "" {
				t.Fatalf("unexpected imported remote: %s", got)
			}
			content, err := os.ReadFile(filepath.Join(harness.destination, "README.md"))
			if err != nil || string(content) != "test\n" {
				t.Fatalf("committed content not preserved: %q, %v", content, err)
			}
			if _, err := os.Stat(filepath.Join(harness.destination, "untracked")); !os.IsNotExist(err) {
				t.Fatalf("untracked source content imported: %v", err)
			}
			writeTestFile(t, filepath.Join(harness.destination, "README.md"), "workspace edit\n")
			if got := gitTestOutput(t, harness.destination, "status", "--porcelain"); got != "M README.md" {
				t.Fatalf("expected writable tracked checkout, got %q", got)
			}
			harness.assertCleaned(t)
		})
	}
}

func TestMaterializationRejectsInvalidBundlesAndCleansUp(t *testing.T) {
	source := newTestRepository(t)
	selected, err := resolve(source, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	bundle := createTestBundle(t, selected)
	gitTestRun(t, source, "branch", "other")
	multipleRefs, err := exec.Command("git", "-C", source, "bundle", "create", "-", selected.ref, "refs/heads/other").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		data      []byte
		selection selection
	}{
		{"malformed", []byte("not a Git bundle\n"), selected},
		{"truncated", bundle[:len(bundle)-16], selected},
		{"multiple-refs", multipleRefs, selected},
		{"wrong-ref", bundle, selection{ref: "refs/heads/other", object: selected.object, commit: selected.commit}},
		{"wrong-object", bundle, selection{ref: selected.ref, object: strings.Repeat("0", 40), commit: selected.commit}},
		{"wrong-commit", bundle, selection{ref: selected.ref, object: selected.object, commit: strings.Repeat("0", 40)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newMaterializationHarness(t, test.selection)
			if output, err := harness.command(bytes.NewReader(test.data)).CombinedOutput(); err == nil {
				t.Fatalf("invalid bundle accepted: %s", output)
			}
			if _, err := os.Lstat(harness.destination); !os.IsNotExist(err) {
				t.Fatalf("failed import left a destination: %v", err)
			}
			harness.assertCleaned(t)
		})
	}
}

func TestMaterializationRefusesExistingDestinations(t *testing.T) {
	source := newTestRepository(t)
	selected, err := resolve(source, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"directory", "dangling-symlink"} {
		t.Run(kind, func(t *testing.T) {
			harness := newMaterializationHarness(t, selected)
			if kind == "directory" {
				if err := os.Mkdir(harness.destination, 0o755); err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filepath.Join(harness.destination, "preserved"), "original")
			} else if err := os.Symlink("missing", harness.destination); err != nil {
				t.Fatal(err)
			}
			output, err := harness.command(bytes.NewReader(createTestBundle(t, selected))).CombinedOutput()
			if err == nil || !strings.Contains(string(output), "already exists") {
				t.Fatalf("expected existing destination refusal: %v: %s", err, output)
			}
			if kind == "directory" {
				if content, err := os.ReadFile(filepath.Join(harness.destination, "preserved")); err != nil || string(content) != "original" {
					t.Fatalf("existing content changed: %q, %v", content, err)
				}
			} else if target, err := os.Readlink(harness.destination); err != nil || target != "missing" {
				t.Fatalf("existing symlink changed: %q, %v", target, err)
			}
			harness.assertCleaned(t)
		})
	}
}

func TestMaterializationDrainsBundleBeforeExistingDestinationCheck(t *testing.T) {
	source := newTestRepository(t)
	selected, err := resolve(source, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	harness := newMaterializationHarness(t, selected)
	if err := os.Mkdir(harness.destination, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(harness.destination, "preserved"), "original")

	command := harness.command(nil)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	payload := append(createTestBundle(t, selected), bytes.Repeat([]byte("padding"), 256*1024)...)
	if _, err := stdin.Write(payload); err != nil {
		t.Fatalf("materializer stopped reading before draining the bundle: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil || !strings.Contains(output.String(), "already exists") {
		t.Fatalf("expected existing destination refusal, got %v: %s", err, output.String())
	}
	if content, err := os.ReadFile(filepath.Join(harness.destination, "preserved")); err != nil || string(content) != "original" {
		t.Fatalf("existing content changed: %q, %v", content, err)
	}
	harness.assertCleaned(t)
}

func TestMaterializationCleansBundleWhenStagingCreationFails(t *testing.T) {
	harness := newMaterializationHarness(t, selection{})
	if err := os.Remove(harness.work); err != nil {
		t.Fatal(err)
	}
	if output, err := harness.command(strings.NewReader("")).CombinedOutput(); err == nil {
		t.Fatalf("expected staging creation failure: %s", output)
	}
	harness.assertCleaned(t)
}

func TestMaterializationRefusesConcurrentDestination(t *testing.T) {
	source := newTestRepository(t)
	selected, err := resolve(source, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	harness := newMaterializationHarness(t, selected)

	// Make the destination appear at the exact moment the script moves its
	// staging tree into place: a PATH-shadowing mv creates it, then defers
	// to the real mv, whose --no-clobber must leave staging untouched. This
	// exercises the race deterministically instead of polling for it.
	realMv, err := exec.LookPath("mv")
	if err != nil {
		t.Fatal(err)
	}
	shadow := t.TempDir()
	wrapper := "#!/bin/sh\nmkdir -p '" + harness.destination + "' && printf concurrent > '" +
		filepath.Join(harness.destination, "preserved") + "'\nexec '" + realMv + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shadow, "mv"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	command := harness.command(bytes.NewReader(createTestBundle(t, selected)))
	command.Env = append(os.Environ(), "PATH="+shadow+string(os.PathListSeparator)+os.Getenv("PATH"))

	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "appeared during materialization") {
		t.Fatalf("expected concurrent destination refusal: %v: %s", err, output)
	}
	if content, err := os.ReadFile(filepath.Join(harness.destination, "preserved")); err != nil || string(content) != "concurrent" {
		t.Fatalf("concurrent destination changed: %q, %v", content, err)
	}
	harness.assertCleaned(t)
}

func TestMaterializationIgnoresRecipientTemplatesAndCheckoutFilters(t *testing.T) {
	source := newTestRepository(t)
	writeTestFile(t, filepath.Join(source, ".gitattributes"), "README.md filter=unexpected\n")
	gitTestRun(t, source, "add", ".gitattributes")
	gitTestRun(t, source, "-c", "user.name=PAW test", "-c", "user.email=paw-test.invalid", "commit", "--message=attributes")
	selected, err := resolve(source, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	harness := newMaterializationHarness(t, selected)
	template := t.TempDir()
	writeTestFile(t, filepath.Join(template, "config"), "[remote \"unexpected\"]\n\turl = https://example.invalid/unexpected.git\n")
	if err := os.Mkdir(filepath.Join(template, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(template, "hooks", "post-checkout")
	writeTestFile(t, hook, "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "gitconfig")
	writeTestFile(t, config, "[filter \"unexpected\"]\n\tsmudge = false\n\trequired = true\n")
	command := harness.command(bytes.NewReader(createTestBundle(t, selected)))
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+config, "GIT_TEMPLATE_DIR="+template)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("recipient configuration affected import: %v: %s", err, output)
	}
	if got := gitTestOutput(t, harness.destination, "remote"); got != "" {
		t.Fatalf("recipient template introduced a remote: %s", got)
	}
	harness.assertCleaned(t)
}

type materializationHarness struct {
	work        string
	temporary   string
	destination string
	selected    selection
}

func newMaterializationHarness(t *testing.T, selected selection) materializationHarness {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	return materializationHarness{work: work, temporary: t.TempDir(), destination: filepath.Join(work, "selected"), selected: selected}
}

func (h materializationHarness) command(stdin io.Reader) *exec.Cmd {
	// Execute the production script with only its container paths relocated.
	script := strings.ReplaceAll(materializeScript, "/workspace/work", h.work)
	script = strings.ReplaceAll(script, "/tmp/paw-repository.", filepath.Join(h.temporary, "paw-repository."))
	command := exec.Command("sh", "-ceu", script, "paw-repository", "selected", h.selected.ref, h.selected.object, h.selected.commit)
	command.Stdin = stdin
	return command
}

func (h materializationHarness) assertCleaned(t *testing.T) {
	t.Helper()
	for _, pattern := range []string{filepath.Join(h.work, ".paw-repository.*"), filepath.Join(h.temporary, "paw-repository.*")} {
		entries, err := filepath.Glob(pattern)
		if err != nil || len(entries) != 0 {
			t.Fatalf("temporary materialization artifacts remain: %v, %v", entries, err)
		}
	}
}

func createTestBundle(t *testing.T, selected selection) []byte {
	t.Helper()
	output, err := exec.Command("git", "-C", selected.source, "bundle", "create", "-", selected.ref).Output()
	if err != nil {
		t.Fatalf("create test bundle: %v", err)
	}
	return output
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestRepository(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_TEMPLATE_DIR", t.TempDir())
	repositoryPath := t.TempDir()
	gitTestRun(t, repositoryPath, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repositoryPath, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, repositoryPath, "add", "README.md")
	gitTestRun(
		t,
		repositoryPath,
		"-c", "user.name=PAW test",
		"-c", "user.email=paw-test.invalid",
		"commit", "--message=initial",
	)
	canonical, err := filepath.EvalSymlinks(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func gitTestRun(t *testing.T, repositoryPath string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", repositoryPath}, args...)
	if output, err := exec.Command("git", commandArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func gitTestOutput(t *testing.T, repositoryPath string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", repositoryPath}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
