package harvester

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/bk201-org/harvester-scaleset/internal/config"
	"github.com/bk201-org/harvester-scaleset/internal/release"
)

const (
	managedByLabel         = "app.kubernetes.io/managed-by"
	managedByValue         = "harvester-scaleset"
	scaleSetIDLabel        = "actions.github.com/scale-set-id"
	runnerNameLabel        = "actions.github.com/runner-name"
	scaleSetAnnotation     = "actions.github.com/scale-set-name"
	controllerLabel        = "actions.github.com/controller-id"
	autoDeleteDisk         = "terraform-provider-harvester-auto-delete"
	imageIDAnnotation      = "harvesterhci.io/imageId"
	vmCreatorLabel         = "harvesterhci.io/creator"
	vmNameLabel            = "harvesterhci.io/vmName"
	volumeClaimsAnnotation = "harvesterhci.io/volumeClaimTemplates"
	removedPVCsAnnotation  = "harvesterhci.io/removedPersistentVolumeClaims"
)

var (
	vmGVR      = schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	imageGVR   = schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachineimages"}
	networkGVR = schema.GroupVersionResource{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"}
	keyPairGVR = schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "keypairs"}
)

type RunnerSpec struct {
	Name       string
	ScaleSetID int
	JITConfig  string
}

type RunnerResource struct {
	Name      string
	CreatedAt time.Time
	Phase     string
	Failed    bool
}

type Backend interface {
	CreateRunner(context.Context, RunnerSpec) error
	DeleteRunner(context.Context, string) error
	ListRunners(context.Context, int) ([]RunnerResource, error)
	DeleteAll(context.Context, int) error
}

type Client struct {
	Dynamic dynamic.Interface
	Kube    kubernetes.Interface
}

type Manager struct {
	client       *Client
	cfg          config.Config
	artifact     release.Artifact
	controllerID string
	imageRef     string
	storageClass string
	networkRef   string
	logger       *slog.Logger
}

func NewManager(client *Client, cfg config.Config, artifact release.Artifact, controllerID string, logger *slog.Logger) (*Manager, error) {
	if client == nil || client.Dynamic == nil || client.Kube == nil {
		return nil, errors.New("Harvester client is required")
	}
	if err := validateGuestPath(cfg.RunnerHome); err != nil {
		return nil, err
	}
	return &Manager{client: client, cfg: cfg, artifact: artifact, controllerID: controllerID, logger: logger}, nil
}

func NewClient(cfg config.Config) (*Client, error) {
	restConfig, err := restConfig(cfg)
	if err != nil {
		return nil, err
	}
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create dynamic Kubernetes client: %w", err)
	}
	kubeClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	return &Client{Dynamic: dynamicClient, Kube: kubeClient}, nil
}

func restConfig(cfg config.Config) (*rest.Config, error) {
	if cfg.KubeconfigBase64 != "" {
		data, err := base64.StdEncoding.DecodeString(cfg.KubeconfigBase64)
		if err != nil {
			return nil, fmt.Errorf("decode HARVESTER_KUBECONFIG_B64: %w", err)
		}
		result, err := clientcmd.RESTConfigFromKubeConfig(data)
		if err != nil {
			return nil, fmt.Errorf("parse base64 kubeconfig: %w", err)
		}
		return result, nil
	}
	if _, err := os.Stat(cfg.Kubeconfig); err != nil {
		return nil, fmt.Errorf("read kubeconfig: %w", err)
	}
	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: cfg.Kubeconfig}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: cfg.KubeContext}
	result, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	return result, nil
}

