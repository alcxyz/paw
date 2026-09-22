package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	backuparchive "github.com/alcxyz/paw/internal/backup"
)

func TestWorkspaceBackupReservesOutputBeforeKubernetesMutation(t *testing.T) {
	output := t.TempDir()
	calls := 0
	deps := workspaceTestDependencies(func(string, []string, io.Writer, io.Writer) error {
		calls++
		return nil
	})
	deps.runInput = func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error {
		calls++
		return nil
	}
	var stderr bytes.Buffer
	exit := runWithDependencies([]string{
		"workspace", "backup", "--adapter", "minikube", "--context", "paw-local", "--output", output,
	}, io.Discard, &stderr, deps)
	if exit != 1 || calls != 0 || !strings.Contains(stderr.String(), "no cluster changes") {
		t.Fatalf("existing output was not rejected before cluster access: exit=%d calls=%d stderr=%q", exit, calls, stderr.String())
	}
}

func TestWorkspaceBackupLifecycleAndArtifact(t *testing.T) {
	harness := newBackupHarness(t, true)
	output := filepath.Join(t.TempDir(), "backup")
	var stdout, stderr bytes.Buffer
	exit := runWithDependencies([]string{
		"workspace", "backup", "--adapter", "minikube", "--context", "paw-local", "--output", output,
	}, &stdout, &stderr, harness.dependencies())
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("backup failed: exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if harness.lock != nil || harness.helper != nil || !harness.running || harness.patchStops != 1 || harness.patchStarts != 1 {
		t.Fatalf("unexpected final lifecycle state: lock=%v helper=%v running=%v stops=%d starts=%d",
			harness.lock != nil, harness.helper != nil, harness.running, harness.patchStops, harness.patchStarts)
	}
	artifact, sidecar, err := openVerifiedBackup(context.Background(), output)
	if err != nil {
		t.Fatalf("published artifact did not verify: %v", err)
	}
	artifact.Close()
	if sidecar.StateClaimUID == "" || sidecar.WorkClaimUID == "" || sidecar.ArchiveSHA256 == "" {
		t.Fatalf("published metadata omits storage identity or checksum: %#v", sidecar)
	}
	if !harness.sawReadOnlyHelper || !harness.sawExport || !harness.sawCASPatch || !harness.sawPreconditionDelete {
		t.Fatalf("safe orchestration contract missing: %#v", harness)
	}
}

func TestWorkspaceBackupFailureRetainsStoppedLockAndDiscardsStage(t *testing.T) {
	harness := newBackupHarness(t, true)
	harness.failExport = true
	output := filepath.Join(t.TempDir(), "backup")
	var stdout, stderr bytes.Buffer
	exit := runWithDependencies([]string{
		"workspace", "backup", "--adapter", "minikube", "--context", "paw-local", "--output", output,
	}, &stdout, &stderr, harness.dependencies())
	if exit != 1 || stdout.Len() != 0 || harness.lock == nil || harness.running || harness.patchStarts != 0 || harness.helper == nil {
		t.Fatalf("backup failure did not fail closed: exit=%d stdout=%q stderr=%q lock=%v running=%v starts=%d helper=%v",
			exit, stdout.String(), stderr.String(), harness.lock != nil, harness.running, harness.patchStarts, harness.helper != nil)
	}
	if info, err := os.Stat(output); err != nil || !info.IsDir() {
		t.Fatalf("private partial output was not retained safely: info=%v err=%v", info, err)
	}
	if !strings.Contains(stderr.String(), "lifecycle lock") || !strings.Contains(stderr.String(), "retained") || strings.Contains(stderr.String(), "fixture-content") {
		t.Fatalf("unsafe failure diagnostics: %q", stderr.String())
	}
}

