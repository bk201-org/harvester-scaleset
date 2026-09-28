package harvester

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/bk201-org/harvester-scaleset/internal/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func TestPreflightRunnerImage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		resourceName string
		displayName  string
		images       []*unstructured.Unstructured
		wantRef      string
		wantErr      string
	}{
		{
			name:         "resource name selects the named image",
			resourceName: "default/ubuntu.qcow2",
			images: []*unstructured.Unstructured{
				testRunnerImage("default", "ubuntu.qcow2", "other.qcow2"),
				testRunnerImage("default", "image-123", "ubuntu.qcow2"),
			},
			wantRef: "default/ubuntu.qcow2",
		},
		{
			name:         "resource name does not resolve display name",
			resourceName: "default/ubuntu.qcow2",
			images: []*unstructured.Unstructured{
				testRunnerImage("default", "image-8w4dt", "ubuntu.qcow2"),
			},
			wantErr: "not found",
		},
		{
			name:        "display name selects matching image even when a resource has that name",
			displayName: "default/ubuntu.qcow2",
			images: []*unstructured.Unstructured{
				testRunnerImage("default", "ubuntu.qcow2", "other.qcow2"),
				testRunnerImage("other", "image-other", "ubuntu.qcow2"),
				testRunnerImage("default", "image-8w4dt", "ubuntu.qcow2"),
			},
			wantRef: "default/image-8w4dt",
		},
		{
			name:        "unqualified display name uses runner namespace",
			displayName: "ubuntu.qcow2",
			images: []*unstructured.Unstructured{
				testRunnerImage("default", "image-8w4dt", "ubuntu.qcow2"),
			},
			wantRef: "default/image-8w4dt",
		},
		{
			name:        "duplicate display names are rejected",
			displayName: "default/ubuntu.qcow2",
			images: []*unstructured.Unstructured{
				testRunnerImage("default", "image-a", "ubuntu.qcow2"),
				testRunnerImage("default", "image-b", "ubuntu.qcow2"),
			},
			wantErr: "matches multiple images",
		},
		{
			name:        "unknown display name is rejected",
			displayName: "default/missing.qcow2",
			images: []*unstructured.Unstructured{
				testRunnerImage("default", "image-8w4dt", "ubuntu.qcow2"),
			},
			wantErr: "no image with displayName",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []runtime.Object
			for _, image := range tt.images {
				objects = append(objects, image)
			}
			dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
				runtime.NewScheme(),
				map[schema.GroupVersionResource]string{imageGVR: "VirtualMachineImageList"},
				objects...,
			)
			_, err := dynamic.Resource(networkGVR).Namespace("default").Create(
				context.Background(),
				&unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "k8s.cni.cncf.io/v1",
					"kind":       "NetworkAttachmentDefinition",
					"metadata": map[string]any{
						"name":      "network",
						"namespace": "default",
					},
				}},
				metav1.CreateOptions{},
			)
			if err != nil {
				t.Fatal(err)
			}
			m := &Manager{
				client: &Client{
					Dynamic: dynamic,
					Kube: kubefake.NewSimpleClientset(&corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{Name: "default"},
					}),
				},
				cfg: config.Config{
					Namespace:              "default",
					RunnerImage:            tt.resourceName,
					RunnerImageDisplayName: tt.displayName,
					Network:                "default/network",
					DiskSize:               "10Gi",
				},
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			err = m.Preflight(context.Background())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Preflight error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if m.imageRef != tt.wantRef {
				t.Fatalf("image reference = %q, want %q", m.imageRef, tt.wantRef)
			}
		})
	}
}

func testRunnerImage(namespace, name, displayName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "harvesterhci.io/v1beta1",
		"kind":       "VirtualMachineImage",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]any{
			"displayName": displayName,
		},
		"status": map[string]any{
			"storageClassName": "harvester-longhorn",
			"conditions": []any{
				map[string]any{"type": "Imported", "status": "True"},
			},
		},
	}}
}

func TestPreflightKeyPair(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		keyPair *unstructured.Unstructured
		wantErr string
	}{
		{name: "existing keypair", keyPair: testKeyPair("keys", "access", "ssh-ed25519 AAAA example")},
		{name: "missing keypair", wantErr: "get keypair keys/access"},
		{name: "empty public key", keyPair: testKeyPair("keys", "access", ""), wantErr: "no valid single-line public key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			objects := []runtime.Object{testRunnerImage("default", "ubuntu", "ubuntu")}
			if tt.keyPair != nil {
				objects = append(objects, tt.keyPair)
			}
			dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
				runtime.NewScheme(),
				map[schema.GroupVersionResource]string{imageGVR: "VirtualMachineImageList", keyPairGVR: "KeyPairList"},
				objects...,
			)
			_, err := dynamic.Resource(networkGVR).Namespace("default").Create(context.Background(), &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "k8s.cni.cncf.io/v1",
				"kind":       "NetworkAttachmentDefinition",
				"metadata":   map[string]any{"name": "network", "namespace": "default"},
			}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			m := &Manager{
				client: &Client{Dynamic: dynamic, Kube: kubefake.NewSimpleClientset(&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: "default"},
				})},
				cfg: config.Config{
					Namespace: "default", RunnerImage: "default/ubuntu", Network: "default/network",
					DiskSize: "10Gi", KeyPair: "keys/access",
				},
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			err = m.Preflight(context.Background())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Preflight error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func testKeyPair(namespace, name, publicKey string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "harvesterhci.io/v1beta1",
		"kind":       "KeyPair",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec":       map[string]any{"publicKey": publicKey},
	}}
}
