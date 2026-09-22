package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	deployment "github.com/alcxyz/paw/deploy"
	backuparchive "github.com/alcxyz/paw/internal/backup"
)

const (
	backupArtifactName     = "workspace.paw-backup"
	backupMetadataName     = "metadata.json"
	backupFormatVersion    = "paw-workspace-backup-v1"
	backupHelperLocalImage = "paw-backup-helper:dev"
	backupOperationTimeout = 20 * time.Minute
)

var backupHelperReference = regexp.MustCompile(
	`^(?:localhost|[a-z0-9]+(?:[.-][a-z0-9]+)*)(?::[0-9]+)?` +
		`(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*/paw-backup-helper@sha256:[0-9a-f]{64}$`,
)

type backupWorkspace struct {
	namespace   namespaceResource
	statefulSet statefulSetResource
	claims      persistentVolumeClaimList
	contract    configMapResource
	pods        podList
}

type backupSidecar struct {
	Format         string `json:"format"`
	CreatedAt      string `json:"createdAt"`
	ArchiveSHA256  string `json:"archiveSHA256"`
	ArchiveSize    int64  `json:"archiveSize"`
	Image          string `json:"image"`
	ImageID        string `json:"imageID"`
	Profile        string `json:"profile"`
	Provider       string `json:"provider"`
	NamespaceUID   string `json:"namespaceUID"`
	StatefulSetUID string `json:"statefulSetUID"`
	StateClaimUID  string `json:"stateClaimUID"`
	StateVolume    string `json:"stateVolume"`
	WorkClaimUID   string `json:"workClaimUID"`
	WorkVolume     string `json:"workVolume"`
}

func validateBackupHelperImage(reference string) error {
	if strings.ContainsAny(reference, " \t\r\n") || strings.Contains(reference, "://") ||
		!backupHelperReference.MatchString(reference) {
		return errors.New("backup helper image must be a lowercase fully qualified paw-backup-helper@sha256:<64 lowercase hex characters> reference")
	}
	return nil
}

