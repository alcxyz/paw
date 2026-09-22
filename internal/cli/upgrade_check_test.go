package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

type upgradeCheckFixture struct {
	namespace   namespaceResource
	statefulSet statefulSetResource
	claims      persistentVolumeClaimList
	contract    configMapResource
	lock        *configMapResource
	pods        podList
}

func TestWorkspaceUpgradeCheckUsesOnlyPointInTimeReads(t *testing.T) {
	fixture := validUpgradeCheckFixture()
	// Container runtimes may normalize the name while preserving the pod spec.
	fixture.pods.Items[0].Status.ContainerStatuses[0].Image = "docker.io/library/paw-core:dev"
	var calls [][]string
	deps := workspaceTestDependencies(upgradeCheckRunner(t, &fixture, &calls, "", ""))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exit := runWithDependencies(
		[]string{"workspace", "upgrade-check", "--adapter", "kubernetes", "--context", "paw-local"},
		&stdout,
		&stderr,
		deps,
	)
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("upgrade check failed: exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if stdout.String() != "workspace upgrade preflight passed at this point in time; no changes were made and this does not authorize an upgrade\n" {
		t.Fatalf("unexpected success output: %q", stdout.String())
	}
	if len(calls) != 6 {
		t.Fatalf("expected six bounded reads, got %d: %v", len(calls), calls)
	}
	for _, args := range calls {
		if countArgument(args, "get") != 1 || !slices.Contains(args, "--request-timeout=10s") ||
			slices.ContainsFunc(args, func(argument string) bool {
				return slices.Contains([]string{"apply", "create", "delete", "exec", "patch", "replace", "scale"}, argument)
			}) {
			t.Fatalf("upgrade preflight issued a non-read-only command: %v", args)
		}
	}
}

func TestWorkspaceUpgradeCheckFailsClosed(t *testing.T) {
	tests := map[string]func(*upgradeCheckFixture){
		"unowned namespace": func(fixture *upgradeCheckFixture) {
			delete(fixture.namespace.Metadata.Labels, workspaceOwnerLabel)
		},
		"legacy storage": func(fixture *upgradeCheckFixture) {
			delete(fixture.statefulSet.Metadata.Annotations, "paw.alc.xyz/storage-layout")
		},
		"ephemeral work": func(fixture *upgradeCheckFixture) {
			volume := &fixture.statefulSet.Spec.Template.Spec.Volumes[1]
			volume.PersistentVolumeClaim = nil
			volume.EmptyDir = &struct{}{}
		},
		"missing claim": func(fixture *upgradeCheckFixture) {
			fixture.claims.Items = fixture.claims.Items[:1]
		},
		"unbound claim": func(fixture *upgradeCheckFixture) {
			fixture.claims.Items[1].Status.Phase = "Pending"
		},
		"stale rollout": func(fixture *upgradeCheckFixture) {
			fixture.statefulSet.Status.UpdateRevision = "workspace-new"
		},
		"unready writer": func(fixture *upgradeCheckFixture) {
			fixture.pods.Items[0].Status.ContainerStatuses[0].Ready = false
		},
		"wrong controller": func(fixture *upgradeCheckFixture) {
			fixture.pods.Items[0].Metadata.OwnerReferences[0].UID = "other"
		},
		"missing canonical writer": func(fixture *upgradeCheckFixture) {
			fixture.pods.Items[0].Metadata.Name = "other-writer"
			fixture.pods.Items[0].Spec.Volumes = nil
		},
		"subpath expression": func(fixture *upgradeCheckFixture) {
			fixture.statefulSet.Spec.Template.Spec.Containers[0].VolumeMounts[0].SubPathExpr = "other"
		},
		"additional init container": func(fixture *upgradeCheckFixture) {
			fixture.statefulSet.Spec.Template.Spec.InitContainers = []containerSpec{{Name: "extra"}}
		},
		"competing claim user": func(fixture *upgradeCheckFixture) {
			other := podResource{Metadata: objectMetadata{Name: "other", Namespace: workspaceNamespace, UID: "other-uid"}}
			other.Status.Phase = "Running"
			other.Spec.Volumes = []volumeSpec{{
				Name:                  "state",
				PersistentVolumeClaim: &persistentVolumeClaim{ClaimName: workspaceStateClaim},
			}}
			fixture.pods.Items = append(fixture.pods.Items, other)
		},
		"lifecycle lock": func(fixture *upgradeCheckFixture) {
			fixture.lock = &configMapResource{Metadata: objectMetadata{Name: workspaceLifecycleLock}}
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := validUpgradeCheckFixture()
			mutate(&fixture)
			var calls [][]string
			deps := workspaceTestDependencies(upgradeCheckRunner(t, &fixture, &calls, "", ""))
			assertUpgradeCheckFailure(t, deps)
		})
	}
}

