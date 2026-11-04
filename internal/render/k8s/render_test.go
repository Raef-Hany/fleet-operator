package k8s

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	fleetv1alpha1 "github.com/Raef-Hany/fleet-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

var update = flag.Bool("update", false, "rewrite testdata golden files from the current Render output; review the diff before committing")

const (
	testTenant  = "acme"
	testVersion = "6.7.1"

	wantKeyManagedBy = "app.kubernetes.io/managed-by"
	wantKeyName      = "app.kubernetes.io/name"
	wantKeyTenant    = "fleet.example.com/tenant"
	wantKeyRing      = "fleet.example.com/ring"
)

func newInstance(tenant, version string, size fleetv1alpha1.Size, ring fleetv1alpha1.Ring) *fleetv1alpha1.Instance {
	return &fleetv1alpha1.Instance{
		Spec: fleetv1alpha1.InstanceSpec{
			Tenant:  tenant,
			Version: version,
			Size:    size,
			Ring:    ring,
			Region:  "us-central1",
		},
	}
}

// findObject returns the first object of type T with the given name, failing the test if absent.
func findObject[T client.Object](t *testing.T, objs []client.Object, name string) T {
	t.Helper()
	for _, obj := range objs {
		if o, ok := obj.(T); ok && o.GetName() == name {
			return o
		}
	}
	var zero T
	t.Fatalf("no %T named %q in rendered output", zero, name)
	return zero
}

func findDeployment(t *testing.T, objs []client.Object) *appsv1.Deployment {
	t.Helper()
	return findObject[*appsv1.Deployment](t, objs, "podinfo")
}

func findQuota(t *testing.T, objs []client.Object) *corev1.ResourceQuota {
	t.Helper()
	return findObject[*corev1.ResourceQuota](t, objs, "quota")
}

func findPolicy(t *testing.T, objs []client.Object, name string) *networkingv1.NetworkPolicy {
	t.Helper()
	return findObject[*networkingv1.NetworkPolicy](t, objs, name)
}

func checkResources(t *testing.T, label string, got corev1.ResourceList, want map[corev1.ResourceName]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: got %d resources %v, want %d", label, len(got), got, len(want))
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("%s: missing %s", label, name)
			continue
		}
		if g.Cmp(resource.MustParse(w)) != 0 {
			t.Errorf("%s: %s = %s, want %s", label, name, g.String(), w)
		}
	}
}

func TestRenderGolden(t *testing.T) {
	cases := []struct {
		name string
		size fleetv1alpha1.Size
	}{
		{"small", fleetv1alpha1.SizeSmall},
		{"medium", fleetv1alpha1.SizeMedium},
		{"large", fleetv1alpha1.SizeLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := newInstance(testTenant, testVersion, tc.size, fleetv1alpha1.RingProd)
			got, err := yaml.Marshal(Render(inst))
			if err != nil {
				t.Fatalf("marshal rendered objects: %v", err)
			}
			path := filepath.Join("testdata", tc.name+".golden.yaml")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("write golden %s: %v", path, err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s (generate with: go test ./internal/render/k8s/ -run TestRenderGolden -args -update): %v", path, err)
			}
			if string(got) != string(want) {
				t.Errorf("golden %s mismatch\n--- got ---\n%s", path, got)
			}
		})
	}
}

