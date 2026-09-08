package repository

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func exportScriptCommand(t *testing.T, repositoryPath, base string) *exec.Cmd {
	t.Helper()
	script := strings.ReplaceAll(exportScript, "/workspace/work", filepath.Dir(repositoryPath))
	return exec.Command("sh", "-ceu", script, "paw-export-test", filepath.Base(repositoryPath), base)
}

func TestExportScriptRoundTrip(t *testing.T) {
	repo := newTestRepository(t)
	writeTestFile(t, filepath.Join(repo, "binary"), "\x00before\xff")
	writeTestFile(t, filepath.Join(repo, "removed"), "remove me\n")
	gitTestRun(t, repo, "add", ".")
	gitTestRun(t, repo, "-c", "user.name=PAW test", "-c", "user.email=paw-test.invalid", "commit", "-m", "base")
	base := gitTestOutput(t, repo, "rev-parse", "HEAD")
	writeTestFile(t, filepath.Join(repo, "README.md"), "committed change\n")
	gitTestRun(t, repo, "add", "README.md")
	gitTestRun(t, repo, "-c", "user.name=PAW test", "-c", "user.email=paw-test.invalid", "commit", "-m", "edit")
	writeTestFile(t, filepath.Join(repo, "binary"), "\x00after\xfe")
	writeTestFile(t, filepath.Join(repo, "new-file"), "staged new file\n")
	gitTestRun(t, repo, "add", "new-file")
	if err := os.Remove(filepath.Join(repo, "removed")); err != nil {
		t.Fatal(err)
	}
	patch, err := exportScriptCommand(t, repo, base).Output()
	if err != nil {
		t.Fatalf("export synthetic repository: %v", err)
	}
	if !bytes.Contains(patch, []byte("GIT binary patch")) {
		t.Fatal("binary patch missing")
	}
	review := filepath.Join(t.TempDir(), "review")
	gitTestRun(t, repo, "clone", "--quiet", "--no-hardlinks", repo, review)
	gitTestRun(t, review, "checkout", "--quiet", "--detach", base)
	apply := exec.Command("git", "-C", review, "apply", "--binary", "-")
	apply.Stdin = bytes.NewReader(patch)
	if err := apply.Run(); err != nil {
		t.Fatalf("apply synthetic patch: %v", err)
	}
	for path, want := range map[string]string{"README.md": "committed change\n", "binary": "\x00after\xfe", "new-file": "staged new file\n"} {
		got, err := os.ReadFile(filepath.Join(review, path))
		if err != nil || string(got) != want {
			t.Fatalf("round-trip mismatch for %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(review, "removed")); !os.IsNotExist(err) {
		t.Fatal("deletion missing from patch")
	}
}

func TestExportScriptRejectsOmittedContentAndMissingBase(t *testing.T) {
	for _, kind := range []string{"untracked", "ignored", "missing-base"} {
		t.Run(kind, func(t *testing.T) {
			repo := newTestRepository(t)
			base := gitTestOutput(t, repo, "rev-parse", "HEAD")
			switch kind {
			case "untracked":
				writeTestFile(t, filepath.Join(repo, "untracked"), "synthetic\n")
			case "ignored":
				writeTestFile(t, filepath.Join(repo, ".gitignore"), "ignored\n")
				gitTestRun(t, repo, "add", ".gitignore")
				writeTestFile(t, filepath.Join(repo, "ignored"), "synthetic\n")
			case "missing-base":
				base = strings.Repeat("a", 40)
			}
			if _, err := exportScriptCommand(t, repo, base).Output(); err == nil {
				t.Fatal("expected export refusal")
			}
		})
	}
}

func fakeExportKubectl(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	path := filepath.Join(bin, "kubectl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func exportRequestForTest(output string) ExportRequest {
	return ExportRequest{Context: "pilot-test", Name: "selected", BaseCommit: strings.Repeat("a", 40), Output: output}
}

func TestExportPrivateArtifactAndNoClobber(t *testing.T) {
	fakeExportKubectl(t, "printf 'synthetic patch\\n'\n")
	dir := t.TempDir()
	output := filepath.Join(dir, "review.patch")
	request := exportRequestForTest(output)
	if err := Export(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact not private: %v", err)
	}
	if err := Export(context.Background(), request); err == nil {
		t.Fatal("overwrote existing output")
	}
	content, err := os.ReadFile(output)
	if err != nil || string(content) != "synthetic patch\n" {
		t.Fatal("existing artifact changed")
	}
	link := filepath.Join(dir, "symlink.patch")
	if err := os.Symlink(filepath.Join(dir, "absent-target"), link); err != nil {
		t.Fatal(err)
	}
	if err := Export(context.Background(), exportRequestForTest(link)); err == nil {
		t.Fatal("accepted dangling output symlink")
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatal("removed pre-existing output symlink")
	}
}

func TestExportFailureAndCancellationDoNotPublish(t *testing.T) {
	for _, kind := range []string{"failure", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			body := "printf 'synthetic payload\\n'\nprintf 'synthetic diagnostic\\n' >&2\nexit 1\n"
			if kind == "timeout" {
				body = "printf 'synthetic payload\\n'\nexec sleep 10\n"
			}
			fakeExportKubectl(t, body)
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := Export(ctx, exportRequestForTest(filepath.Join(dir, "result.patch")))
			if err == nil || strings.Contains(err.Error(), "synthetic") {
				t.Fatalf("missing failure or leaked child output: %v", err)
			}
			if time.Since(start) > 5*time.Second {
				t.Fatal("export cancellation not bounded")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("partial output remains: %v", err)
			}
		})
	}
}

func TestExportRejectsInvalidSelection(t *testing.T) {
	for _, field := range []string{"context", "name", "base", "output"} {
		t.Run(field, func(t *testing.T) {
			request := exportRequestForTest(filepath.Join(t.TempDir(), "patch"))
			switch field {
			case "context":
				request.Context = ""
			case "name":
				request.Name = "../other"
			case "base":
				request.BaseCommit = "HEAD"
			case "output":
				request.Output = "relative.patch"
			}
			if err := Export(context.Background(), request); err == nil {
				t.Fatal("accepted invalid export request")
			}
		})
	}
}

func TestExportScriptFailsClosedWhenGitInventoryFails(t *testing.T) {
	repo := newTestRepository(t)
	base := gitTestOutput(t, repo, "rev-parse", "HEAD")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	shim := "#!/bin/sh\nfor arg do\n  if test \"$arg\" = ls-files; then exit 1; fi\ndone\nexec \"$PAW_EXPORT_TEST_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAW_EXPORT_TEST_GIT", realGit)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := exportScriptCommand(t, repo, base).Output(); err == nil {
		t.Fatal("failed inventory was accepted as empty")
	}
}

