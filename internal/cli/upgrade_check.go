package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	deployment "github.com/alcxyz/paw/deploy"
)

const (
	workspaceStorageLayout = "persistent-v2"
	workspaceStateClaim    = "workspace-state"
	workspaceWorkClaim     = "workspace-work"
	// workspaceSessionClaim holds provider login state. It is retained until
	// destroy but deliberately outside the backup and restore contract.
	workspaceSessionClaim  = "workspace-session"
	workspaceLifecycleLock = "workspace-lifecycle-lock"
)

type objectMetadata struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid"`
	ResourceVersion   string            `json:"resourceVersion"`
	Generation        int64             `json:"generation"`
	DeletionTimestamp *string           `json:"deletionTimestamp"`
	Labels            map[string]string `json:"labels"`
	Annotations       map[string]string `json:"annotations"`
	OwnerReferences   []ownerReference  `json:"ownerReferences"`
}

type ownerReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Controller *bool  `json:"controller"`
}

type namespaceResource struct {
	Metadata objectMetadata `json:"metadata"`
}

type statefulSetResource struct {
	Metadata objectMetadata `json:"metadata"`
	Spec     struct {
		Replicas *int32      `json:"replicas"`
		Template podTemplate `json:"template"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration int64  `json:"observedGeneration"`
		Replicas           int32  `json:"replicas"`
		CurrentReplicas    int32  `json:"currentReplicas"`
		ReadyReplicas      int32  `json:"readyReplicas"`
		UpdatedReplicas    int32  `json:"updatedReplicas"`
		CurrentRevision    string `json:"currentRevision"`
		UpdateRevision     string `json:"updateRevision"`
	} `json:"status"`
}

type podTemplate struct {
	Metadata objectMetadata `json:"metadata"`
	Spec     podSpec        `json:"spec"`
}

type podSpec struct {
	Containers          []containerSpec `json:"containers"`
	InitContainers      []containerSpec `json:"initContainers"`
	EphemeralContainers []containerSpec `json:"ephemeralContainers"`
	Volumes             []volumeSpec    `json:"volumes"`
}

type containerSpec struct {
	Name         string        `json:"name"`
	Image        string        `json:"image"`
	VolumeMounts []volumeMount `json:"volumeMounts"`
}

type volumeMount struct {
	Name        string `json:"name"`
	MountPath   string `json:"mountPath"`
	ReadOnly    bool   `json:"readOnly"`
	SubPath     string `json:"subPath"`
	SubPathExpr string `json:"subPathExpr"`
}

type volumeSpec struct {
	Name                  string                 `json:"name"`
	PersistentVolumeClaim *persistentVolumeClaim `json:"persistentVolumeClaim"`
	EmptyDir              *struct{}              `json:"emptyDir"`
	ConfigMap             *configMapVolume       `json:"configMap"`
}

type persistentVolumeClaim struct {
	ClaimName string `json:"claimName"`
	ReadOnly  bool   `json:"readOnly"`
}

type configMapVolume struct {
	Name string `json:"name"`
}

type persistentVolumeClaimResource struct {
	Metadata objectMetadata `json:"metadata"`
	Spec     struct {
		AccessModes []string `json:"accessModes"`
		VolumeName  string   `json:"volumeName"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

type persistentVolumeClaimList struct {
	Items []persistentVolumeClaimResource `json:"items"`
}

type configMapResource struct {
	Metadata objectMetadata    `json:"metadata"`
	Data     map[string]string `json:"data"`
}

type podResource struct {
	Metadata objectMetadata `json:"metadata"`
	Spec     podSpec        `json:"spec"`
	Status   struct {
		Phase             string            `json:"phase"`
		Conditions        []podCondition    `json:"conditions"`
		ContainerStatuses []containerStatus `json:"containerStatuses"`
	} `json:"status"`
}

type podCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type containerStatus struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	ImageID string `json:"imageID"`
	Ready   bool   `json:"ready"`
}

type podList struct {
	Items []podResource `json:"items"`
}

func runWorkspaceUpgradeCheck(options workspaceOptions, stdout, stderr io.Writer, deps dependencies) int {
	if err := checkWorkspaceUpgrade(options.context, deps); err != nil {
		// Kubernetes diagnostics and object fields are deliberately suppressed:
		// repository-controlled metadata must not become terminal output.
		io.WriteString(stderr, "paw: workspace upgrade preflight failed: "+err.Error()+"; no changes were made\n")
		return 1
	}
	io.WriteString(stdout, "workspace upgrade preflight passed at this point in time; no changes were made and this does not authorize an upgrade\n")
	return 0
}

func checkWorkspaceUpgrade(contextName string, deps dependencies) error {
	var namespace namespaceResource
	if err := getUpgradeResource(contextName, "", []string{"namespace/" + workspaceNamespace}, &namespace, deps); err != nil {
		return err
	}
	if namespace.Metadata.Name != workspaceNamespace || namespace.Metadata.UID == "" ||
		namespace.Metadata.ResourceVersion == "" || namespace.Metadata.DeletionTimestamp != nil ||
		namespace.Metadata.Labels[workspaceOwnerLabel] != "paw" {
		return errors.New("invalid namespace")
	}

	var statefulSet statefulSetResource
	if err := getUpgradeResource(contextName, workspaceNamespace, []string{"statefulset/workspace"}, &statefulSet, deps); err != nil {
		return err
	}
	if !hasPersistentV1Layout(statefulSet) {
		return errors.New("legacy or missing persistent-v2 storage layout; do not stop this pod; migration is required")
	}
	if !validUpgradeStatefulSet(statefulSet) {
		return errors.New("workspace writer is not in a supported steady state")
	}

	var claims persistentVolumeClaimList
	if err := getUpgradeResource(contextName, workspaceNamespace, []string{
		"persistentvolumeclaim/" + workspaceStateClaim,
		"persistentvolumeclaim/" + workspaceWorkClaim,
	}, &claims, deps); err != nil {
		return errors.New("required persistent workspace claims are unavailable")
	}
	if !validUpgradeClaims(claims) {
		return errors.New("persistent workspace claims are not safely bound")
	}

	var contract configMapResource
	if err := getUpgradeResource(contextName, workspaceNamespace, []string{"configmap/workspace-contract"}, &contract, deps); err != nil {
		return err
	}
	if !validUpgradeContract(contract, statefulSet) {
		return errors.New("workspace contract does not match the running writer")
	}

	var lock bytes.Buffer
	if err := deps.runCommand("kubectl", []string{
		"--context", contextName, "--namespace", workspaceNamespace, "--request-timeout=10s",
		"get", "configmap/" + workspaceLifecycleLock, "--ignore-not-found=true", "--output=json",
	}, &lock, io.Discard); err != nil || len(bytes.TrimSpace(lock.Bytes())) != 0 {
		return errors.New("lifecycle lock present or unknown")
	}

	var pods podList
	if err := getUpgradeResource(contextName, workspaceNamespace, []string{"pods"}, &pods, deps); err != nil {
		return err
	}
	if !validUpgradePods(pods, statefulSet) {
		return errors.New("workspace does not have exactly one ready controlled writer")
	}
	return nil
}

func getUpgradeResource(contextName, namespace string, resources []string, target any, deps dependencies) error {
	args := []string{"--context", contextName}
	if namespace != "" {
		args = append(args, "--namespace", namespace)
	}
	args = append(args, "--request-timeout=10s", "get")
	args = append(args, resources...)
	args = append(args, "--output=json")

	var output bytes.Buffer
	if err := deps.runCommand("kubectl", args, &output, io.Discard); err != nil {
		return errors.New("resource lookup failed")
	}
	if err := json.Unmarshal(output.Bytes(), target); err != nil {
		return errors.New("invalid resource response")
	}
	return nil
}

func validUpgradeStatefulSet(statefulSet statefulSetResource) bool {
	metadata := statefulSet.Metadata
	profile := metadata.Annotations["paw.alc.xyz/profile"]
	provider := metadata.Annotations["paw.alc.xyz/provider"]
	templateMetadata := statefulSet.Spec.Template.Metadata
	if _, err := deployment.ImageName(deployment.Selection{Profile: profile, Provider: provider}); err != nil {
		return false
	}
	if metadata.Name != "workspace" || metadata.Namespace != workspaceNamespace || metadata.UID == "" ||
		metadata.ResourceVersion == "" || metadata.Generation <= 0 || metadata.DeletionTimestamp != nil ||
		metadata.Annotations["paw.alc.xyz/single-writer"] != "true" ||
		profile == "" || provider == "" || templateMetadata.Annotations["paw.alc.xyz/profile"] != profile ||
		templateMetadata.Annotations["paw.alc.xyz/provider"] != provider ||
		statefulSet.Spec.Replicas == nil || *statefulSet.Spec.Replicas != 1 ||
		statefulSet.Status.ObservedGeneration != metadata.Generation ||
		statefulSet.Status.Replicas != 1 || statefulSet.Status.CurrentReplicas != 1 ||
		statefulSet.Status.ReadyReplicas != 1 || statefulSet.Status.UpdatedReplicas != 1 ||
		statefulSet.Status.CurrentRevision == "" || statefulSet.Status.CurrentRevision != statefulSet.Status.UpdateRevision {
		return false
	}
	return validUpgradePodSpec(statefulSet.Spec.Template.Spec)
}

func hasPersistentV1Layout(statefulSet statefulSetResource) bool {
	return statefulSet.Metadata.Annotations["paw.alc.xyz/storage-layout"] == workspaceStorageLayout &&
		statefulSet.Spec.Template.Metadata.Annotations["paw.alc.xyz/storage-layout"] == workspaceStorageLayout &&
		validUpgradePodSpec(statefulSet.Spec.Template.Spec)
}

func validUpgradePodSpec(spec podSpec) bool {
	if len(spec.Containers) != 1 || spec.Containers[0].Name != "t3" || spec.Containers[0].Image == "" ||
		len(spec.InitContainers) != 0 || len(spec.EphemeralContainers) != 0 ||
		!validUpgradeMounts(spec.Containers[0].VolumeMounts) || len(spec.Volumes) != 5 {
		return false
	}
	volumes := make(map[string]volumeSpec, len(spec.Volumes))
	for _, volume := range spec.Volumes {
		if volume.Name == "" {
			return false
		}
		if _, exists := volumes[volume.Name]; exists {
			return false
		}
		volumes[volume.Name] = volume
	}
	state, stateExists := volumes["state"]
	work, workExists := volumes["work"]
	tmp, tmpExists := volumes["tmp"]
	session, sessionExists := volumes["session"]
	contract, contractExists := volumes["contract"]
	return stateExists && state.PersistentVolumeClaim != nil &&
		state.PersistentVolumeClaim.ClaimName == workspaceStateClaim && !state.PersistentVolumeClaim.ReadOnly &&
		workExists && work.PersistentVolumeClaim != nil &&
		work.PersistentVolumeClaim.ClaimName == workspaceWorkClaim && !work.PersistentVolumeClaim.ReadOnly &&
		tmpExists && tmp.EmptyDir != nil &&
		sessionExists && session.PersistentVolumeClaim != nil &&
		session.PersistentVolumeClaim.ClaimName == workspaceSessionClaim && !session.PersistentVolumeClaim.ReadOnly &&
		contractExists && contract.ConfigMap != nil && contract.ConfigMap.Name == "workspace-contract"
}

func validUpgradeMounts(mounts []volumeMount) bool {
	if len(mounts) != 6 {
		return false
	}
	expected := map[string]volumeMount{
		"/workspace/state":         {Name: "state", MountPath: "/workspace/state"},
		"/workspace/work":          {Name: "work", MountPath: "/workspace/work"},
		"/tmp":                     {Name: "tmp", MountPath: "/tmp"},
		"/workspace/session/codex": {Name: "session", MountPath: "/workspace/session/codex", SubPath: "codex"},
		"/workspace/session/claude": {
			Name: "session", MountPath: "/workspace/session/claude", SubPath: "claude",
		},
		"/etc/paw": {Name: "contract", MountPath: "/etc/paw", ReadOnly: true},
	}
	for _, mount := range mounts {
		want, exists := expected[mount.MountPath]
		if !exists || mount != want {
			return false
		}
		delete(expected, mount.MountPath)
	}
	return len(expected) == 0
}

func validUpgradeClaims(list persistentVolumeClaimList) bool {
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
			len(claim.Spec.AccessModes) != 1 || claim.Spec.AccessModes[0] != "ReadWriteOnce" ||
			claim.Spec.VolumeName == "" || claim.Status.Phase != "Bound" {
			return false
		}
		delete(wanted, metadata.Name)
	}
	return len(wanted) == 0
}