func TestRenderSizing(t *testing.T) {
	type sizeWant struct {
		quota  map[corev1.ResourceName]string
		ctrReq map[corev1.ResourceName]string
		ctrLim map[corev1.ResourceName]string
	}
	small := sizeWant{
		quota: map[corev1.ResourceName]string{
			corev1.ResourceRequestsCPU:    "500m",
			corev1.ResourceRequestsMemory: "512Mi",
			corev1.ResourceLimitsCPU:      "1",
			corev1.ResourceLimitsMemory:   "1Gi",
			corev1.ResourcePods:           "5",
		},
		ctrReq: map[corev1.ResourceName]string{corev1.ResourceCPU: "50m", corev1.ResourceMemory: "64Mi"},
		ctrLim: map[corev1.ResourceName]string{corev1.ResourceCPU: "200m", corev1.ResourceMemory: "128Mi"},
	}
	medium := sizeWant{
		quota: map[corev1.ResourceName]string{
			corev1.ResourceRequestsCPU:    "2",
			corev1.ResourceRequestsMemory: "2Gi",
			corev1.ResourceLimitsCPU:      "4",
			corev1.ResourceLimitsMemory:   "4Gi",
			corev1.ResourcePods:           "10",
		},
		ctrReq: map[corev1.ResourceName]string{corev1.ResourceCPU: "100m", corev1.ResourceMemory: "128Mi"},
		ctrLim: map[corev1.ResourceName]string{corev1.ResourceCPU: "500m", corev1.ResourceMemory: "256Mi"},
	}
	large := sizeWant{
		quota: map[corev1.ResourceName]string{
			corev1.ResourceRequestsCPU:    "4",
			corev1.ResourceRequestsMemory: "4Gi",
			corev1.ResourceLimitsCPU:      "8",
			corev1.ResourceLimitsMemory:   "8Gi",
			corev1.ResourcePods:           "20",
		},
		ctrReq: map[corev1.ResourceName]string{corev1.ResourceCPU: "250m", corev1.ResourceMemory: "256Mi"},
		ctrLim: map[corev1.ResourceName]string{corev1.ResourceCPU: "1", corev1.ResourceMemory: "512Mi"},
	}
	cases := []struct {
		name string
		size fleetv1alpha1.Size
		want sizeWant
	}{
		{"small", fleetv1alpha1.SizeSmall, small},
		{"medium", fleetv1alpha1.SizeMedium, medium},
		{"large", fleetv1alpha1.SizeLarge, large},
		{"empty size defaults to small", "", small},
		{"unknown size defaults to small", "xl", small},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := Render(newInstance(testTenant, testVersion, tc.size, fleetv1alpha1.RingProd))
			checkResources(t, "quota.hard", findQuota(t, objs).Spec.Hard, tc.want.quota)
			dep := findDeployment(t, objs)
			if len(dep.Spec.Template.Spec.Containers) != 1 {
				t.Fatalf("got %d containers, want 1", len(dep.Spec.Template.Spec.Containers))
			}
			ctr := dep.Spec.Template.Spec.Containers[0]
			checkResources(t, "container.requests", ctr.Resources.Requests, tc.want.ctrReq)
			checkResources(t, "container.limits", ctr.Resources.Limits, tc.want.ctrLim)
		})
	}
}

func TestRenderOrderAndNames(t *testing.T) {
	objs := Render(newInstance(testTenant, testVersion, fleetv1alpha1.SizeSmall, fleetv1alpha1.RingProd))
	want := []string{
		"Namespace/inst-acme",
		"ServiceAccount/podinfo",
		"ResourceQuota/quota",
		"NetworkPolicy/default-deny",
		"NetworkPolicy/allow-podinfo",
		"Deployment/podinfo",
		"Service/podinfo",
	}
	got := make([]string, 0, len(objs))
	for _, obj := range objs {
		got = append(got, obj.GetObjectKind().GroupVersionKind().Kind+"/"+obj.GetName())
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("object order mismatch\n got: %v\nwant: %v", got, want)
	}
}

func TestRenderTypeMeta(t *testing.T) {
	objs := Render(newInstance(testTenant, testVersion, fleetv1alpha1.SizeSmall, fleetv1alpha1.RingProd))
	wantAPIVersion := map[string]string{
		"Namespace":      "v1",
		"ServiceAccount": "v1",
		"ResourceQuota":  "v1",
		"NetworkPolicy":  "networking.k8s.io/v1",
		"Deployment":     "apps/v1",
		"Service":        "v1",
	}
	for _, obj := range objs {
		gvk := obj.GetObjectKind().GroupVersionKind()
		want, ok := wantAPIVersion[gvk.Kind]
		if !ok {
			t.Errorf("%s/%s: unexpected kind %q", gvk.Kind, obj.GetName(), gvk.Kind)
			continue
		}
		if got := gvk.GroupVersion().String(); got != want {
			t.Errorf("%s/%s: apiVersion = %q, want %q", gvk.Kind, obj.GetName(), got, want)
		}
	}
}