func TestWorkspaceRestoreValidatesTargetAndLeavesItStopped(t *testing.T) {
	source := newBackupHarness(t, true)
	input := createHarnessBackup(t, source)
	restore := newBackupHarness(t, false)
	var stdout, stderr bytes.Buffer
	exit := runWithDependencies([]string{
		"workspace", "restore", "--adapter", "minikube", "--context", "paw-local",
		"--input", input, "--confirm-empty-restore",
	}, &stdout, &stderr, restore.dependencies())
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("restore failed: exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if restore.running || restore.patchStarts != 0 || restore.lock != nil || restore.helper != nil || !restore.sawWritableHelper || !restore.sawEmptyCheck || !restore.sawImport {
		t.Fatalf("restore did not remain cleanly stopped: %#v", restore)
	}
	content, err := os.ReadFile(filepath.Join(restore.stateRoot, "state.txt"))
	if err != nil || string(content) != "fixture-content" {
		t.Fatalf("state content was not restored: content=%q err=%v", content, err)
	}
}

func TestWorkspaceRestoreNonemptyCheckRunsBeforeStreaming(t *testing.T) {
	source := newBackupHarness(t, true)
	input := createHarnessBackup(t, source)
	restore := newBackupHarness(t, false)
	restore.failEmptyCheck = true
	var stderr bytes.Buffer
	exit := runWithDependencies([]string{
		"workspace", "restore", "--adapter", "minikube", "--context", "paw-local",
		"--input", input, "--confirm-empty-restore",
	}, io.Discard, &stderr, restore.dependencies())
	if exit != 1 || !restore.sawEmptyCheck || restore.sawImport || restore.lock == nil || restore.helper == nil || restore.running {
		t.Fatalf("nonempty restore did not fail before streaming: exit=%d emptyCheck=%v import=%v lock=%v helper=%v running=%v stderr=%q",
			exit, restore.sawEmptyCheck, restore.sawImport, restore.lock != nil, restore.helper != nil, restore.running, stderr.String())
	}
	entries, err := os.ReadDir(restore.stateRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed empty check changed restore state: entries=%v err=%v", entries, err)
	}
}

func TestWorkspaceBackupRestoreOptionsFailClosed(t *testing.T) {
	digest := strings.Repeat("a", 64)
	tests := [][]string{
		{"workspace", "backup", "--adapter", "kubernetes", "--context", "x", "--output", "/tmp/new"},
		{"workspace", "backup", "--adapter", "minikube", "--context", "x", "--output", "/tmp/new", "--helper-image-ref", "registry.example/paw/paw-backup-helper@sha256:" + digest},
		{"workspace", "restore", "--adapter", "minikube", "--context", "x", "--input", "/tmp/backup"},
		{"workspace", "backup", "--adapter", "kubernetes", "--context", "x", "--output", "/tmp/new", "--helper-image-ref", "paw-backup-helper:latest"},
	}
	for _, args := range tests {
		var stderr bytes.Buffer
		if exit := runWithDependencies(args, io.Discard, &stderr, workspaceTestDependencies(nil)); exit != 2 {
			t.Fatalf("unsafe arguments accepted: args=%v exit=%d stderr=%q", args, exit, stderr.String())
		}
	}
}

type backupHarness struct {
	t                     *testing.T
	fixture               upgradeCheckFixture
	originalPod           podResource
	running               bool
	lock                  *configMapResource
	helper                *podResource
	stateRoot             string
	workRoot              string
	failExport            bool
	failEmptyCheck        bool
	patchStops            int
	patchStarts           int
	sawCASPatch           bool
	sawPreconditionDelete bool
	sawReadOnlyHelper     bool
	sawWritableHelper     bool
	sawExport             bool
	sawEmptyCheck         bool
	sawImport             bool
}