func TestWorkspaceUpgradeCheckSuppressesMalformedAndChildDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name      string
		malformed string
		fail      string
	}{
		{name: "malformed response", malformed: "statefulset/workspace"},
		{name: "command failure", fail: "persistentvolumeclaim/workspace-state"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := validUpgradeCheckFixture()
			var calls [][]string
			deps := workspaceTestDependencies(upgradeCheckRunner(t, &fixture, &calls, test.malformed, test.fail))
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exit := runWithDependencies(
				[]string{"workspace", "upgrade-check", "--adapter", "minikube", "--context", "paw-local"},
				&stdout,
				&stderr,
				deps,
			)
			if exit != 1 || stdout.Len() != 0 ||
				!strings.HasPrefix(stderr.String(), "paw: workspace upgrade preflight failed: ") ||
				!strings.HasSuffix(stderr.String(), "; no changes were made\n") ||
				strings.Contains(stderr.String(), "untrusted-secret-diagnostic") {
				t.Fatalf("unsafe failure output: exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
		})
	}
}

func TestWorkspaceUpgradeCheckAcceptsOnlyAdapterAndContext(t *testing.T) {
	for _, args := range [][]string{
		{"workspace", "upgrade-check", "--adapter", "minikube"},
		{"workspace", "upgrade-check", "--adapter", "minikube", "--context", "paw-local", "--json"},
		{"workspace", "upgrade-check", "--adapter", "minikube", "--context", "paw-local", "--profile", "core"},
	} {
		var stderr bytes.Buffer
		if exit := runWithDependencies(args, io.Discard, &stderr, workspaceTestDependencies(nil)); exit != 2 {
			t.Fatalf("unsafe options accepted: args=%v exit=%d stderr=%q", args, exit, stderr.String())
		}
	}
}