func (m *Manager) Preflight(ctx context.Context) error {
	if _, err := m.client.Kube.CoreV1().Namespaces().Get(ctx, m.cfg.Namespace, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("get runner namespace %q: %w", m.cfg.Namespace, err)
	}
	imageValue := m.cfg.RunnerImage
	if m.cfg.RunnerImageDisplayName != "" {
		imageValue = m.cfg.RunnerImageDisplayName
	}
	imageNS, imageIdentifier, err := namespacedName(imageValue, m.cfg.Namespace)
	if err != nil {
		return fmt.Errorf("parse runner image: %w", err)
	}
	var image *unstructured.Unstructured
	if m.cfg.RunnerImageDisplayName != "" {
		image, err = m.findRunnerImageByDisplayName(ctx, imageNS, imageIdentifier)
	} else {
		image, err = m.client.Dynamic.Resource(imageGVR).Namespace(imageNS).Get(ctx, imageIdentifier, metav1.GetOptions{})
	}
	if err != nil {
		return fmt.Errorf("get runner image %s/%s: %w", imageNS, imageIdentifier, err)
	}
	imageName := image.GetName()
	if !conditionTrue(image, "Imported") {
		return fmt.Errorf("runner image %s/%s is not imported and ready", imageNS, imageName)
	}
	storageClass, _, err := unstructured.NestedString(image.Object, "status", "storageClassName")
	if err != nil || storageClass == "" {
		return fmt.Errorf("runner image %s/%s has no storage class", imageNS, imageName)
	}
	virtualSize, found, err := unstructured.NestedInt64(image.Object, "status", "virtualSize")
	if err != nil {
		return fmt.Errorf("read runner image %s/%s virtual size: %w", imageNS, imageName, err)
	}
	if found && virtualSize > 0 {
		diskSize, err := resource.ParseQuantity(m.cfg.DiskSize)
		if err != nil {
			return fmt.Errorf("parse runner disk size: %w", err)
		}
		if diskSize.Value() < virtualSize {
			return fmt.Errorf("runner disk size %s is smaller than image virtual size %d bytes", m.cfg.DiskSize, virtualSize)
		}
	}
	networkNS, networkName, err := namespacedName(m.cfg.Network, m.cfg.Namespace)
	if err != nil {
		return fmt.Errorf("parse VM network: %w", err)
	}
	if _, err := m.client.Dynamic.Resource(networkGVR).Namespace(networkNS).Get(ctx, networkName, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("get VM network %s/%s: %w", networkNS, networkName, err)
	}
	if _, err := m.keyPairPublicKey(ctx); err != nil {
		return err
	}
	m.imageRef = imageNS + "/" + imageName
	m.storageClass = storageClass
	if networkNS == m.cfg.Namespace {
		m.networkRef = networkName
	} else {
		m.networkRef = networkNS + "/" + networkName
	}
	runnerVersion := m.artifact.Version
	if m.cfg.SkipRunnerInstall {
		runnerVersion = "preinstalled"
	}
	m.logger.Info("Harvester preflight complete", "namespace", m.cfg.Namespace, "image", m.imageRef, "network", networkNS+"/"+networkName, "runnerVersion", runnerVersion)
	return nil
}

func (m *Manager) keyPairPublicKey(ctx context.Context) (string, error) {
	if m.cfg.KeyPair == "" {
		return "", nil
	}
	parts := strings.Split(m.cfg.KeyPair, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("keypair %q must be namespace/name", m.cfg.KeyPair)
	}
	keyPair, err := m.client.Dynamic.Resource(keyPairGVR).Namespace(parts[0]).Get(ctx, parts[1], metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get keypair %s: %w", m.cfg.KeyPair, err)
	}
	publicKey, found, err := unstructured.NestedString(keyPair.Object, "spec", "publicKey")
	if err != nil {
		return "", fmt.Errorf("read keypair %s public key: %w", m.cfg.KeyPair, err)
	}
	publicKey = strings.TrimSpace(publicKey)
	if !found || publicKey == "" || strings.ContainsAny(publicKey, "\r\n") {
		return "", fmt.Errorf("keypair %s has no valid single-line public key", m.cfg.KeyPair)
	}
	return publicKey, nil
}

func (m *Manager) findRunnerImageByDisplayName(ctx context.Context, namespace, wantedDisplayName string) (*unstructured.Unstructured, error) {
	list, err := m.client.Dynamic.Resource(imageGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list images in namespace %q to find displayName %q: %w", namespace, wantedDisplayName, err)
	}
	var match *unstructured.Unstructured
	for i := range list.Items {
		candidate := &list.Items[i]
		displayName, _, fieldErr := unstructured.NestedString(candidate.Object, "spec", "displayName")
		if fieldErr != nil {
			return nil, fmt.Errorf("read image %s/%s displayName: %w", namespace, candidate.GetName(), fieldErr)
		}
		if displayName != wantedDisplayName {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("displayName %q matches multiple images in namespace %q (%s, %s)", wantedDisplayName, namespace, match.GetName(), candidate.GetName())
		}
		match = candidate
	}
	if match == nil {
		return nil, fmt.Errorf("no image with displayName %q in namespace %q", wantedDisplayName, namespace)
	}
	return match, nil
}