func runWorkspaceBackup(options workspaceOptions, stdout, stderr io.Writer, deps dependencies) int {
	if !filepath.IsAbs(options.backupOutput) {
		return usageError(stderr, "workspace backup --output must be an absolute new directory")
	}
	output, err := createBackupOutput(options.backupOutput)
	if err != nil {
		writeBackupFailure(stderr, "could not create the private new backup destination; no cluster changes were made")
		return 1
	}
	if err := checkWorkspaceUpgrade(options.context, deps); err != nil {
		writeBackupFailure(stderr, "preflight failed; no cluster changes were made; the private output directory was retained for inspection")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, backupOperationTimeout)
	defer cancel()

	operationID, err := newBackupOperationID()
	if err != nil {
		writeBackupFailure(stderr, "could not create an operation identifier; no cluster changes were made; the private output directory was retained")
		return 1
	}
	lock, err := createBackupLock(ctx, options.context, "backup", operationID, deps)
	if err != nil {
		writeBackupFailure(stderr, "could not acquire the lifecycle lock; inspect the existing lock before retrying; the private output directory was retained")
		return 1
	}
	locked := true
	fail := func(message string) int {
		if locked {
			writeBackupFailure(stderr, message+"; the lifecycle lock and private output directory were retained; inspect the StatefulSet replicas and helper pod before recovery")
		} else {
			writeBackupFailure(stderr, message)
		}
		return 1
	}

	workspace, err := readBackupWorkspace(options.context, lock, true, deps)
	if err != nil {
		return fail("locked workspace revalidation failed")
	}
	original := workspace.statefulSet
	if err := patchWorkspaceReplicas(options.context, original.Metadata, 1, 0, deps); err != nil {
		return fail("writer stop request was not confirmed")
	}
	if err := waitForWorkspacePodDeletion(options.context, deps); err != nil {
		return fail("writer termination was not confirmed")
	}
	paused, err := readStoppedBackupWorkspace(options.context, lock, deps)
	if err != nil {
		return fail("exclusive stopped-volume state was not confirmed")
	}
	if !sameBackupWorkspaceIdentity(workspace, paused, true) {
		return fail("workspace storage identity changed while stopping the writer")
	}

	helperImage := backupHelperLocalImage
	if options.adapter == deployment.AdapterKubernetes {
		helperImage = options.helperImageRef
	}
	helper, err := createBackupHelper(ctx, options.context, operationID, helperImage, options.adapter, true, deps)
	if err != nil {
		return fail("backup helper creation was not confirmed")
	}
	if err := waitForBackupHelper(options.context, helper.Metadata.Name, deps); err != nil {
		return fail("backup helper readiness was not confirmed")
	}
	helper, err = confirmBackupHelper(options.context, helper, operationID, helperImage, true, deps)
	if err != nil {
		return fail("backup helper identity was not confirmed")
	}

	runtimeMetadata, ok := runningWorkspaceMetadata(workspace)
	if !ok {
		return fail("runtime metadata changed during backup preparation")
	}
	temp, err := output.createArtifact()
	if err != nil {
		return fail("could not stage the private backup artifact")
	}
	exportArgs := backupStreamKubectlArgs(options.context,
		"exec", helper.Metadata.Name, "--", "/bin/paw-backup-helper", "export",
		"--image", runtimeMetadata.Image,
		"--image-id", runtimeMetadata.ImageID,
		"--profile", runtimeMetadata.Profile,
		"--provider", runtimeMetadata.Provider,
	)
	streamCtx, streamCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer streamCancel()
	if deps.runInput == nil || deps.runInput(streamCtx, "kubectl", exportArgs, nil, temp, io.Discard) != nil {
		_ = temp.Close()
		return fail("backup stream did not complete")
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fail("backup artifact could not be synchronized")
	}
	if err := temp.Close(); err != nil {
		return fail("backup artifact could not be closed")
	}

	verified, digest, size, err := verifyBackupArtifact(ctx, output.tempPath)
	if err != nil || verified != runtimeMetadata {
		return fail("backup artifact verification failed")
	}
	sidecar := makeBackupSidecar(paused, runtimeMetadata, digest, size)
	if err := output.publish(sidecar); err != nil {
		return fail("verified backup artifact could not be published")
	}
	if err := deleteBackupPod(ctx, options.context, helper.Metadata, deps); err != nil {
		return fail("backup helper deletion was not confirmed")
	}
	if err := waitForBackupHelperDeletion(options.context, helper.Metadata.Name, deps); err != nil {
		return fail("backup helper termination was not confirmed")
	}
	beforeResume, err := readStoppedBackupWorkspace(options.context, lock, deps)
	if err != nil || !sameBackupWorkspaceIdentity(workspace, beforeResume, true) {
		return fail("writer resume preconditions were not confirmed")
	}
	current := beforeResume.statefulSet
	if current.Metadata.UID != original.Metadata.UID || current.Spec.Replicas == nil || *current.Spec.Replicas != 0 {
		return fail("writer resume preconditions were not confirmed")
	}
	if err := patchWorkspaceReplicas(options.context, current.Metadata, 0, 1, deps); err != nil {
		return fail("writer resume request was not confirmed")
	}
	if err := waitForWorkspaceReadiness(options.context, deps); err != nil {
		return fail("writer readiness was not confirmed")
	}
	resumed, err := readBackupWorkspace(options.context, lock, true, deps)
	if err != nil || !sameBackupWorkspaceIdentity(workspace, resumed, true) {
		return fail("resumed workspace validation failed")
	}
	if err := deleteBackupLock(ctx, options.context, lock.Metadata, deps); err != nil {
		return fail("lifecycle lock release was not confirmed")
	}
	if err := waitForBackupLockDeletion(options.context, deps); err != nil {
		return fail("lifecycle lock release was not confirmed")
	}
	locked = false
	fmt.Fprintln(stdout, "workspace backup completed and verified; the writer is ready and the lifecycle lock was released")
	return 0
}