func TestExportRefusesOutputCreatedDuringTransfer(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "result.patch")
	t.Setenv("PAW_EXPORT_TEST_OUTPUT", output)
	fakeExportKubectl(t, "printf 'other owner' > \"$PAW_EXPORT_TEST_OUTPUT\"\nprintf 'synthetic patch'\n")
	if err := Export(context.Background(), exportRequestForTest(output)); err == nil {
		t.Fatal("overwrote concurrent output")
	}
	content, err := os.ReadFile(output)
	if err != nil || string(content) != "other owner" {
		t.Fatal("concurrent artifact was changed or removed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary export artifact not cleaned")
	}
}

func TestExportScriptDisablesExternalDiffAndTextconv(t *testing.T) {
	repo := newTestRepository(t)
	writeTestFile(t, filepath.Join(repo, ".gitattributes"), "README.md diff=unexpected\n")
	gitTestRun(t, repo, "add", ".gitattributes")
	gitTestRun(t, repo, "-c", "user.name=PAW test", "-c", "user.email=paw-test.invalid", "commit", "-m", "attributes")
	base := gitTestOutput(t, repo, "rev-parse", "HEAD")
	gitTestRun(t, repo, "config", "diff.unexpected.command", "false")
	gitTestRun(t, repo, "config", "diff.unexpected.textconv", "false")
	gitTestRun(t, repo, "config", "diff.external", "false")
	gitTestRun(t, repo, "config", "diff.noprefix", "true")
	writeTestFile(t, filepath.Join(repo, "README.md"), "changed\n")
	patch, err := exportScriptCommand(t, repo, base).Output()
	if err != nil || !bytes.Contains(patch, []byte("diff --git a/README.md b/README.md")) {
		t.Fatalf("diff configuration broke export: %v", err)
	}
}

func TestExportScriptRejectsSymlinkedRepository(t *testing.T) {
	repo := newTestRepository(t)
	base := gitTestOutput(t, repo, "rev-parse", "HEAD")
	link := filepath.Join(t.TempDir(), "selected")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	if _, err := exportScriptCommand(t, link, base).Output(); err == nil {
		t.Fatal("accepted symlinked repository")
	}
}

func TestExportScriptRejectsDirtySubmodule(t *testing.T) {
	repo := newTestRepository(t)
	nested := newTestRepository(t)
	gitTestRun(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", nested, "nested")
	gitTestRun(t, repo, "-c", "user.name=PAW test", "-c", "user.email=paw-test.invalid", "commit", "-m", "submodule")
	base := gitTestOutput(t, repo, "rev-parse", "HEAD")
	writeTestFile(t, filepath.Join(repo, "nested", "README.md"), "dirty submodule\n")
	if _, err := exportScriptCommand(t, repo, base).Output(); err == nil {
		t.Fatal("silently omitted dirty submodule")
	}
}
