package v1alpha1_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var (
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
)

var gvk = schema.GroupVersionKind{Group: "fleet.example.com", Version: "v1alpha1", Kind: "Instance"}

func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "envtest start failed:", err)
		os.Exit(1)
	}
	k8sClient, err = client.New(cfg, client.Options{Scheme: runtime.NewScheme()})
	if err != nil {
		fmt.Fprintln(os.Stderr, "client failed:", err)
		_ = testEnv.Stop()
		os.Exit(1)
	}
	code := m.Run()
	_ = testEnv.Stop()
	os.Exit(code)
}

func newInstance(name string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name},
		"spec":     spec,
	}}
	u.SetGroupVersionKind(gvk)
	return u
}

func TestValidMinimalInstanceGetsDefaults(t *testing.T) {
	ctx := context.Background()
	u := newInstance("minimal", map[string]any{"tenant": "acme", "version": "6.5.0"})
	if err := k8sClient.Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(gvk)
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "minimal"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	want := map[string]string{
		"spec.size":   "small",
		"spec.region": "us-central1",
		"spec.ring":   "prod",
		"spec.tenant": "acme",
	}
	for path, w := range want {
		v, found, _ := unstructured.NestedString(got.Object, strings.Split(path, ".")...)
		if !found || v != w {
			t.Errorf("%s = %q (found=%v), want %q", path, v, found, w)
		}
	}
	mode, found, _ := unstructured.NestedString(got.Object, "spec", "connectivity", "mode")
	if found && mode != "none" {
		t.Errorf("connectivity.mode = %q, want none when connectivity is present", mode)
	}
}

func TestInvalidInstancesRejected(t *testing.T) {
	long := strings.Repeat("a", 41)
	exactly40 := strings.Repeat("a", 40)
	tests := []struct {
		name    string
		spec    map[string]any
		wantErr bool
	}{
		{"uppercase tenant", map[string]any{"tenant": "Acme", "version": "1.0.0"}, true},
		{"41 char tenant", map[string]any{"tenant": long, "version": "1.0.0"}, true},
		{"40 char tenant is ok", map[string]any{"tenant": exactly40, "version": "1.0.0"}, false},
		{"empty tenant", map[string]any{"tenant": "", "version": "1.0.0"}, true},
		{"missing tenant", map[string]any{"version": "1.0.0"}, true},
		{"tenant with underscore", map[string]any{"tenant": "a_b", "version": "1.0.0"}, true},
		{"tenant leading dash", map[string]any{"tenant": "-ab", "version": "1.0.0"}, true},
		{"missing version", map[string]any{"tenant": "ok"}, true},
		{"version v-prefixed", map[string]any{"tenant": "ok", "version": "v1.2.3"}, true},
		{"version two parts", map[string]any{"tenant": "ok", "version": "1.2"}, true},
		{"version prerelease", map[string]any{"tenant": "ok", "version": "1.2.3-rc1"}, true},
		{"bad size", map[string]any{"tenant": "ok", "version": "1.2.3", "size": "huge"}, true},
		{"bad ring", map[string]any{"tenant": "ok", "version": "1.2.3", "ring": "canary"}, true},
		{"bad mode", map[string]any{"tenant": "ok", "version": "1.2.3",
			"connectivity": map[string]any{"mode": "vpn"}}, true},
		{"psc without projects", map[string]any{"tenant": "ok", "version": "1.2.3",
			"connectivity": map[string]any{"mode": "psc"}}, true},
		{"psc with empty projects", map[string]any{"tenant": "ok", "version": "1.2.3",
			"connectivity": map[string]any{"mode": "psc", "allowedProjects": []any{}}}, true},
		{"privatelink without projects", map[string]any{"tenant": "ok", "version": "1.2.3",
			"connectivity": map[string]any{"mode": "privatelink"}}, true},
		{"psc with projects ok", map[string]any{"tenant": "ok", "version": "1.2.3",
			"connectivity": map[string]any{"mode": "psc", "allowedProjects": []any{"proj-a"}}}, false},
		{"none without projects ok", map[string]any{"tenant": "ok", "version": "1.2.3",
			"connectivity": map[string]any{"mode": "none"}}, false},
		{"too many projects", map[string]any{"tenant": "ok", "version": "1.2.3",
			"connectivity": map[string]any{"mode": "psc", "allowedProjects": manyProjects(11)}}, true},
		{"ten projects ok", map[string]any{"tenant": "ok", "version": "1.2.3",
			"connectivity": map[string]any{"mode": "psc", "allowedProjects": manyProjects(10)}}, false},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := newInstance(fmt.Sprintf("case-%d", i), tc.spec)
			err := k8sClient.Create(context.Background(), u)
			if tc.wantErr && err == nil {
				t.Fatalf("expected rejection, object was accepted")
			}
			if tc.wantErr && !apierrors.IsInvalid(err) {
				t.Fatalf("expected Invalid error, got: %v", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected acceptance, got: %v", err)
			}
		})
	}
}

func manyProjects(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = fmt.Sprintf("proj-%d", i)
	}
	return out
}

func TestTenantIsImmutable(t *testing.T) {
	ctx := context.Background()
	u := newInstance("immutable", map[string]any{"tenant": "first", "version": "1.0.0"})
	if err := k8sClient.Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}
	u.Object["spec"].(map[string]any)["tenant"] = "second"
	err := k8sClient.Update(ctx, u)
	if err == nil {
		t.Fatalf("expected tenant change to be rejected")
	}
	if !apierrors.IsInvalid(err) {
		t.Fatalf("expected Invalid error, got: %v", err)
	}

	// Changing a mutable field must still work.
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(gvk)
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "immutable"}, got); err != nil {
		t.Fatal(err)
	}
	got.Object["spec"].(map[string]any)["version"] = "1.0.1"
	if err := k8sClient.Update(ctx, got); err != nil {
		t.Fatalf("version update should be allowed: %v", err)
	}
}

func TestInstanceIsClusterScoped(t *testing.T) {
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"})
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "instances.fleet.example.com"}, crd); err != nil {
		t.Fatalf("get crd: %v", err)
	}
	scope, _, _ := unstructured.NestedString(crd.Object, "spec", "scope")
	if scope != "Cluster" {
		t.Fatalf("CRD scope = %q, want Cluster", scope)
	}
}