func TestDefaultDenyPolicy(t *testing.T) {
	objs := Render(newInstance(testTenant, testVersion, fleetv1alpha1.SizeSmall, fleetv1alpha1.RingProd))
	np := findPolicy(t, objs, "default-deny")
	if np.Namespace != "inst-acme" {
		t.Errorf("namespace = %q, want inst-acme", np.Namespace)
	}
	if len(np.Spec.PodSelector.MatchLabels) != 0 || len(np.Spec.PodSelector.MatchExpressions) != 0 {
		t.Errorf("podSelector = %+v, want empty (selects all pods)", np.Spec.PodSelector)
	}
	wantTypes := []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}
	if !reflect.DeepEqual(np.Spec.PolicyTypes, wantTypes) {
		t.Errorf("policyTypes = %v, want %v", np.Spec.PolicyTypes, wantTypes)
	}
	if len(np.Spec.Ingress) != 0 {
		t.Errorf("ingress rules = %d, want none", len(np.Spec.Ingress))
	}
	if len(np.Spec.Egress) != 0 {
		t.Errorf("egress rules = %d, want none", len(np.Spec.Egress))
	}
}

func TestAllowPodinfoPolicy(t *testing.T) {
	objs := Render(newInstance(testTenant, testVersion, fleetv1alpha1.SizeSmall, fleetv1alpha1.RingProd))
	np := findPolicy(t, objs, "allow-podinfo")
	if np.Namespace != "inst-acme" {
		t.Errorf("namespace = %q, want inst-acme", np.Namespace)
	}
	if got := np.Spec.PodSelector.MatchLabels; !reflect.DeepEqual(got, map[string]string{wantKeyName: "podinfo"}) {
		t.Errorf("podSelector matchLabels = %v, want name=podinfo", got)
	}
	wantTypes := []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}
	if !reflect.DeepEqual(np.Spec.PolicyTypes, wantTypes) {
		t.Fatalf("policyTypes = %v, want %v", np.Spec.PolicyTypes, wantTypes)
	}

	// Ingress: tcp/9898 from pods in the same namespace (empty podSelector).
	if len(np.Spec.Ingress) != 1 {
		t.Fatalf("ingress rules = %d, want 1", len(np.Spec.Ingress))
	}
	in := np.Spec.Ingress[0]
	if len(in.From) != 1 || in.From[0].PodSelector == nil {
		t.Fatalf("ingress from = %+v, want one podSelector peer", in.From)
	}
	if in.From[0].NamespaceSelector != nil || in.From[0].IPBlock != nil {
		t.Errorf("ingress peer must be same-namespace pods only, got %+v", in.From[0])
	}
	if len(in.From[0].PodSelector.MatchLabels) != 0 || len(in.From[0].PodSelector.MatchExpressions) != 0 {
		t.Errorf("ingress podSelector = %+v, want empty (all pods in namespace)", in.From[0].PodSelector)
	}
	if len(in.Ports) != 1 {
		t.Fatalf("ingress ports = %d, want 1", len(in.Ports))
	}
	if p := in.Ports[0]; p.Protocol == nil || *p.Protocol != corev1.ProtocolTCP || p.Port == nil || *p.Port != intstr.FromInt32(9898) {
		t.Errorf("ingress port = %+v, want TCP/9898", p)
	}

	// Egress: tcp+udp 53 to kube-system only.
	if len(np.Spec.Egress) != 1 {
		t.Fatalf("egress rules = %d, want 1", len(np.Spec.Egress))
	}
	eg := np.Spec.Egress[0]
	if len(eg.To) != 1 || eg.To[0].NamespaceSelector == nil {
		t.Fatalf("egress to = %+v, want one namespaceSelector peer", eg.To)
	}
	wantNS := map[string]string{"kubernetes.io/metadata.name": "kube-system"}
	if got := eg.To[0].NamespaceSelector.MatchLabels; !reflect.DeepEqual(got, wantNS) {
		t.Errorf("egress namespaceSelector = %v, want %v", got, wantNS)
	}
	if eg.To[0].PodSelector != nil || eg.To[0].IPBlock != nil {
		t.Errorf("egress peer must be kube-system namespace only, got %+v", eg.To[0])
	}
	if len(eg.Ports) != 2 {
		t.Fatalf("egress ports = %d, want 2 (tcp and udp 53)", len(eg.Ports))
	}
	seen := map[corev1.Protocol]bool{}
	for _, p := range eg.Ports {
		if p.Port == nil || *p.Port != intstr.FromInt32(53) || p.Protocol == nil {
			t.Errorf("egress port = %+v, want 53 with protocol", p)
			continue
		}
		seen[*p.Protocol] = true
	}
	if !seen[corev1.ProtocolTCP] || !seen[corev1.ProtocolUDP] {
		t.Errorf("egress protocols = %v, want TCP and UDP", seen)
	}
}