func runWorkspaceRestore(options workspaceOptions, stdout, stderr io.Writer, deps dependencies) int {
	if !filepath.IsAbs(options.backupInput) {
		return usageError(stderr, "workspace restore --input must be an absolute backup directory")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, backupOperationTimeout)
	defer cancel()

	archive, sidecar, err := openVerifiedBackup(ctx, options.backupInput)
	if err != nil {
		writeBackupFailure(stderr, "input backup validation failed; no cluster changes were made")
		return 1
	}
	defer archive.Close()
	workspace, err := readRestoreWorkspace(options.context, configMapResource{}, true, deps)
	if err != nil {
		writeBackupFailure(stderr, "offline restore preflight failed; no changes were made")
		return 1
	}
	if !backupMatchesRestoreTarget(sidecar, workspace) {
		writeBackupFailure(stderr, "backup runtime metadata does not match the stopped restore target; no changes were made")
		return 1
	}

	operationID, err := newBackupOperationID()
	if err != nil {
		writeBackupFailure(stderr, "could not create an operation identifier; no changes were made")
		return 1
	}
	lock, err := createBackupLock(ctx, options.context, "restore", operationID, deps)
	if err != nil {
		writeBackupFailure(stderr, "could not acquire the lifecycle lock; inspect the existing lock before retrying")
		return 1
	}
	fail := func(message string) int {
		writeBackupFailure(stderr, message+"; the workspace remains stopped and the lifecycle lock was retained; inspect the helper pod before recovery")
		return 1
	}
	lockedWorkspace, err := readRestoreWorkspace(options.context, lock, true, deps)
	if err != nil || !sameBackupWorkspaceIdentity(workspace, lockedWorkspace, false) {
		return fail("locked offline workspace revalidation failed")
	}

	helperImage := backupHelperLocalImage
	if options.adapter == deployment.AdapterKubernetes {
		helperImage = options.helperImageRef
	}
	helper, err := createBackupHelper(ctx, options.context, operationID, helperImage, options.adapter, false, deps)
	if err != nil {
		return fail("restore helper creation was not confirmed")
	}
	if err := waitForBackupHelper(options.context, helper.Metadata.Name, deps); err != nil {
		return fail("restore helper readiness was not confirmed")
	}
	helper, err = confirmBackupHelper(options.context, helper, operationID, helperImage, false, deps)
	if err != nil {
		return fail("restore helper identity was not confirmed")
	}
	boundWorkspace, err := readRestoreWorkspaceAllowHelper(options.context, lock, helper, deps)
	if err != nil || !sameBackupWorkspaceIdentity(workspace, boundWorkspace, false) {
		return fail("restore claim binding was not confirmed")
	}
	if err := deps.runCommand("kubectl", backupHelperCommandArgs(options.context,
		"exec", helper.Metadata.Name, "--", "/bin/paw-backup-helper", "check-empty"), io.Discard, io.Discard); err != nil {
		return fail("restore claims are not empty; no archive bytes were sent and existing contents were not erased")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return fail("input backup could not be reopened")
	}
	importArgs := backupStreamKubectlArgs(options.context,
		"exec", "--stdin", helper.Metadata.Name, "--", "/bin/paw-backup-helper", "import",
	)
	streamCtx, streamCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer streamCancel()
	if deps.runInput == nil || deps.runInput(streamCtx, "kubectl", importArgs, archive, io.Discard, io.Discard) != nil {
		return fail("restore stream did not complete; never erase the partial claims")
	}
	if err := deleteBackupPod(ctx, options.context, helper.Metadata, deps); err != nil {
		return fail("restore helper deletion was not confirmed")
	}
	if err := waitForBackupHelperDeletion(options.context, helper.Metadata.Name, deps); err != nil {
		return fail("restore helper termination was not confirmed")
	}
	if _, err := readStoppedBackupWorkspace(options.context, lock, deps); err != nil {
		return fail("post-restore stopped state was not confirmed")
	}
	if err := deleteBackupLock(ctx, options.context, lock.Metadata, deps); err != nil {
		return fail("lifecycle lock release was not confirmed")
	}
	if err := waitForBackupLockDeletion(options.context, deps); err != nil {
		return fail("lifecycle lock release was not confirmed")
	}
	fmt.Fprintln(stdout, "workspace restore completed; the workspace remains stopped and no writer was started")
	return 0
}

func writeBackupFailure(stderr io.Writer, message string) {
	fmt.Fprintln(stderr, "paw: workspace backup/restore:", message)
}

func newBackupOperationID() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func createBackupLock(ctx context.Context, contextName, operation, operationID string, deps dependencies) (configMapResource, error) {
	manifest := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      workspaceLifecycleLock,
			"namespace": workspaceNamespace,
			"labels":    map[string]string{workspaceOwnerLabel: "paw"},
		},
		"data": map[string]string{"operation": operation, "operation-id": operationID},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil || deps.runInput == nil {
		return configMapResource{}, errors.New("lock manifest unavailable")
	}
	var response bytesBuffer
	if err := deps.runInput(ctx, "kubectl", backupKubectlArgs(contextName, "create", "-f", "-", "--output=json"),
		strings.NewReader(string(encoded)), &response, io.Discard); err != nil {
		return configMapResource{}, errors.New("lock creation failed")
	}
	var lock configMapResource
	if json.Unmarshal(response.Bytes(), &lock) != nil || !validBackupLock(lock, operation, operationID) {
		return configMapResource{}, errors.New("invalid lock response")
	}
	return lock, nil
}

// bytesBuffer is the narrow buffer surface used here, which keeps all command
// diagnostics discarded while retaining trusted Kubernetes JSON responses.
type bytesBuffer struct{ data []byte }

func (b *bytesBuffer) Write(value []byte) (int, error) {
	b.data = append(b.data, value...)
	return len(value), nil
}
func (b *bytesBuffer) Bytes() []byte { return b.data }

func validBackupLock(lock configMapResource, operation, operationID string) bool {
	return lock.Metadata.Name == workspaceLifecycleLock && lock.Metadata.Namespace == workspaceNamespace &&
		lock.Metadata.UID != "" && lock.Metadata.ResourceVersion != "" && lock.Metadata.DeletionTimestamp == nil &&
		lock.Metadata.Labels[workspaceOwnerLabel] == "paw" && lock.Data["operation"] == operation &&
		lock.Data["operation-id"] == operationID
}

func readBackupWorkspace(contextName string, expectedLock configMapResource, running bool, deps dependencies) (backupWorkspace, error) {
	return readBackupWorkspaceState(contextName, expectedLock, running, false, "", deps)
}