func (m *Manager) CreateRunner(ctx context.Context, spec RunnerSpec) error {
	publicKey, err := m.keyPairPublicKey(ctx)
	if err != nil {
		return err
	}
	labels := m.labels(spec.ScaleSetID, spec.Name)
	userData, err := RenderCloudInit(BootstrapConfig{
		RunnerUser:        m.cfg.RunnerUser,
		SSHUsername:       m.cfg.SSHUsername,
		SSHPublicKey:      publicKey,
		RunnerHome:        m.cfg.RunnerHome,
		JITConfig:         spec.JITConfig,
		Artifact:          m.artifact,
		SkipRunnerInstall: m.cfg.SkipRunnerInstall,
		HostMappings:      m.cfg.HostMappings,
		TrustedCACerts:    m.cfg.TrustedCACerts,
	})
	if err != nil {
		return fmt.Errorf("render cloud-init for %s: %w", spec.Name, err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        spec.Name,
			Namespace:   m.cfg.Namespace,
			Labels:      labels,
			Annotations: map[string]string{scaleSetAnnotation: m.cfg.ScaleSetName},
		},
		StringData: map[string]string{"userdata": string(userData)},
	}
	if _, err := m.client.Kube.CoreV1().Secrets(m.cfg.Namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create cloud-init Secret for %s: %w", spec.Name, err)
	}

	vm, err := m.buildVM(spec, labels)
	if err != nil {
		_ = m.deleteSecret(context.WithoutCancel(ctx), spec.Name)
		return err
	}
	created, err := m.client.Dynamic.Resource(vmGVR).Namespace(m.cfg.Namespace).Create(ctx, vm, metav1.CreateOptions{})
	if err != nil {
		_ = m.deleteSecret(context.WithoutCancel(ctx), spec.Name)
		return fmt.Errorf("create runner VM %s: %w", spec.Name, err)
	}
	currentSecret, err := m.client.Kube.CoreV1().Secrets(m.cfg.Namespace).Get(ctx, spec.Name, metav1.GetOptions{})
	if err == nil {
		currentSecret.OwnerReferences = []metav1.OwnerReference{{
			APIVersion:         "kubevirt.io/v1",
			Kind:               "VirtualMachine",
			Name:               created.GetName(),
			UID:                created.GetUID(),
			Controller:         boolPtr(true),
			BlockOwnerDeletion: boolPtr(true),
		}}
		if _, updateErr := m.client.Kube.CoreV1().Secrets(m.cfg.Namespace).Update(ctx, currentSecret, metav1.UpdateOptions{}); updateErr != nil {
			m.logger.Warn("Failed to attach VM owner to cloud-init Secret", "runner", spec.Name, "error", updateErr)
		}
	}
	return nil
}

func (m *Manager) buildVM(spec RunnerSpec, labels map[string]string) (*unstructured.Unstructured, error) {
	pvcName := spec.Name + "-rootdisk"
	pvcTemplate := []any{map[string]any{
		"metadata": map[string]any{
			"name": pvcName,
			"annotations": map[string]any{
				imageIDAnnotation: m.imageRef,
				autoDeleteDisk:    "true",
			},
			"labels": stringMapAny(labels),
		},
		"spec": map[string]any{
			"accessModes":      []any{"ReadWriteMany"},
			"resources":        map[string]any{"requests": map[string]any{"storage": m.cfg.DiskSize}},
			"storageClassName": m.storageClass,
			"volumeMode":       "Block",
		},
	}}
	pvcJSON, err := json.Marshal(pvcTemplate)
	if err != nil {
		return nil, fmt.Errorf("marshal root disk template: %w", err)
	}
	templateLabels := cloneLabels(labels)
	templateLabels[vmCreatorLabel] = managedByValue
	templateLabels[vmNameLabel] = spec.Name
	vmLabels := cloneLabels(templateLabels)

	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachine",
		"metadata": map[string]any{
			"name":      spec.Name,
			"namespace": m.cfg.Namespace,
			"labels":    stringMapAny(vmLabels),
			"annotations": map[string]any{
				scaleSetAnnotation:     m.cfg.ScaleSetName,
				volumeClaimsAnnotation: string(pvcJSON),
			},
		},
		"spec": map[string]any{
			"runStrategy": "Once",
			"template": map[string]any{
				"metadata": map[string]any{"labels": stringMapAny(templateLabels)},
				"spec": map[string]any{
					"evictionStrategy": "LiveMigrateIfPossible",
					"domain": map[string]any{
						"cpu":       map[string]any{"cores": int64(m.cfg.CPU), "sockets": int64(1), "threads": int64(1)},
						"resources": map[string]any{"limits": map[string]any{"cpu": strconv.Itoa(m.cfg.CPU), "memory": m.cfg.Memory}},
						"devices": map[string]any{
							"disks": []any{
								map[string]any{"name": "rootdisk", "bootOrder": int64(1), "disk": map[string]any{"bus": "virtio"}},
								map[string]any{"name": "cloudinitdisk", "disk": map[string]any{"bus": "virtio"}},
							},
							"interfaces": []any{map[string]any{"name": "nic-1", "model": "virtio", "bridge": map[string]any{}}},
						},
					},
					"networks": []any{map[string]any{"name": "nic-1", "multus": map[string]any{"networkName": m.networkRef}}},
					"volumes": []any{
						map[string]any{"name": "rootdisk", "persistentVolumeClaim": map[string]any{"claimName": pvcName}},
						map[string]any{"name": "cloudinitdisk", "cloudInitNoCloud": map[string]any{"secretRef": map[string]any{"name": spec.Name}}},
					},
				},
			},
		},
	}}, nil
}