func newBackupHarness(t *testing.T, running bool) *backupHarness {
	t.Helper()
	fixture := validUpgradeCheckFixture()
	stateRoot := filepath.Join(t.TempDir(), "state")
	workRoot := filepath.Join(t.TempDir(), "work")
	if err := os.Mkdir(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	h := &backupHarness{t: t, fixture: fixture, originalPod: fixture.pods.Items[0], running: running, stateRoot: stateRoot, workRoot: workRoot}
	if running {
		if err := os.WriteFile(filepath.Join(stateRoot, "state.txt"), []byte("fixture-content"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workRoot, "work.txt"), []byte("work-content"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		h.setReplicas(0)
	}
	return h
}

func (h *backupHarness) dependencies() dependencies {
	deps := workspaceTestDependencies(h.run)
	deps.runInput = h.runInput
	return deps
}

func (h *backupHarness) run(name string, args []string, stdout, _ io.Writer) error {
	if name != "kubectl" {
		h.t.Fatalf("unexpected command %q", name)
	}
	switch {
	case slices.Contains(args, "patch"):
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, `"path":"/metadata/uid"`) || !strings.Contains(joined, `"path":"/metadata/resourceVersion"`) {
			h.t.Fatalf("patch lacks CAS tests: %v", args)
		}
		h.sawCASPatch = true
		if strings.Contains(joined, `"value":0`) && h.running {
			h.patchStops++
			h.setReplicas(0)
		} else if strings.Contains(joined, `"value":1`) && !h.running {
			h.patchStarts++
			h.setReplicas(1)
		} else {
			return errors.New("invalid replica transition")
		}
		return nil
	case slices.Contains(args, "wait"), slices.Contains(args, "rollout"):
		return nil
	case slices.Contains(args, "exec") && slices.Contains(args, "check-empty"):
		h.sawEmptyCheck = true
		if h.failEmptyCheck {
			return errors.New("injected nonempty claims")
		}
		return nil
	case slices.Contains(args, "get"):
		return h.writeGet(args, stdout)
	default:
		h.t.Fatalf("unexpected kubectl args: %v", args)
	}
	return nil
}

func (h *backupHarness) runInput(ctx context.Context, name string, args []string, stdin io.Reader, stdout, _ io.Writer) error {
	if name != "kubectl" {
		h.t.Fatalf("unexpected input command %q", name)
	}
	joined := strings.Join(args, " ")
	switch {
	case slices.Contains(args, "create"):
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(stdin).Decode(&raw); err != nil {
			h.t.Fatal(err)
		}
		var kind string
		if err := json.Unmarshal(raw["kind"], &kind); err != nil {
			h.t.Fatal(err)
		}
		switch kind {
		case "ConfigMap":
			var lock configMapResource
			if err := remarshal(raw, &lock); err != nil {
				h.t.Fatal(err)
			}
			lock.Metadata.UID, lock.Metadata.ResourceVersion = "lock-uid", "lock-rv"
			h.lock = &lock
			return json.NewEncoder(stdout).Encode(lock)
		case "Pod":
			var pod podResource
			if err := remarshal(raw, &pod); err != nil {
				h.t.Fatal(err)
			}
			pod.Metadata.UID, pod.Metadata.ResourceVersion = "helper-uid", "helper-create-rv"
			pod.Status.Phase = "Running"
			pod.Status.Conditions = []podCondition{{Type: "Ready", Status: "True"}}
			pod.Status.ContainerStatuses = []containerStatus{{Name: "backup", Image: pod.Spec.Containers[0].Image, ImageID: "containerd://helper", Ready: true}}
			h.helper = &pod
			readOnly := pod.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly
			h.sawReadOnlyHelper = h.sawReadOnlyHelper || readOnly
			h.sawWritableHelper = h.sawWritableHelper || !readOnly
			return json.NewEncoder(stdout).Encode(pod)
		default:
			h.t.Fatalf("unexpected manifest kind %q", kind)
		}
	case slices.Contains(args, "exec") && slices.Contains(args, "export"):
		h.sawExport = true
		if h.failExport {
			return errors.New("injected export failure")
		}
		metadata := backuparchive.Metadata{Image: h.fixture.statefulSet.Spec.Template.Spec.Containers[0].Image, ImageID: h.originalPod.Status.ContainerStatuses[0].ImageID, Profile: "core", Provider: "codex"}
		return backuparchive.Write(ctx, stdout, backuparchive.WriteRequest{Metadata: metadata, StateRoot: h.stateRoot, WorkRoot: h.workRoot})
	case slices.Contains(args, "exec") && slices.Contains(args, "import"):
		h.sawImport = true
		_, err := backuparchive.Restore(ctx, stdin, backuparchive.RestoreRequest{StateRoot: h.stateRoot, WorkRoot: h.workRoot})
		return err
	case slices.Contains(args, "delete"):
		h.sawPreconditionDelete = true
		body, err := io.ReadAll(stdin)
		if err != nil || !bytes.Contains(body, []byte(`"preconditions"`)) {
			return errors.New("missing delete preconditions")
		}
		if strings.Contains(joined, "/pods/") {
			h.helper = nil
		} else if strings.Contains(joined, "/configmaps/") {
			h.lock = nil
		} else {
			h.t.Fatalf("unexpected delete: %v", args)
		}
		return nil
	default:
		h.t.Fatalf("unexpected input kubectl args: %v", args)
	}
	return nil
}

func (h *backupHarness) writeGet(args []string, stdout io.Writer) error {
	var value any
	switch {
	case slices.Contains(args, "namespace/"+workspaceNamespace):
		value = h.fixture.namespace
	case slices.Contains(args, "statefulset/workspace"):
		value = h.fixture.statefulSet
	case slices.Contains(args, "persistentvolumeclaim/"+workspaceStateClaim):
		value = h.fixture.claims
	case slices.Contains(args, "configmap/workspace-contract"):
		value = h.fixture.contract
	case slices.Contains(args, "configmap/"+workspaceLifecycleLock):
		if h.lock == nil {
			return nil
		}
		value = *h.lock
	case h.helper != nil && slices.Contains(args, "pod/"+h.helper.Metadata.Name):
		h.helper.Metadata.ResourceVersion = "helper-ready-rv"
		value = *h.helper
	case slices.Contains(args, "pods"):
		var pods podList
		if h.running {
			pods.Items = append(pods.Items, h.originalPod)
		}
		if h.helper != nil {
			pods.Items = append(pods.Items, *h.helper)
		}
		value = pods
	default:
		return fmt.Errorf("unexpected get: %v", args)
	}
	return json.NewEncoder(stdout).Encode(value)
}

func (h *backupHarness) setReplicas(replicas int32) {
	h.running = replicas == 1
	h.fixture.statefulSet.Spec.Replicas = &replicas
	h.fixture.statefulSet.Metadata.Generation++
	h.fixture.statefulSet.Metadata.ResourceVersion = fmt.Sprintf("sts-rv-%d-%d", replicas, h.fixture.statefulSet.Metadata.Generation)
	h.fixture.statefulSet.Status.ObservedGeneration = h.fixture.statefulSet.Metadata.Generation
	h.fixture.statefulSet.Status.Replicas = replicas
	h.fixture.statefulSet.Status.CurrentReplicas = replicas
	h.fixture.statefulSet.Status.ReadyReplicas = replicas
	h.fixture.statefulSet.Status.UpdatedReplicas = replicas
}

func remarshal(source any, target any) error {
	data, err := json.Marshal(source)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func createHarnessBackup(t *testing.T, source *backupHarness) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "input")
	output, err := createBackupOutput(directory)
	if err != nil {
		t.Fatal(err)
	}
	file, err := output.createArtifact()
	if err != nil {
		t.Fatal(err)
	}
	metadata := backuparchive.Metadata{Image: source.fixture.statefulSet.Spec.Template.Spec.Containers[0].Image, ImageID: source.originalPod.Status.ContainerStatuses[0].ImageID, Profile: "core", Provider: "codex"}
	if err := backuparchive.Write(context.Background(), file, backuparchive.WriteRequest{Metadata: metadata, StateRoot: source.stateRoot, WorkRoot: source.workRoot}); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	verified, digest, size, err := verifyBackupArtifact(context.Background(), output.tempPath)
	if err != nil || verified != metadata {
		t.Fatalf("test backup verification: %v", err)
	}
	if err := output.publish(makeBackupSidecar(backupWorkspace{namespace: source.fixture.namespace, statefulSet: source.fixture.statefulSet, claims: source.fixture.claims}, metadata, digest, size)); err != nil {
		t.Fatal(err)
	}
	return directory
}