func readBackupWorkspaceState(contextName string, expectedLock configMapResource, running, allowPending bool, allowedHelperUID string, deps dependencies) (backupWorkspace, error) {
	var result backupWorkspace
	if err := getUpgradeResource(contextName, "", []string{"namespace/" + workspaceNamespace}, &result.namespace, deps); err != nil {
		return result, err
	}
	if result.namespace.Metadata.Name != workspaceNamespace || result.namespace.Metadata.UID == "" ||
		result.namespace.Metadata.ResourceVersion == "" || result.namespace.Metadata.DeletionTimestamp != nil ||
		result.namespace.Metadata.Labels[workspaceOwnerLabel] != "paw" {
		return result, errors.New("invalid namespace")
	}
	if err := getUpgradeResource(contextName, workspaceNamespace, []string{"statefulset/workspace"}, &result.statefulSet, deps); err != nil {
		return result, err
	}
	if !hasPersistentV1Layout(result.statefulSet) {
		return result, errors.New("legacy workspace")
	}
	if running {
		if !validUpgradeStatefulSet(result.statefulSet) {
			return result, errors.New("invalid running writer")
		}
	} else if !validStoppedBackupStatefulSet(result.statefulSet) {
		return result, errors.New("workspace is not stopped")
	}
	if err := getUpgradeResource(contextName, workspaceNamespace, []string{
		"persistentvolumeclaim/" + workspaceStateClaim,
		"persistentvolumeclaim/" + workspaceWorkClaim,
	}, &result.claims, deps); err != nil || (!allowPending && !validUpgradeClaims(result.claims)) ||
		(allowPending && !validRestoreClaims(result.claims)) {
		return result, errors.New("invalid persistent claims")
	}
	if err := getUpgradeResource(contextName, workspaceNamespace, []string{"configmap/workspace-contract"}, &result.contract, deps); err != nil ||
		!validUpgradeContract(result.contract, result.statefulSet) {
		return result, errors.New("invalid workspace contract")
	}
	var lock configMapResource
	var raw bytesBuffer
	if err := deps.runCommand("kubectl", backupKubectlArgs(contextName,
		"get", "configmap/"+workspaceLifecycleLock, "--ignore-not-found=true", "--output=json"), &raw, io.Discard); err != nil {
		return result, errors.New("lock lookup failed")
	}
	if len(strings.TrimSpace(string(raw.Bytes()))) != 0 {
		if json.Unmarshal(raw.Bytes(), &lock) != nil {
			return result, errors.New("invalid lock")
		}
	}
	if expectedLock.Metadata.UID == "" {
		if lock.Metadata.UID != "" {
			return result, errors.New("lifecycle lock present")
		}
	} else if lock.Metadata.UID != expectedLock.Metadata.UID || lock.Metadata.ResourceVersion != expectedLock.Metadata.ResourceVersion ||
		lock.Data["operation-id"] != expectedLock.Data["operation-id"] {
		return result, errors.New("lifecycle lock changed")
	}
	if err := getUpgradeResource(contextName, workspaceNamespace, []string{"pods"}, &result.pods, deps); err != nil {
		return result, err
	}
	if running {
		if !validUpgradePods(result.pods, result.statefulSet) {
			return result, errors.New("invalid running pods")
		}
	} else if liveWorkspaceClaimConsumerExcept(result.pods, allowedHelperUID) {
		return result, errors.New("live claim consumer")
	}
	return result, nil
}

func readStoppedBackupWorkspace(contextName string, expectedLock configMapResource, deps dependencies) (backupWorkspace, error) {
	return readBackupWorkspace(contextName, expectedLock, false, deps)
}

func readRestoreWorkspace(contextName string, expectedLock configMapResource, allowPending bool, deps dependencies) (backupWorkspace, error) {
	return readBackupWorkspaceState(contextName, expectedLock, false, allowPending, "", deps)
}

func readRestoreWorkspaceAllowHelper(contextName string, expectedLock configMapResource, helper podResource, deps dependencies) (backupWorkspace, error) {
	return readBackupWorkspaceState(contextName, expectedLock, false, false, helper.Metadata.UID, deps)
}

func validStoppedBackupStatefulSet(statefulSet statefulSetResource) bool {
	metadata := statefulSet.Metadata
	profile := metadata.Annotations["paw.alc.xyz/profile"]
	provider := metadata.Annotations["paw.alc.xyz/provider"]
	_, selectionErr := deployment.ImageName(deployment.Selection{Profile: profile, Provider: provider})
	return selectionErr == nil && metadata.Name == "workspace" && metadata.Namespace == workspaceNamespace &&
		metadata.UID != "" && metadata.ResourceVersion != "" && metadata.Generation > 0 && metadata.DeletionTimestamp == nil &&
		metadata.Annotations["paw.alc.xyz/single-writer"] == "true" &&
		statefulSet.Spec.Replicas != nil && *statefulSet.Spec.Replicas == 0 &&
		statefulSet.Status.ObservedGeneration == metadata.Generation && statefulSet.Status.Replicas == 0 &&
		statefulSet.Status.CurrentReplicas == 0 && statefulSet.Status.ReadyReplicas == 0 &&
		statefulSet.Status.UpdatedReplicas == 0 && validUpgradePodSpec(statefulSet.Spec.Template.Spec)
}