func validUpgradeContract(contract configMapResource, statefulSet statefulSetResource) bool {
	return contract.Metadata.Name == "workspace-contract" && contract.Metadata.Namespace == workspaceNamespace &&
		contract.Metadata.UID != "" && contract.Metadata.ResourceVersion != "" &&
		contract.Metadata.DeletionTimestamp == nil &&
		contract.Data["profile"] == statefulSet.Metadata.Annotations["paw.alc.xyz/profile"] &&
		contract.Data["provider"] == statefulSet.Metadata.Annotations["paw.alc.xyz/provider"] &&
		contract.Data["state-policy"] == "retain-until-destroy"
}

func validUpgradePods(list podList, statefulSet statefulSetResource) bool {
	controllerPods := 0
	workspaceFound := false
	for _, pod := range list.Items {
		live := pod.Status.Phase != "Succeeded" && pod.Status.Phase != "Failed"
		owned := controlledByStatefulSet(pod.Metadata.OwnerReferences, statefulSet.Metadata)
		if live && owned {
			controllerPods++
		}
		if live && pod.Metadata.Name != "workspace-0" && podMountsWorkspaceClaim(pod.Spec) {
			return false
		}
		if pod.Metadata.Name == "workspace-0" {
			workspaceFound = true
			if !validUpgradeWorkspacePod(pod, statefulSet, owned) {
				return false
			}
		}
	}
	return workspaceFound && controllerPods == 1
}

