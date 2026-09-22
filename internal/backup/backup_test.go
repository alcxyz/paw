package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var testMetadata = Metadata{
	Image:    "registry.invalid/paw@sha256:" + strings.Repeat("a", 64),
	ImageID:  "docker-pullable://registry.invalid/paw@sha256:" + strings.Repeat("b", 64),
	Profile:  "core",
	Provider: "codex",
}

func TestRoundTripCompleteWorkspaceContent(t *testing.T) {
	state := t.TempDir()
	work := t.TempDir()
	writeFile(t, filepath.Join(state, "threads", "session.bin"), []byte{0, 1, 2, 0xff}, 0o640)
	writeFile(t, filepath.Join(work, "selected", "tracked.txt"), []byte("tracked\n"), 0o644)
	writeFile(t, filepath.Join(work, "selected", "untracked.txt"), []byte("untracked\n"), 0o600)
	writeFile(t, filepath.Join(work, "selected", ".gitignore"), []byte("ignored.bin\n"), 0o644)
	writeFile(t, filepath.Join(work, "selected", "ignored.bin"), []byte{0, 0xfe, 0xfd, 0}, 0o600)
	writeFile(t, filepath.Join(work, "selected", "run"), []byte("#!/bin/sh\n"), 0o751)
	writeFile(t, filepath.Join(work, "selected", strings.Repeat("long-name-", 16)), []byte("pax path\n"), 0o640)
	if err := os.Chmod(filepath.Join(work, "selected"), 0o710); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0o2770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(state, "threads"), 0o2775); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tracked.txt", filepath.Join(work, "selected", "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("selected/ignored.bin", filepath.Join(work, "binary-link")); err != nil {
		t.Fatal(err)
	}

	archive := writeTestArchive(t, state, work)
	gotMetadata, err := Verify(context.Background(), bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("verify backup: %v", err)
	}
	if gotMetadata != testMetadata {
		t.Fatalf("metadata = %#v, want %#v", gotMetadata, testMetadata)
	}

	restoredState := emptyDirectory(t)
	restoredWork := emptyDirectory(t)
	if err := os.Chmod(restoredState, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(restoredWork, 0o710); err != nil {
		t.Fatal(err)
	}
	gotMetadata, err = Restore(context.Background(), bytes.NewReader(archive), RestoreRequest{
		StateRoot: restoredState,
		WorkRoot:  restoredWork,
	})
	if err != nil {
		t.Fatalf("restore backup: %v", err)
	}
	if gotMetadata != testMetadata {
		t.Fatalf("restored metadata = %#v, want %#v", gotMetadata, testMetadata)
	}
	assertTreesEqual(t, state, restoredState)
	assertTreesEqual(t, work, restoredWork)
	assertMode(t, restoredState, 0o750)
	assertMode(t, restoredWork, 0o710)
}

func TestWriteRejectsUnsupportedSourceWithoutLeakingNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes are not available")
	}
	state := t.TempDir()
	work := t.TempDir()
	secretName := "private-fifo-name"
	path := filepath.Join(work, secretName)
	if err := runMkfifo(path); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	var archive bytes.Buffer
	err := Write(context.Background(), &archive, WriteRequest{Metadata: testMetadata, StateRoot: state, WorkRoot: work})
	if err == nil || strings.Contains(err.Error(), secretName) {
		t.Fatalf("unsupported source error = %v", err)
	}
}

func TestWritePropagatesDestinationFailureAndCancellation(t *testing.T) {
	state := t.TempDir()
	work := t.TempDir()
	writeFile(t, filepath.Join(state, "payload"), bytes.Repeat([]byte("x"), 4096), 0o600)

	err := Write(context.Background(), &failingWriter{remaining: 700}, WriteRequest{
		Metadata: testMetadata, StateRoot: state, WorkRoot: work,
	})
	if err == nil {
		t.Fatal("destination failure was accepted")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Write(ctx, io.Discard, WriteRequest{Metadata: testMetadata, StateRoot: state, WorkRoot: work})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("cancelled write error = %v", err)
	}
}

func TestRestoreRequiresDistinctEmptyRoots(t *testing.T) {
	state := emptyDirectory(t)
	work := emptyDirectory(t)
	writeFile(t, filepath.Join(work, "existing"), []byte("keep"), 0o600)
	for _, request := range []RestoreRequest{
		{StateRoot: state, WorkRoot: work},
		{StateRoot: state, WorkRoot: state},
		{StateRoot: "relative", WorkRoot: emptyDirectory(t)},
	} {
		if _, err := Restore(context.Background(), bytes.NewReader(nil), request); err == nil {
			t.Fatalf("accepted restore roots %#v", request)
		}
	}
	content, err := os.ReadFile(filepath.Join(work, "existing"))
	if err != nil || string(content) != "keep" {
		t.Fatal("restore precondition changed existing content")
	}
}