func liveWorkspaceClaimConsumerExcept(pods podList, allowedUID string) bool {
	for _, pod := range pods.Items {
		if pod.Metadata.UID != allowedUID && pod.Status.Phase != "Succeeded" && pod.Status.Phase != "Failed" && podMountsWorkspaceClaim(pod.Spec) {
			return true
		}
	}
	return false
}

func validRestoreClaims(list persistentVolumeClaimList) bool {
	if len(list.Items) != 2 {
		return false
	}
	wanted := map[string]bool{workspaceStateClaim: true, workspaceWorkClaim: true}
	for _, claim := range list.Items {
		metadata := claim.Metadata
		if !wanted[metadata.Name] || metadata.Namespace != workspaceNamespace || metadata.UID == "" ||
			metadata.ResourceVersion == "" || metadata.DeletionTimestamp != nil ||
			metadata.Labels[workspaceOwnerLabel] != "paw" ||
			metadata.Annotations["paw.alc.xyz/state-policy"] != "retain-until-destroy" ||
			len(claim.Spec.AccessModes) != 1 || claim.Spec.AccessModes[0] != "ReadWriteOnce" {
			return false
		}
		switch claim.Status.Phase {
		case "Pending":
			if claim.Spec.VolumeName != "" {
				return false
			}
		case "Bound":
			if claim.Spec.VolumeName == "" {
				return false
			}
		default:
			return false
		}
		delete(wanted, metadata.Name)
	}
	return len(wanted) == 0
}

func patchWorkspaceReplicas(contextName string, metadata objectMetadata, from, to int32, deps dependencies) error {
	patch, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": metadata.UID},
		{"op": "test", "path": "/metadata/resourceVersion", "value": metadata.ResourceVersion},
		{"op": "test", "path": "/spec/replicas", "value": from},
		{"op": "replace", "path": "/spec/replicas", "value": to},
	})
	if err != nil {
		return err
	}
	return deps.runCommand("kubectl", backupKubectlArgs(contextName,
		"patch", "statefulset/workspace", "--type=json", "--patch", string(patch)), io.Discard, io.Discard)
}

func waitForWorkspacePodDeletion(contextName string, deps dependencies) error {
	return deps.runCommand("kubectl", []string{"--context", contextName, "--namespace", workspaceNamespace,
		"--request-timeout=130s", "wait", "--for=delete", "pod/workspace-0", "--timeout=120s"}, io.Discard, io.Discard)
}

func waitForWorkspaceReadiness(contextName string, deps dependencies) error {
	return deps.runCommand("kubectl", []string{"--context", contextName, "--namespace", workspaceNamespace,
		"--request-timeout=130s", "rollout", "status", "statefulset/workspace", "--timeout=120s"}, io.Discard, io.Discard)
}

func getBackupStatefulSet(contextName string, deps dependencies) (statefulSetResource, error) {
	var statefulSet statefulSetResource
	err := getUpgradeResource(contextName, workspaceNamespace, []string{"statefulset/workspace"}, &statefulSet, deps)
	return statefulSet, err
}

func runningWorkspaceMetadata(workspace backupWorkspace) (backuparchive.Metadata, bool) {
	for _, pod := range workspace.pods.Items {
		if pod.Metadata.Name != "workspace-0" {
			continue
		}
		if len(pod.Status.ContainerStatuses) != 1 {
			return backuparchive.Metadata{}, false
		}
		return backuparchive.Metadata{
			Image:    workspace.statefulSet.Spec.Template.Spec.Containers[0].Image,
			ImageID:  pod.Status.ContainerStatuses[0].ImageID,
			Profile:  workspace.statefulSet.Metadata.Annotations["paw.alc.xyz/profile"],
			Provider: workspace.statefulSet.Metadata.Annotations["paw.alc.xyz/provider"],
		}, true
	}
	return backuparchive.Metadata{}, false
}