func TestPodinfoDeployment(t *testing.T) {
	objs := Render(newInstance(testTenant, "9.8.7", fleetv1alpha1.SizeSmall, fleetv1alpha1.RingProd))
	dep := findDeployment(t, objs)
	if dep.Namespace != "inst-acme" {
		t.Errorf("namespace = %q, want inst-acme", dep.Namespace)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 1 {
		t.Errorf("replicas = %v, want 1", dep.Spec.Replicas)
	}
	if dep.Spec.Selector == nil || !reflect.DeepEqual(dep.Spec.Selector.MatchLabels, map[string]string{wantKeyName: "podinfo"}) {
		t.Errorf("selector = %+v, want matchLabels name=podinfo", dep.Spec.Selector)
	}
	tmpl := dep.Spec.Template
	if got := tmpl.Labels[wantKeyName]; got != "podinfo" {
		t.Errorf("pod template name label = %q, want podinfo", got)
	}
	if tmpl.Spec.ServiceAccountName != "podinfo" {
		t.Errorf("serviceAccountName = %q, want podinfo", tmpl.Spec.ServiceAccountName)
	}
	if len(tmpl.Spec.Containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(tmpl.Spec.Containers))
	}
	ctr := tmpl.Spec.Containers[0]
	if ctr.Image != "ghcr.io/stefanprodan/podinfo:9.8.7" {
		t.Errorf("image = %q, want ghcr.io/stefanprodan/podinfo:9.8.7", ctr.Image)
	}
	if len(ctr.Ports) != 1 || ctr.Ports[0].ContainerPort != 9898 || ctr.Ports[0].Name != "http" {
		t.Errorf("ports = %+v, want one port named http on 9898", ctr.Ports)
	}
	wantPort := intstr.FromString("http")
	if ctr.ReadinessProbe == nil || ctr.ReadinessProbe.HTTPGet == nil {
		t.Fatalf("readinessProbe missing httpGet")
	}
	if h := ctr.ReadinessProbe.HTTPGet; h.Path != "/readyz" || h.Port != wantPort {
		t.Errorf("readinessProbe httpGet = %+v, want /readyz on port http", h)
	}
	if ctr.LivenessProbe == nil || ctr.LivenessProbe.HTTPGet == nil {
		t.Fatalf("livenessProbe missing httpGet")
	}
	if h := ctr.LivenessProbe.HTTPGet; h.Path != "/healthz" || h.Port != wantPort {
		t.Errorf("livenessProbe httpGet = %+v, want /healthz on port http", h)
	}
}

func TestService(t *testing.T) {
	objs := Render(newInstance(testTenant, testVersion, fleetv1alpha1.SizeSmall, fleetv1alpha1.RingProd))
	svc := findObject[*corev1.Service](t, objs, "podinfo")
	if svc.Namespace != "inst-acme" {
		t.Errorf("namespace = %q, want inst-acme", svc.Namespace)
	}
	if svc.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Errorf("type = %q, want ClusterIP", svc.Spec.Type)
	}
	if !reflect.DeepEqual(svc.Spec.Selector, map[string]string{wantKeyName: "podinfo"}) {
		t.Errorf("selector = %v, want name=podinfo", svc.Spec.Selector)
	}
	if len(svc.Spec.Ports) != 1 {
		t.Fatalf("ports = %d, want 1", len(svc.Spec.Ports))
	}
	p := svc.Spec.Ports[0]
	if p.Port != 9898 || p.TargetPort != intstr.FromString("http") {
		t.Errorf("port = %d targetPort = %v, want 9898 -> http", p.Port, p.TargetPort)
	}
}

func TestNamespaceNameLengthsAreValidDNS1123(t *testing.T) {
	tenants := []string{
		"a",
		strings.Repeat("a", 40),
		"ab" + strings.Repeat("-z", 19),
	}
	for _, tenant := range tenants {
		t.Run(tenant, func(t *testing.T) {
			objs := Render(newInstance(tenant, testVersion, fleetv1alpha1.SizeSmall, fleetv1alpha1.RingProd))
			wantNS := "inst-" + tenant
			ns := findObject[*corev1.Namespace](t, objs, wantNS)
			if len(ns.Name) > 63 {
				t.Errorf("namespace %q has length %d, want <= 63", ns.Name, len(ns.Name))
			}
			if errs := validation.IsDNS1123Label(ns.Name); len(errs) != 0 {
				t.Errorf("namespace %q is not a valid DNS-1123 label: %v", ns.Name, errs)
			}
		})
	}
}

