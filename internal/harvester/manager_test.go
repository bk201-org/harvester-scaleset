package harvester

import (
	"encoding/json"
	"testing"

	"github.com/bk201-org/harvester-scaleset/internal/config"
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