func createBackupHelper(ctx context.Context, contextName, operationID, image, adapter string, readOnly bool, deps dependencies) (podResource, error) {
	name := "workspace-backup-" + operationID
	pullPolicy := "IfNotPresent"
	if adapter == deployment.AdapterMinikube {
		pullPolicy = "Never"
	}
	podSecurityContext := map[string]any{
		"runAsNonRoot": true, "runAsUser": 65532, "runAsGroup": 65532,
		"seccompProfile": map[string]string{"type": "RuntimeDefault"},
	}
	if !readOnly {
		podSecurityContext["fsGroup"] = 65532
		podSecurityContext["fsGroupChangePolicy"] = "OnRootMismatch"
	}
	manifest := map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": name, "namespace": workspaceNamespace,
			"labels": map[string]string{
				workspaceOwnerLabel: "paw", "app.kubernetes.io/name": "paw",
				"app.kubernetes.io/component": "workspace", "paw.alc.xyz/operation-id": operationID,
			},
		},
		"spec": map[string]any{
			"automountServiceAccountToken": false, "enableServiceLinks": false,
			"restartPolicy": "Never", "activeDeadlineSeconds": 1200,
			"securityContext": podSecurityContext,
			"containers": []any{map[string]any{
				"name": "backup", "image": image, "imagePullPolicy": pullPolicy,
				"command":         []string{"/bin/paw-backup-helper", "serve"},
				"securityContext": map[string]any{"allowPrivilegeEscalation": false, "privileged": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}},
				"resources": map[string]any{
					"requests": map[string]string{"cpu": "10m", "memory": "32Mi"},
					"limits":   map[string]string{"cpu": "1", "memory": "512Mi"},
				},
				"volumeMounts": []any{
					map[string]any{"name": "state", "mountPath": "/backup/state", "readOnly": readOnly},
					map[string]any{"name": "work", "mountPath": "/backup/work", "readOnly": readOnly},
				},
			}},
			"volumes": []any{
				map[string]any{"name": "state", "persistentVolumeClaim": map[string]any{"claimName": workspaceStateClaim, "readOnly": readOnly}},
				map[string]any{"name": "work", "persistentVolumeClaim": map[string]any{"claimName": workspaceWorkClaim, "readOnly": readOnly}},
			},
		},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil || deps.runInput == nil {
		return podResource{}, errors.New("helper manifest unavailable")
	}
	var response bytesBuffer
	if err := deps.runInput(ctx, "kubectl", backupKubectlArgs(contextName, "create", "-f", "-", "--output=json"),
		strings.NewReader(string(encoded)), &response, io.Discard); err != nil {
		return podResource{}, errors.New("helper creation failed")
	}
	var pod podResource
	if json.Unmarshal(response.Bytes(), &pod) != nil || pod.Metadata.Name != name || pod.Metadata.Namespace != workspaceNamespace ||
		pod.Metadata.UID == "" || pod.Metadata.ResourceVersion == "" || pod.Metadata.DeletionTimestamp != nil {
		return podResource{}, errors.New("invalid helper response")
	}
	return pod, nil
}

func waitForBackupHelper(contextName, name string, deps dependencies) error {
	return deps.runCommand("kubectl", []string{"--context", contextName, "--namespace", workspaceNamespace,
		"--request-timeout=130s", "wait", "--for=condition=Ready", "pod/" + name, "--timeout=120s"}, io.Discard, io.Discard)
}

func waitForBackupHelperDeletion(contextName, name string, deps dependencies) error {
	return deps.runCommand("kubectl", []string{"--context", contextName, "--namespace", workspaceNamespace,
		"--request-timeout=130s", "wait", "--for=delete", "pod/" + name, "--timeout=120s"}, io.Discard, io.Discard)
}

func waitForBackupLockDeletion(contextName string, deps dependencies) error {
	return deps.runCommand("kubectl", []string{"--context", contextName, "--namespace", workspaceNamespace,
		"--request-timeout=130s", "wait", "--for=delete", "configmap/" + workspaceLifecycleLock, "--timeout=120s"}, io.Discard, io.Discard)
}

func confirmBackupHelper(contextName string, expected podResource, operationID, image string, readOnly bool, deps dependencies) (podResource, error) {
	var actual podResource
	if err := getUpgradeResource(contextName, workspaceNamespace, []string{"pod/" + expected.Metadata.Name}, &actual, deps); err != nil {
		return podResource{}, err
	}
	if actual.Metadata.Name != expected.Metadata.Name || actual.Metadata.Namespace != workspaceNamespace ||
		actual.Metadata.UID != expected.Metadata.UID || actual.Metadata.ResourceVersion == "" ||
		actual.Metadata.DeletionTimestamp != nil || actual.Metadata.Labels[workspaceOwnerLabel] != "paw" ||
		actual.Metadata.Labels["paw.alc.xyz/operation-id"] != operationID || actual.Status.Phase != "Running" ||
		!podReady(actual.Status.Conditions) || len(actual.Spec.Containers) != 1 ||
		actual.Spec.Containers[0].Name != "backup" || actual.Spec.Containers[0].Image != image ||
		len(actual.Spec.Containers[0].VolumeMounts) != 2 || len(actual.Spec.Volumes) != 2 {
		return podResource{}, errors.New("invalid helper pod")
	}
	wanted := map[string]string{"state": workspaceStateClaim, "work": workspaceWorkClaim}
	for _, volume := range actual.Spec.Volumes {
		claim, exists := wanted[volume.Name]
		if !exists || volume.PersistentVolumeClaim == nil || volume.PersistentVolumeClaim.ClaimName != claim ||
			volume.PersistentVolumeClaim.ReadOnly != readOnly {
			return podResource{}, errors.New("invalid helper volume")
		}
		delete(wanted, volume.Name)
	}
	wantedMounts := map[string]string{"state": "/backup/state", "work": "/backup/work"}
	for _, mount := range actual.Spec.Containers[0].VolumeMounts {
		path, exists := wantedMounts[mount.Name]
		if !exists || mount.MountPath != path || mount.ReadOnly != readOnly || mount.SubPath != "" || mount.SubPathExpr != "" {
			return podResource{}, errors.New("invalid helper mount")
		}
		delete(wantedMounts, mount.Name)
	}
	if len(wanted) != 0 || len(wantedMounts) != 0 {
		return podResource{}, errors.New("missing helper storage")
	}
	return actual, nil
}