func (m *Manager) DeleteRunner(ctx context.Context, name string) error {
	vms := m.client.Dynamic.Resource(vmGVR).Namespace(m.cfg.Namespace)
	vm, err := vms.Get(ctx, name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get runner VM %s before deletion: %w", name, err)
	}
	if vm != nil {
		claimNames := pvcNames(vm)
		if len(claimNames) > 0 {
			copy := vm.DeepCopy()
			annotations := copy.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations[removedPVCsAnnotation] = strings.Join(claimNames, ",")
			copy.SetAnnotations(annotations)
			if _, updateErr := vms.Update(ctx, copy, metav1.UpdateOptions{}); updateErr != nil && !apierrors.IsNotFound(updateErr) {
				return fmt.Errorf("mark runner VM disks for deletion %s: %w", name, updateErr)
			}
		}
		foreground := metav1.DeletePropagationForeground
		if err := vms.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &foreground}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete runner VM %s: %w", name, err)
		}
		for _, claimName := range claimNames {
			if deleteErr := m.client.Kube.CoreV1().PersistentVolumeClaims(m.cfg.Namespace).Delete(ctx, claimName, metav1.DeleteOptions{}); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
				m.logger.Warn("Failed to delete runner PVC", "runner", name, "pvc", claimName, "error", deleteErr)
			}
		}
	}
	if err := m.deleteSecret(ctx, name); err != nil {
		return fmt.Errorf("delete cloud-init Secret for %s: %w", name, err)
	}
	return nil
}

func (m *Manager) ListRunners(ctx context.Context, scaleSetID int) ([]RunnerResource, error) {
	selector := fmt.Sprintf("%s=%s,%s=%d", managedByLabel, managedByValue, scaleSetIDLabel, scaleSetID)
	vms, err := m.client.Dynamic.Resource(vmGVR).Namespace(m.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list runner VMs: %w", err)
	}
	resources := make([]RunnerResource, 0, len(vms.Items))
	for i := range vms.Items {
		vm := &vms.Items[i]
		if vm.GetDeletionTimestamp() != nil {
			continue
		}
		phase, failed := vmPhase(vm)
		resources = append(resources, RunnerResource{Name: vm.GetName(), CreatedAt: vm.GetCreationTimestamp().Time, Phase: phase, Failed: failed})
	}
	return resources, nil
}

func (m *Manager) DeleteAll(ctx context.Context, scaleSetID int) error {
	resources, err := m.ListRunners(ctx, scaleSetID)
	if err != nil {
		return err
	}
	var errs []error
	for _, resource := range resources {
		if err := m.DeleteRunner(ctx, resource.Name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) deleteSecret(ctx context.Context, name string) error {
	err := m.client.Kube.CoreV1().Secrets(m.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (m *Manager) labels(scaleSetID int, runnerName string) map[string]string {
	return map[string]string{
		managedByLabel:  managedByValue,
		scaleSetIDLabel: strconv.Itoa(scaleSetID),
		runnerNameLabel: runnerName,
		controllerLabel: m.controllerID,
	}
}

func namespacedName(value, fallbackNamespace string) (string, string, error) {
	parts := strings.Split(value, "/")
	switch len(parts) {
	case 1:
		if parts[0] == "" {
			return "", "", errors.New("name is empty")
		}
		return fallbackNamespace, parts[0], nil
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", fmt.Errorf("invalid namespaced name %q", value)
		}
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("invalid namespaced name %q", value)
	}
}

func conditionTrue(object *unstructured.Unstructured, conditionType string) bool {
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if ok && condition["type"] == conditionType && condition["status"] == "True" {
			return true
		}
	}
	return false
}

func pvcNames(vm *unstructured.Unstructured) []string {
	volumes, _, _ := unstructured.NestedSlice(vm.Object, "spec", "template", "spec", "volumes")
	var names []string
	for _, raw := range volumes {
		volume, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		pvc, ok := volume["persistentVolumeClaim"].(map[string]any)
		if !ok {
			continue
		}
		if name, ok := pvc["claimName"].(string); ok && name != "" {
			names = append(names, name)
		}
	}
	return names
}

func vmPhase(vm *unstructured.Unstructured) (string, bool) {
	if conditionTrue(vm, "Failure") {
		return "Failure", true
	}
	if conditionTrue(vm, "Ready") {
		return "Ready", false
	}
	phase, _, _ := unstructured.NestedString(vm.Object, "status", "printableStatus")
	switch phase {
	case "CrashLoopBackOff", "Unschedulable", "ErrImagePull", "ImagePullBackOff":
		return phase, true
	}
	return phase, false
}

func cloneLabels(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+2)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func stringMapAny(source map[string]string) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func boolPtr(value bool) *bool { return &value }

var _ Backend = (*Manager)(nil)