func TestNamespacedObjectsLiveInTenantNamespace(t *testing.T) {
	objs := Render(newInstance(testTenant, testVersion, fleetv1alpha1.SizeSmall, fleetv1alpha1.RingProd))
	for _, obj := range objs {
		if _, ok := obj.(*corev1.Namespace); ok {
			if obj.GetNamespace() != "" {
				t.Errorf("Namespace/%s must be cluster-scoped, has namespace %q", obj.GetName(), obj.GetNamespace())
			}
			continue
		}
		if obj.GetNamespace() != "inst-acme" {
			t.Errorf("%s/%s namespace = %q, want inst-acme", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), obj.GetNamespace())
		}
	}
}

func TestLabelsOnEveryObject(t *testing.T) {
	objs := Render(newInstance(testTenant, testVersion, fleetv1alpha1.SizeSmall, fleetv1alpha1.RingTrial))
	if len(objs) == 0 {
		t.Fatal("no objects rendered")
	}
	for _, obj := range objs {
		id := obj.GetObjectKind().GroupVersionKind().Kind + "/" + obj.GetName()
		l := obj.GetLabels()
		if l[wantKeyManagedBy] != "fleet-operator" {
			t.Errorf("%s: %s = %q, want fleet-operator", id, wantKeyManagedBy, l[wantKeyManagedBy])
		}
		if l[wantKeyTenant] != testTenant {
			t.Errorf("%s: %s = %q, want %s", id, wantKeyTenant, l[wantKeyTenant], testTenant)
		}
		if l[wantKeyRing] != "trial" {
			t.Errorf("%s: %s = %q, want trial", id, wantKeyRing, l[wantKeyRing])
		}
	}
	dep := findDeployment(t, objs)
	if got := dep.Spec.Template.Labels[wantKeyTenant]; got != testTenant {
		t.Errorf("pod template tenant label = %q, want %s", got, testTenant)
	}
	if got := dep.Spec.Template.Labels[wantKeyManagedBy]; got != "fleet-operator" {
		t.Errorf("pod template managed-by label = %q, want fleet-operator", got)
	}
}

func TestDefaultsForEmptyRingAndSize(t *testing.T) {
	objs := Render(newInstance(testTenant, testVersion, "", ""))
	for _, obj := range objs {
		if got := obj.GetLabels()[wantKeyRing]; got != "prod" {
			t.Errorf("%s/%s ring label = %q, want prod for empty ring", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), got)
		}
	}
	if got := findQuota(t, objs).Spec.Hard[corev1.ResourcePods]; got.Cmp(resource.MustParse("5")) != 0 {
		t.Errorf("empty size: quota pods = %s, want 5 (small)", got.String())
	}
}

func TestVersionInImage(t *testing.T) {
	for _, v := range []string{"0.0.1", "10.20.30"} {
		t.Run(v, func(t *testing.T) {
			dep := findDeployment(t, Render(newInstance(testTenant, v, fleetv1alpha1.SizeSmall, fleetv1alpha1.RingProd)))
			want := "ghcr.io/stefanprodan/podinfo:" + v
			if got := dep.Spec.Template.Spec.Containers[0].Image; got != want {
				t.Errorf("image = %q, want %q", got, want)
			}
		})
	}
}

func TestRenderDeterministic(t *testing.T) {
	inst := newInstance(testTenant, testVersion, fleetv1alpha1.SizeMedium, fleetv1alpha1.RingInternal)
	a := Render(inst)
	b := Render(inst)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("two Render calls on the same input differ")
	}
}

func TestRenderDoesNotMutateInput(t *testing.T) {
	inst := newInstance(testTenant, testVersion, fleetv1alpha1.SizeLarge, fleetv1alpha1.RingProd)
	inst.Spec.Connectivity = &fleetv1alpha1.Connectivity{
		Mode:            fleetv1alpha1.ConnectivityPSC,
		AllowedProjects: []string{"proj-a"},
	}
	inst.Labels = map[string]string{"x": "y"}
	before := inst.DeepCopy()
	_ = Render(inst)
	if !reflect.DeepEqual(inst, before) {
		t.Errorf("Render mutated its input\n before: %+v\n after:  %+v", before, inst)
	}
}

func TestRenderNilInstance(t *testing.T) {
	if got := Render(nil); got != nil {
		t.Errorf("Render(nil) = %v, want nil", got)
	}
}