func deleteBackupPod(ctx context.Context, contextName string, metadata objectMetadata, deps dependencies) error {
	return deleteBackupResource(ctx, contextName, "/api/v1/namespaces/"+workspaceNamespace+"/pods/"+metadata.Name, metadata, deps)
}

func deleteBackupLock(ctx context.Context, contextName string, metadata objectMetadata, deps dependencies) error {
	return deleteBackupResource(ctx, contextName, "/api/v1/namespaces/"+workspaceNamespace+"/configmaps/"+workspaceLifecycleLock, metadata, deps)
}

func deleteBackupResource(ctx context.Context, contextName, endpoint string, metadata objectMetadata, deps dependencies) error {
	request := map[string]any{
		"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]string{
			"uid": metadata.UID, "resourceVersion": metadata.ResourceVersion,
		},
	}
	encoded, err := json.Marshal(request)
	if err != nil || deps.runInput == nil {
		return errors.New("delete preconditions unavailable")
	}
	return deps.runInput(ctx, "kubectl", []string{"--context", contextName, "--request-timeout=10s", "delete", "--raw=" + endpoint, "-f", "-"},
		strings.NewReader(string(encoded)), io.Discard, io.Discard)
}

func backupKubectlArgs(contextName string, command ...string) []string {
	args := []string{"--context", contextName, "--namespace", workspaceNamespace, "--request-timeout=10s"}
	return append(args, command...)
}

func backupStreamKubectlArgs(contextName string, command ...string) []string {
	args := []string{"--context", contextName, "--namespace", workspaceNamespace, "--request-timeout=15m"}
	return append(args, command...)
}

func backupHelperCommandArgs(contextName string, command ...string) []string {
	args := []string{"--context", contextName, "--namespace", workspaceNamespace, "--request-timeout=2m"}
	return append(args, command...)
}

type backupOutput struct {
	directory    string
	tempPath     string
	artifactPath string
	metadataPath string
}

func createBackupOutput(directory string) (*backupOutput, error) {
	if err := os.Mkdir(directory, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.Remove(directory)
		return nil, err
	}
	return &backupOutput{
		directory:    directory,
		tempPath:     filepath.Join(directory, ".workspace.paw-backup.tmp"),
		artifactPath: filepath.Join(directory, backupArtifactName),
		metadataPath: filepath.Join(directory, backupMetadataName),
	}, nil
}