func controlledByStatefulSet(references []ownerReference, statefulSet objectMetadata) bool {
	for _, reference := range references {
		if reference.Controller != nil && *reference.Controller && reference.APIVersion == "apps/v1" &&
			reference.Kind == "StatefulSet" && reference.Name == statefulSet.Name && reference.UID == statefulSet.UID {
			return true
		}
	}
	return false
}

func podMountsWorkspaceClaim(spec podSpec) bool {
	for _, volume := range spec.Volumes {
		if volume.PersistentVolumeClaim != nil &&
			(volume.PersistentVolumeClaim.ClaimName == workspaceStateClaim ||
				volume.PersistentVolumeClaim.ClaimName == workspaceWorkClaim) {
			return true
		}
	}
	return false
}

func validUpgradeWorkspacePod(pod podResource, statefulSet statefulSetResource, owned bool) bool {
	profile := statefulSet.Metadata.Annotations["paw.alc.xyz/profile"]
	provider := statefulSet.Metadata.Annotations["paw.alc.xyz/provider"]
	if !owned || pod.Metadata.Namespace != workspaceNamespace || pod.Metadata.UID == "" ||
		pod.Metadata.ResourceVersion == "" || pod.Metadata.DeletionTimestamp != nil || pod.Status.Phase != "Running" ||
		pod.Metadata.Annotations["paw.alc.xyz/profile"] != profile ||
		pod.Metadata.Annotations["paw.alc.xyz/provider"] != provider ||
		pod.Metadata.Annotations["paw.alc.xyz/storage-layout"] != workspaceStorageLayout ||
		!validUpgradePodSpec(pod.Spec) || !podReady(pod.Status.Conditions) ||
		len(pod.Status.ContainerStatuses) != 1 || !pod.Status.ContainerStatuses[0].Ready ||
		pod.Status.ContainerStatuses[0].Name != "t3" || pod.Status.ContainerStatuses[0].ImageID == "" ||
		pod.Spec.Containers[0].Image != statefulSet.Spec.Template.Spec.Containers[0].Image {
		return false
	}
	return true
}

func podReady(conditions []podCondition) bool {
	for _, condition := range conditions {
		if condition.Type == "Ready" && condition.Status == "True" {
			return true
		}
	}
	return false
}