func TestPartialRestoreIsRetainedWithRestrictivePermissions(t *testing.T) {
	state := t.TempDir()
	work := t.TempDir()
	writeFile(t, filepath.Join(state, "first"), []byte("inspect me"), 0o777)
	writeFile(t, filepath.Join(work, "second"), []byte("later"), 0o777)
	archive := writeTestArchive(t, state, work)
	completion := bytes.Index(archive, []byte(completionName))
	if completion < 0 {
		t.Fatal("completion record not found")
	}
	archive = archive[:completion]

	restoredState := emptyDirectory(t)
	restoredWork := emptyDirectory(t)
	if _, err := Restore(context.Background(), bytes.NewReader(archive), RestoreRequest{
		StateRoot: restoredState, WorkRoot: restoredWork,
	}); err == nil {
		t.Fatal("truncated archive was accepted")
	}
	info, err := os.Stat(filepath.Join(restoredState, "first"))
	if err != nil {
		t.Fatalf("partial output was not retained: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("partial file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestVerifyRejectsIncompleteAndCorruptedContent(t *testing.T) {
	state := t.TempDir()
	work := t.TempDir()
	writeFile(t, filepath.Join(state, "payload"), []byte("integrity-marker"), 0o600)
	archive := writeTestArchive(t, state, work)

	truncated := archive[:len(archive)-2048]
	if _, err := Verify(context.Background(), bytes.NewReader(truncated)); err == nil {
		t.Fatal("truncated archive was accepted")
	}
	corrupt := bytes.Clone(archive)
	index := bytes.Index(corrupt, []byte("integrity-marker"))
	if index < 0 {
		t.Fatal("payload not found")
	}
	corrupt[index] ^= 0xff
	if _, err := Verify(context.Background(), bytes.NewReader(corrupt)); err == nil {
		t.Fatal("corrupted content was accepted")
	}
	withTrailing := append(bytes.Clone(archive), 'x')
	if _, err := Verify(context.Background(), bytes.NewReader(withTrailing)); err == nil {
		t.Fatal("trailing data was accepted")
	}
}

func TestVerifyRejectsUnknownFormatVersion(t *testing.T) {
	archive := writeTestArchive(t, t.TempDir(), t.TempDir())
	current := []byte(`"formatVersion":1`)
	unknown := []byte(`"formatVersion":2`)
	archive = bytes.Replace(archive, current, unknown, 1)
	if _, err := Verify(context.Background(), bytes.NewReader(archive)); err == nil {
		t.Fatal("unknown backup format version was accepted")
	}
}

func TestRejectsMaliciousArchiveEntries(t *testing.T) {
	tests := map[string][]tarEntry{
		"absolute path": {
			directoryEntry("state"), directoryEntry("work"), fileEntry("/escape", "x"),
		},
		"traversal": {
			directoryEntry("state"), fileEntry("state/../escape", "x"), directoryEntry("work"),
		},
		"symlink escape": {
			directoryEntry("state"), symlinkEntry("state/out", "../../escape"), directoryEntry("work"),
		},
		"child through symlink": {
			directoryEntry("state"), symlinkEntry("state/link", "."), fileEntry("state/link/file", "x"), directoryEntry("work"),
		},
		"chained symlink escape": {
			directoryEntry("state"), symlinkEntry("state/foo", "."), symlinkEntry("state/link", "foo/../outside"), directoryEntry("work"),
		},
		"hardlink": {
			directoryEntry("state"), {name: "state/hard", typeflag: tar.TypeLink, linkname: "../../escape"}, directoryEntry("work"),
		},
		"special file": {
			directoryEntry("state"), {name: "state/device", typeflag: tar.TypeChar}, directoryEntry("work"),
		},
		"duplicate": {
			directoryEntry("state"), fileEntry("state/file", "one"), fileEntry("state/file", "two"), directoryEntry("work"),
		},
		"file-directory collision": {
			fileEntry("state", "x"), directoryEntry("work"),
		},
		"missing parent": {
			directoryEntry("state"), fileEntry("state/missing/file", "x"), directoryEntry("work"),
		},
		"setuid mode": {
			directoryEntry("state"), {name: "state/tool", typeflag: tar.TypeReg, mode: 0o4755}, directoryEntry("work"),
		},
		"setgid regular file": {
			directoryEntry("state"), {name: "state/tool", typeflag: tar.TypeReg, mode: 0o2755}, directoryEntry("work"),
		},
	}

	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			archive := maliciousArchive(t, entries)
			if _, err := Verify(context.Background(), bytes.NewReader(archive)); err == nil {
				t.Fatal("malicious archive was accepted by Verify")
			}
			state := emptyDirectory(t)
			work := emptyDirectory(t)
			if _, err := Restore(context.Background(), bytes.NewReader(archive), RestoreRequest{
				StateRoot: state, WorkRoot: work,
			}); err == nil {
				t.Fatal("malicious archive was accepted by Restore")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(state), "escape")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("archive wrote outside restore roots")
			}
		})
	}
}