func (output *backupOutput) createArtifact() (*os.File, error) {
	return os.OpenFile(output.tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func (output *backupOutput) publish(metadata backupSidecar) error {
	file, err := os.OpenFile(output.metadataPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(file)
	encodeErr := json.NewEncoder(writer).Encode(metadata)
	flushErr := writer.Flush()
	syncErr := file.Sync()
	closeErr := file.Close()
	if encodeErr != nil || flushErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("metadata write failed")
	}
	if err := os.Link(output.tempPath, output.artifactPath); err != nil {
		return err
	}
	if err := os.Remove(output.tempPath); err != nil {
		return err
	}
	directory, err := os.Open(output.directory)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr = directory.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func verifyBackupArtifact(ctx context.Context, path string) (backuparchive.Metadata, string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return backuparchive.Metadata{}, "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	metadata, err := backuparchive.Verify(ctx, io.TeeReader(file, hash))
	if err != nil {
		return backuparchive.Metadata{}, "", 0, err
	}
	info, err := file.Stat()
	if err != nil {
		return backuparchive.Metadata{}, "", 0, err
	}
	return metadata, hex.EncodeToString(hash.Sum(nil)), info.Size(), nil
}

func makeBackupSidecar(workspace backupWorkspace, metadata backuparchive.Metadata, digest string, size int64) backupSidecar {
	result := backupSidecar{
		Format: backupFormatVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ArchiveSHA256: digest, ArchiveSize: size,
		Image: metadata.Image, ImageID: metadata.ImageID, Profile: metadata.Profile, Provider: metadata.Provider,
		NamespaceUID: workspace.namespace.Metadata.UID, StatefulSetUID: workspace.statefulSet.Metadata.UID,
	}
	for _, claim := range workspace.claims.Items {
		switch claim.Metadata.Name {
		case workspaceStateClaim:
			result.StateClaimUID, result.StateVolume = claim.Metadata.UID, claim.Spec.VolumeName
		case workspaceWorkClaim:
			result.WorkClaimUID, result.WorkVolume = claim.Metadata.UID, claim.Spec.VolumeName
		}
	}
	return result
}

func sameBackupWorkspaceIdentity(before, after backupWorkspace, compareVolumes bool) bool {
	if before.namespace.Metadata.UID != after.namespace.Metadata.UID ||
		before.statefulSet.Metadata.UID != after.statefulSet.Metadata.UID ||
		before.contract.Metadata.UID != after.contract.Metadata.UID || len(before.claims.Items) != len(after.claims.Items) {
		return false
	}
	beforeMetadata := before.statefulSet.Metadata
	afterMetadata := after.statefulSet.Metadata
	if beforeMetadata.Annotations["paw.alc.xyz/profile"] != afterMetadata.Annotations["paw.alc.xyz/profile"] ||
		beforeMetadata.Annotations["paw.alc.xyz/provider"] != afterMetadata.Annotations["paw.alc.xyz/provider"] ||
		beforeMetadata.Annotations["paw.alc.xyz/storage-layout"] != afterMetadata.Annotations["paw.alc.xyz/storage-layout"] ||
		len(before.statefulSet.Spec.Template.Spec.Containers) != 1 || len(after.statefulSet.Spec.Template.Spec.Containers) != 1 ||
		before.statefulSet.Spec.Template.Spec.Containers[0].Image != after.statefulSet.Spec.Template.Spec.Containers[0].Image {
		return false
	}
	afterClaims := make(map[string]persistentVolumeClaimResource, len(after.claims.Items))
	for _, claim := range after.claims.Items {
		afterClaims[claim.Metadata.Name] = claim
	}
	for _, claim := range before.claims.Items {
		current, exists := afterClaims[claim.Metadata.Name]
		if !exists || claim.Metadata.UID != current.Metadata.UID ||
			(compareVolumes && claim.Spec.VolumeName != current.Spec.VolumeName) {
			return false
		}
	}
	return true
}

func backupMatchesRestoreTarget(sidecar backupSidecar, workspace backupWorkspace) bool {
	return len(workspace.statefulSet.Spec.Template.Spec.Containers) == 1 &&
		sidecar.Image == workspace.statefulSet.Spec.Template.Spec.Containers[0].Image &&
		sidecar.Profile == workspace.statefulSet.Metadata.Annotations["paw.alc.xyz/profile"] &&
		sidecar.Provider == workspace.statefulSet.Metadata.Annotations["paw.alc.xyz/provider"]
}

func openVerifiedBackup(ctx context.Context, directory string) (*os.File, backupSidecar, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, backupSidecar{}, errors.New("invalid backup directory")
	}
	metadataPath := filepath.Join(directory, backupMetadataName)
	metadataInfo, err := os.Lstat(metadataPath)
	if err != nil || !metadataInfo.Mode().IsRegular() {
		return nil, backupSidecar{}, errors.New("invalid backup metadata file")
	}
	metadataFile, err := os.Open(metadataPath)
	if err != nil {
		return nil, backupSidecar{}, err
	}
	var sidecar backupSidecar
	decoder := json.NewDecoder(io.LimitReader(metadataFile, 64*1024))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&sidecar)
	var trailing any
	trailingErr := decoder.Decode(&trailing)
	closeErr := metadataFile.Close()
	_, timeErr := time.Parse(time.RFC3339Nano, sidecar.CreatedAt)
	if decodeErr != nil || trailingErr != io.EOF || closeErr != nil || timeErr != nil || sidecar.Format != backupFormatVersion ||
		sidecar.ArchiveSize <= 0 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sidecar.ArchiveSHA256) ||
		sidecar.NamespaceUID == "" || sidecar.StatefulSetUID == "" || sidecar.StateClaimUID == "" ||
		sidecar.StateVolume == "" || sidecar.WorkClaimUID == "" || sidecar.WorkVolume == "" {
		return nil, backupSidecar{}, errors.New("invalid backup metadata")
	}
	archivePath := filepath.Join(directory, backupArtifactName)
	archiveInfo, err := os.Lstat(archivePath)
	if err != nil || !archiveInfo.Mode().IsRegular() {
		return nil, backupSidecar{}, errors.New("invalid backup artifact file")
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return nil, backupSidecar{}, err
	}
	info, err = archive.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != sidecar.ArchiveSize {
		archive.Close()
		return nil, backupSidecar{}, errors.New("invalid backup artifact")
	}
	hash := sha256.New()
	verified, err := backuparchive.Verify(ctx, io.TeeReader(archive, hash))
	if err != nil || hex.EncodeToString(hash.Sum(nil)) != sidecar.ArchiveSHA256 ||
		verified.Image != sidecar.Image || verified.ImageID != sidecar.ImageID ||
		verified.Profile != sidecar.Profile || verified.Provider != sidecar.Provider {
		archive.Close()
		return nil, backupSidecar{}, errors.New("backup verification failed")
	}
	return archive, sidecar, nil
}
