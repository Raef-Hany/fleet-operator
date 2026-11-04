// Package k8s renders the Kubernetes objects for a fleet Instance.
package k8s

import (
	fleetv1alpha1 "github.com/Raef-Hany/fleet-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	podinfoName      = "podinfo"
	podinfoImageRepo = "ghcr.io/stefanprodan/podinfo"
	podinfoPort      = 9898

	keyManagedBy = "app.kubernetes.io/managed-by"
	keyName      = "app.kubernetes.io/name"
	keyTenant    = "fleet.example.com/tenant"
	keyRing      = "fleet.example.com/ring"
)

// sizing holds the quota and per-container resources for one Size.
type sizing struct {
	quotaReqCPU, quotaReqMem, quotaLimCPU, quotaLimMem, quotaPods string
	ctrReqCPU, ctrReqMem, ctrLimCPU, ctrLimMem                    string
}

var sizings = map[fleetv1alpha1.Size]sizing{
	fleetv1alpha1.SizeSmall: {
		quotaReqCPU: "500m", quotaReqMem: "512Mi", quotaLimCPU: "1", quotaLimMem: "1Gi", quotaPods: "5",
		ctrReqCPU: "50m", ctrReqMem: "64Mi", ctrLimCPU: "200m", ctrLimMem: "128Mi",
	},
	fleetv1alpha1.SizeMedium: {
		quotaReqCPU: "2", quotaReqMem: "2Gi", quotaLimCPU: "4", quotaLimMem: "4Gi", quotaPods: "10",
		ctrReqCPU: "100m", ctrReqMem: "128Mi", ctrLimCPU: "500m", ctrLimMem: "256Mi",
	},
	fleetv1alpha1.SizeLarge: {
		quotaReqCPU: "4", quotaReqMem: "4Gi", quotaLimCPU: "8", quotaLimMem: "8Gi", quotaPods: "20",
		ctrReqCPU: "250m", ctrReqMem: "256Mi", ctrLimCPU: "1", ctrLimMem: "512Mi",
	},
}

// Render returns the Kubernetes objects for inst, in apply order. It is pure:
// it does not modify inst and performs no I/O. A nil inst yields nil.
func Render(inst *fleetv1alpha1.Instance) []client.Object {
	if inst == nil {
		return nil
	}
	tenant := inst.Spec.Tenant
	ring := string(inst.Spec.Ring)
	if ring == "" {
		ring = string(fleetv1alpha1.RingProd)
	}
	sz, ok := sizings[inst.Spec.Size]
	if !ok {
		sz = sizings[fleetv1alpha1.SizeSmall]
	}
	ns := "inst-" + tenant

	// labels returns a fresh label map for each object, merged with extra.
	labels := func(extra map[string]string) map[string]string {
		m := map[string]string{
			keyManagedBy: "fleet-operator",
			keyTenant:    tenant,
			keyRing:      ring,
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	selectorLabels := map[string]string{keyName: podinfoName}

	namespace := &corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: labels(nil)},
	}

	serviceAccount := &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: metav1.ObjectMeta{Name: podinfoName, Namespace: ns, Labels: labels(nil)},
	}

	quota := &corev1.ResourceQuota{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ResourceQuota"},
		ObjectMeta: metav1.ObjectMeta{Name: "quota", Namespace: ns, Labels: labels(nil)},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				corev1.ResourceRequestsCPU:    resource.MustParse(sz.quotaReqCPU),
				corev1.ResourceRequestsMemory: resource.MustParse(sz.quotaReqMem),
				corev1.ResourceLimitsCPU:      resource.MustParse(sz.quotaLimCPU),
				corev1.ResourceLimitsMemory:   resource.MustParse(sz.quotaLimMem),
				corev1.ResourcePods:           resource.MustParse(sz.quotaPods),
			},
		},
	}

	defaultDeny := &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: "default-deny", Namespace: ns, Labels: labels(nil)},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		},
	}

	dnsPort := intstr.FromInt32(53)
	allowPodinfo := &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: "allow-podinfo", Namespace: ns, Labels: labels(nil)},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: selectorLabels},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}},
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: ptrTo(corev1.ProtocolTCP),
					Port:     ptrTo(intstr.FromInt32(podinfoPort)),
				}},
			}},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
				}}},
				Ports: []networkingv1.NetworkPolicyPort{
					{Protocol: ptrTo(corev1.ProtocolTCP), Port: ptrTo(dnsPort)},
					{Protocol: ptrTo(corev1.ProtocolUDP), Port: ptrTo(dnsPort)},
				},
			}},
		},
	}

	replicas := int32(1)
	probe := func(path string) *corev1.Probe {
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
			Path: path,
			Port: intstr.FromString("http"),
		}}}
	}
	deployment := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: podinfoName, Namespace: ns, Labels: labels(selectorLabels)},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: selectorLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels(selectorLabels)},
				Spec: corev1.PodSpec{
					ServiceAccountName: podinfoName,
					Containers: []corev1.Container{{
						Name:  podinfoName,
						Image: podinfoImageRepo + ":" + inst.Spec.Version,
						Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: podinfoPort}},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse(sz.ctrReqCPU),
								corev1.ResourceMemory: resource.MustParse(sz.ctrReqMem),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse(sz.ctrLimCPU),
								corev1.ResourceMemory: resource.MustParse(sz.ctrLimMem),
							},
						},
						ReadinessProbe: probe("/readyz"),
						LivenessProbe:  probe("/healthz"),
					}},
				},
			},
		},
	}

	service := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: podinfoName, Namespace: ns, Labels: labels(nil)},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selectorLabels,
			Ports: []corev1.ServicePort{{
				Protocol:   corev1.ProtocolTCP,
				Port:       podinfoPort,
				TargetPort: intstr.FromString("http"),
			}},
		},
	}

	return []client.Object{
		namespace,
		serviceAccount,
		quota,
		defaultDeny,
		allowPodinfo,
		deployment,
		service,
	}
}

func ptrTo[T any](v T) *T {
	return &v
}