func assertUpgradeCheckFailure(t *testing.T, deps dependencies) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exit := runWithDependencies(
		[]string{"workspace", "upgrade-check", "--adapter", "kubernetes", "--context", "paw-local"},
		&stdout,
		&stderr,
		deps,
	)
	if exit != 1 || stdout.Len() != 0 ||
		!strings.HasPrefix(stderr.String(), "paw: workspace upgrade preflight failed: ") ||
		!strings.HasSuffix(stderr.String(), "; no changes were made\n") {
		t.Fatalf("check did not fail closed: exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func upgradeCheckRunner(
	t *testing.T,
	fixture *upgradeCheckFixture,
	calls *[][]string,
	malformedResource string,
	failingResource string,
) commandRunner {
	t.Helper()
	return func(name string, args []string, stdout, stderr io.Writer) error {
		if name != "kubectl" {
			t.Fatalf("unexpected command %q", name)
		}
		*calls = append(*calls, slices.Clone(args))
		joined := strings.Join(args, " ")
		if failingResource != "" && strings.Contains(joined, failingResource) {
			_, _ = io.WriteString(stderr, "untrusted-secret-diagnostic")
			return errors.New("untrusted-secret-diagnostic")
		}
		if malformedResource != "" && strings.Contains(joined, malformedResource) {
			_, _ = io.WriteString(stdout, `{untrusted-secret-diagnostic`)
			return nil
		}

		var value any
		switch {
		case slices.Contains(args, "namespace/"+workspaceNamespace):
			value = fixture.namespace
		case slices.Contains(args, "statefulset/workspace"):
			value = fixture.statefulSet
		case slices.Contains(args, "persistentvolumeclaim/"+workspaceStateClaim):
			value = fixture.claims
		case slices.Contains(args, "configmap/workspace-contract"):
			value = fixture.contract
		case slices.Contains(args, "configmap/"+workspaceLifecycleLock):
			if fixture.lock == nil {
				return nil
			}
			value = *fixture.lock
		case slices.Contains(args, "pods"):
			value = fixture.pods
		default:
			t.Fatalf("unexpected kubectl arguments: %v", args)
		}
		encoder := json.NewEncoder(stdout)
		if err := encoder.Encode(value); err != nil {
			t.Fatal(err)
		}
		return nil
	}
}

func validUpgradeCheckFixture() upgradeCheckFixture {
	controller := true
	replicas := int32(1)
	annotations := map[string]string{
		"paw.alc.xyz/profile":        "core",
		"paw.alc.xyz/provider":       "codex",
		"paw.alc.xyz/single-writer":  "true",
		"paw.alc.xyz/storage-layout": workspaceStorageLayout,
	}
	templateAnnotations := map[string]string{
		"paw.alc.xyz/profile":        "core",
		"paw.alc.xyz/provider":       "codex",
		"paw.alc.xyz/storage-layout": workspaceStorageLayout,
	}
	podSpec := podSpec{
		Containers: []containerSpec{{
			Name:  "t3",
			Image: "registry.example/paw/paw-codex@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			VolumeMounts: []volumeMount{
				{Name: "state", MountPath: "/workspace/state"},
				{Name: "work", MountPath: "/workspace/work"},
				{Name: "tmp", MountPath: "/tmp"},
				{Name: "contract", MountPath: "/etc/paw", ReadOnly: true},
			},
		}},
		Volumes: []volumeSpec{
			{Name: "state", PersistentVolumeClaim: &persistentVolumeClaim{ClaimName: workspaceStateClaim}},
			{Name: "work", PersistentVolumeClaim: &persistentVolumeClaim{ClaimName: workspaceWorkClaim}},
			{Name: "tmp", EmptyDir: &struct{}{}},
			{Name: "contract", ConfigMap: &configMapVolume{Name: "workspace-contract"}},
		},
	}

	fixture := upgradeCheckFixture{
		namespace: namespaceResource{Metadata: objectMetadata{
			Name: workspaceNamespace, UID: "namespace-uid", ResourceVersion: "1",
			Labels: map[string]string{workspaceOwnerLabel: "paw"},
		}},
		statefulSet: statefulSetResource{Metadata: objectMetadata{
			Name: "workspace", Namespace: workspaceNamespace, UID: "statefulset-uid",
			ResourceVersion: "2", Generation: 3, Annotations: annotations,
		}},
		contract: configMapResource{
			Metadata: objectMetadata{Name: "workspace-contract", Namespace: workspaceNamespace, UID: "contract-uid", ResourceVersion: "5"},
			Data:     map[string]string{"profile": "core", "provider": "codex", "state-policy": "retain-until-destroy"},
		},
	}
	fixture.statefulSet.Spec.Replicas = &replicas
	fixture.statefulSet.Spec.Template = podTemplate{
		Metadata: objectMetadata{Annotations: templateAnnotations},
		Spec:     podSpec,
	}
	fixture.statefulSet.Status.ObservedGeneration = 3
	fixture.statefulSet.Status.Replicas = 1
	fixture.statefulSet.Status.CurrentReplicas = 1
	fixture.statefulSet.Status.ReadyReplicas = 1
	fixture.statefulSet.Status.UpdatedReplicas = 1
	fixture.statefulSet.Status.CurrentRevision = "workspace-current"
	fixture.statefulSet.Status.UpdateRevision = "workspace-current"

	for index, name := range []string{workspaceStateClaim, workspaceWorkClaim} {
		claim := persistentVolumeClaimResource{Metadata: objectMetadata{
			Name: name, Namespace: workspaceNamespace, UID: name + "-uid", ResourceVersion: []string{"6", "7"}[index],
			Labels:      map[string]string{workspaceOwnerLabel: "paw"},
			Annotations: map[string]string{"paw.alc.xyz/state-policy": "retain-until-destroy"},
		}}
		claim.Spec.AccessModes = []string{"ReadWriteOnce"}
		claim.Spec.VolumeName = "pv-" + name
		claim.Status.Phase = "Bound"
		fixture.claims.Items = append(fixture.claims.Items, claim)
	}

	pod := podResource{
		Metadata: objectMetadata{
			Name: "workspace-0", Namespace: workspaceNamespace, UID: "pod-uid", ResourceVersion: "8",
			Annotations: templateAnnotations,
			OwnerReferences: []ownerReference{{
				APIVersion: "apps/v1", Kind: "StatefulSet", Name: "workspace", UID: "statefulset-uid", Controller: &controller,
			}},
		},
		Spec: podSpec,
	}
	pod.Status.Phase = "Running"
	pod.Status.Conditions = []podCondition{{Type: "Ready", Status: "True"}}
	pod.Status.ContainerStatuses = []containerStatus{{
		Name: "t3", Image: podSpec.Containers[0].Image, ImageID: "containerd://sha256:bbbb", Ready: true,
	}}
	fixture.pods.Items = []podResource{pod}
	return fixture
}

func countArgument(args []string, target string) int {
	count := 0
	for _, argument := range args {
		if argument == target {
			count++
		}
	}
	return count
}