func writeTestArchive(t *testing.T, state, work string) []byte {
	t.Helper()
	var archive bytes.Buffer
	if err := Write(context.Background(), &archive, WriteRequest{
		Metadata: testMetadata, StateRoot: state, WorkRoot: work,
	}); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	return archive.Bytes()
}

func emptyDirectory(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func writeFile(t *testing.T, name string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
}

func assertTreesEqual(t *testing.T, wantRoot, gotRoot string) {
	t.Helper()
	err := filepath.Walk(wantRoot, func(wantPath string, wantInfo os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(wantRoot, wantPath)
		if err != nil {
			return err
		}
		gotPath := filepath.Join(gotRoot, relative)
		gotInfo, err := os.Lstat(gotPath)
		if err != nil {
			return err
		}
		modeMask := os.ModeType | os.ModePerm | os.ModeSetgid | os.ModeSticky
		if relative != "." && wantInfo.Mode()&modeMask != gotInfo.Mode()&modeMask {
			t.Errorf("mode mismatch for %s: got %v, want %v", relative, gotInfo.Mode(), wantInfo.Mode())
		}
		switch {
		case wantInfo.Mode().IsRegular():
			want, err := os.ReadFile(wantPath)
			if err != nil {
				return err
			}
			got, err := os.ReadFile(gotPath)
			if err != nil {
				return err
			}
			if !bytes.Equal(got, want) {
				t.Errorf("content mismatch for %s", relative)
			}
		case wantInfo.Mode()&os.ModeSymlink != 0:
			want, err := os.Readlink(wantPath)
			if err != nil {
				return err
			}
			got, err := os.Readlink(gotPath)
			if err != nil {
				return err
			}
			if got != want {
				t.Errorf("link mismatch for %s: got %q, want %q", relative, got, want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertMode(t *testing.T, name string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want.Perm() {
		t.Fatalf("mode for %s = %o, want %o", name, info.Mode().Perm(), want.Perm())
	}
}

type failingWriter struct {
	remaining int
}

func (writer *failingWriter) Write(content []byte) (int, error) {
	if writer.remaining <= 0 {
		return 0, errors.New("synthetic failure")
	}
	if len(content) > writer.remaining {
		content = content[:writer.remaining]
	}
	written := len(content)
	writer.remaining -= written
	if writer.remaining == 0 {
		return written, errors.New("synthetic failure")
	}
	return written, nil
}

type tarEntry struct {
	name     string
	typeflag byte
	mode     int64
	linkname string
	content  string
}

func directoryEntry(name string) tarEntry {
	return tarEntry{name: name, typeflag: tar.TypeDir, mode: 0o700}
}

func fileEntry(name, content string) tarEntry {
	return tarEntry{name: name, typeflag: tar.TypeReg, mode: 0o600, content: content}
}

func symlinkEntry(name, target string) tarEntry {
	return tarEntry{name: name, typeflag: tar.TypeSymlink, mode: 0o777, linkname: target}
}

func maliciousArchive(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	tw := tar.NewWriter(&output)
	metadata, err := json.Marshal(metadataDocument{
		FormatVersion: formatVersion,
		Image:         testMetadata.Image, ImageID: testMetadata.ImageID,
		Profile: testMetadata.Profile, Provider: testMetadata.Provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeControlFile(tw, metadataName, metadata); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		header := &tar.Header{
			Name: entry.name, Typeflag: entry.typeflag, Mode: entry.mode,
			Linkname: entry.linkname, Size: int64(len(entry.content)),
		}
		if entry.typeflag != tar.TypeReg {
			header.Size = 0
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(entry.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func runMkfifo(path string) error {
	return exec.Command("mkfifo", path).Run()
}
