package harvester

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/bk201-org/harvester-scaleset/internal/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func TestBuildVM(t *testing.T) {
	t.Parallel()
	m := &Manager{
		cfg:          config.Config{Namespace: "default", CPU: 1, Memory: "1Gi", DiskSize: "10Gi", ScaleSetName: "test"},
		imageRef:     "default/ubuntu",
		storageClass: "image-storage-class",
		networkRef:   "hvst-dev-vlan",
	}
	labels := m.labels(42, "test-runner")
	vm, err := m.buildVM(RunnerSpec{Name: "test-runner", ScaleSetID: 42}, labels)
	if err != nil {
		t.Fatal(err)
	}
	if got := vm.GetName(); got != "test-runner" {
		t.Fatalf("VM name = %q", got)
	}
	if got, _, _ := nestedString(vm.Object, "spec", "runStrategy"); got != "Once" {
		t.Fatalf("run strategy = %q", got)
	}
	annotations := vm.GetAnnotations()
	var templates []map[string]any
	if err := json.Unmarshal([]byte(annotations[volumeClaimsAnnotation]), &templates); err != nil {
		t.Fatalf("invalid PVC template annotation: %v", err)
	}
	if len(templates) != 1 {
		t.Fatalf("PVC template count = %d", len(templates))
	}
	metadata := templates[0]["metadata"].(map[string]any)
	if metadata["name"] != "test-runner-rootdisk" {
		t.Fatalf("PVC name = %#v", metadata["name"])
	}
}

func TestNamespacedName(t *testing.T) {
	t.Parallel()
	ns, name, err := namespacedName("ubuntu", "default")
	if err != nil || ns != "default" || name != "ubuntu" {
		t.Fatalf("unexpected result: %q %q %v", ns, name, err)
	}
	ns, name, err = namespacedName("images/ubuntu", "default")
	if err != nil || ns != "images" || name != "ubuntu" {
		t.Fatalf("unexpected result: %q %q %v", ns, name, err)
	}
	if _, _, err := namespacedName("a/b/c", "default"); err == nil {
		t.Fatal("expected invalid namespaced name error")
	}
}

func nestedString(object map[string]any, fields ...string) (string, bool, error) {
	current := any(object)
	for _, field := range fields {
		mapping, ok := current.(map[string]any)
		if !ok {
			return "", false, nil
		}
		current, ok = mapping[field]
		if !ok {
			return "", false, nil
		}
	}
	value, ok := current.(string)
	return value, ok, nil
}

func TestCreateRunnerUsesKeyPair(t *testing.T) {
	t.Parallel()
	const publicKey = "ssh-ed25519 AAAA example"
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{keyPairGVR: "KeyPairList", vmGVR: "VirtualMachineList"},
		testKeyPair("keys", "access", publicKey),
	)
	kube := kubefake.NewSimpleClientset()
	m := &Manager{
		client: &Client{Dynamic: dynamic, Kube: kube},
		cfg: config.Config{
			Namespace: "default", KeyPair: "keys/access", SSHUsername: "ubuntu",
			RunnerUser: "runner", RunnerHome: "/opt/actions-runner", SkipRunnerInstall: true,
			CPU: 1, Memory: "1Gi", DiskSize: "10Gi", ScaleSetName: "test",
			HostMappings:   []config.HostMapping{{Host: "git.internal", IP: "192.0.2.20"}},
			TrustedCACerts: []string{"test-cert"},
		},
		imageRef: "default/ubuntu", storageClass: "image-storage-class", networkRef: "default/network",
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	spec := RunnerSpec{Name: "test-runner", ScaleSetID: 42, JITConfig: "encoded-jit"}
	if err := m.CreateRunner(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	secret, err := kube.CoreV1().Secrets("default").Get(context.Background(), spec.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	userData := secret.StringData["userdata"]
	if !strings.Contains(userData, "name: ubuntu") || !strings.Contains(userData, publicKey) {
		t.Fatalf("cloud-init does not assign key to ubuntu: %s", userData)
	}
	if !strings.Contains(userData, "192.0.2.20 git.internal") ||
		!strings.Contains(userData, "manage_etc_hosts: false") ||
		!strings.Contains(userData, "trusted:") ||
		!strings.Contains(userData, "test-cert") {
		t.Fatalf("cloud-init Secret lacks runner host or CA settings: %s", userData)
	}
	if _, err := dynamic.Resource(vmGVR).Namespace("default").Get(context.Background(), spec.Name, metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := dynamic.Resource(keyPairGVR).Namespace("keys").Delete(context.Background(), "access", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	spec.Name = "another-runner"
	if err := m.CreateRunner(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "get keypair") {
		t.Fatalf("expected missing keypair error, got %v", err)
	}
	if _, err := kube.CoreV1().Secrets("default").Get(context.Background(), spec.Name, metav1.GetOptions{}); err == nil {
		t.Fatal("created cloud-init Secret after keypair disappeared")
	}
}
