package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	fleetv1alpha1 "github.com/Raef-Hany/fleet-operator/api/v1alpha1"
)

const testPodinfoRepo = "ghcr.io/stefanprodan/podinfo"

// newTestReconciler returns a reconciler wired to c and a fake recorder that the test can inspect.
func newTestReconciler(c client.Client) (*InstanceReconciler, *events.FakeRecorder) {
	rec := events.NewFakeRecorder(100)
	return &InstanceReconciler{Client: c, Scheme: testScheme, Recorder: rec}, rec
}

func createInstance(t *testing.T, name, tenant, version string) {
	t.Helper()
	inst := &fleetv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       fleetv1alpha1.InstanceSpec{Tenant: tenant, Version: version},
	}
	if err := k8sClient.Create(context.Background(), inst); err != nil {
		t.Fatalf("create instance %s: %v", name, err)
	}
}

func reconcileName(t *testing.T, r *InstanceReconciler, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
	if err != nil {
		t.Fatalf("Reconcile(%s): %v", name, err)
	}
	return res
}

func getInstance(t *testing.T, name string) *fleetv1alpha1.Instance {
	t.Helper()
	inst := &fleetv1alpha1.Instance{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: name}, inst); err != nil {
		t.Fatalf("get instance %s: %v", name, err)
	}
	return inst
}

func getDeployment(t *testing.T, tenant string) *appsv1.Deployment {
	t.Helper()
	d := &appsv1.Deployment{}
	key := client.ObjectKey{Namespace: "inst-" + tenant, Name: "podinfo"}
	if err := k8sClient.Get(context.Background(), key, d); err != nil {
		t.Fatalf("get deployment %s: %v", key, err)
	}
	return d
}

// markDeploymentReady plays the role of the deployment controller, which envtest does not run.
func markDeploymentReady(t *testing.T, tenant string) {
	t.Helper()
	d := getDeployment(t, tenant)
	d.Status = appsv1.DeploymentStatus{
		ObservedGeneration: d.Generation,
		Replicas:           1,
		UpdatedReplicas:    1,
		ReadyReplicas:      1,
		AvailableReplicas:  1,
	}
	if err := k8sClient.Status().Update(context.Background(), d); err != nil {
		t.Fatalf("set deployment status: %v", err)
	}
}

// forceFinalizeNamespace plays the role of the namespace controller: it clears spec.finalizers
// through the finalize subresource and waits until the namespace is gone.
func forceFinalizeNamespace(t *testing.T, ns string) {
	t.Helper()
	ctx := context.Background()
	live := &corev1.Namespace{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: ns}, live); err != nil {
		t.Fatalf("get namespace %s: %v", ns, err)
	}
	live.Spec.Finalizers = nil
	if _, err := clientset.CoreV1().Namespaces().Finalize(ctx, live, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("finalize namespace %s: %v", ns, err)
	}
	err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		gone := &corev1.Namespace{}
		getErr := k8sClient.Get(ctx, client.ObjectKey{Name: ns}, gone)
		if apierrors.IsNotFound(getErr) {
			return true, nil
		}
		return false, getErr
	})
	if err != nil {
		t.Fatalf("namespace %s was not removed after finalize: %v", ns, err)
	}
}

func drainEvents(rec *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func assertCondition(t *testing.T, inst *fleetv1alpha1.Instance, condType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	c := meta.FindStatusCondition(inst.Status.Conditions, condType)
	if c == nil {
		t.Fatalf("condition %s missing; conditions=%v", condType, inst.Status.Conditions)
	}
	if c.Status != status || c.Reason != reason {
		t.Errorf("condition %s = %s/%s, want %s/%s (message %q)", condType, c.Status, c.Reason, status, reason, c.Message)
	}
}

func TestCreateProvisionsKubernetesObjects(t *testing.T) {
	ctx := context.Background()
	const name, tenant = "create-basic", "acme-create"
	createInstance(t, name, tenant, "6.5.0")
	r, _ := newTestReconciler(k8sClient)

	res := reconcileName(t, r, name)
	inst := getInstance(t, name)

	if !controllerutil.ContainsFinalizer(inst, instanceFinalizer) {
		t.Errorf("finalizer %q missing; finalizers=%v", instanceFinalizer, inst.Finalizers)
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("Provisioning instance should requeue, got %+v", res)
	}
	if inst.Status.Phase != fleetv1alpha1.PhaseProvisioning {
		t.Errorf("phase = %q, want Provisioning", inst.Status.Phase)
	}
	if inst.Status.ObservedGeneration != inst.Generation {
		t.Errorf("observedGeneration = %d, want %d", inst.Status.ObservedGeneration, inst.Generation)
	}
	assertCondition(t, inst, fleetv1alpha1.ConditionKubernetesReady, metav1.ConditionFalse, reasonDeploymentProgressing)
	assertCondition(t, inst, fleetv1alpha1.ConditionReady, metav1.ConditionFalse, string(fleetv1alpha1.PhaseProvisioning))

	ns := "inst-" + tenant
	owned := []struct {
		desc string
		key  client.ObjectKey
		obj  client.Object
	}{
		{"namespace", client.ObjectKey{Name: ns}, &corev1.Namespace{}},
		{"serviceaccount", client.ObjectKey{Namespace: ns, Name: "podinfo"}, &corev1.ServiceAccount{}},
		{"resourcequota", client.ObjectKey{Namespace: ns, Name: "quota"}, &corev1.ResourceQuota{}},
		{"networkpolicy default-deny", client.ObjectKey{Namespace: ns, Name: "default-deny"}, &networkingv1.NetworkPolicy{}},
		{"networkpolicy allow-podinfo", client.ObjectKey{Namespace: ns, Name: "allow-podinfo"}, &networkingv1.NetworkPolicy{}},
		{"deployment", client.ObjectKey{Namespace: ns, Name: "podinfo"}, &appsv1.Deployment{}},
		{"service", client.ObjectKey{Namespace: ns, Name: "podinfo"}, &corev1.Service{}},
	}
	for _, o := range owned {
		if err := k8sClient.Get(ctx, o.key, o.obj); err != nil {
			t.Errorf("%s not created: %v", o.desc, err)
			continue
		}
		owner := metav1.GetControllerOf(o.obj)
		if owner == nil || owner.UID != inst.UID || owner.Kind != "Instance" || owner.Name != name {
			t.Errorf("%s controller owner = %+v, want Instance %s uid %s", o.desc, owner, name, inst.UID)
		}
	}

	d := getDeployment(t, tenant)
	want := testPodinfoRepo + ":6.5.0"
	if got := d.Spec.Template.Spec.Containers[0].Image; got != want {
		t.Errorf("deployment image = %q, want %q", got, want)
	}
}

func TestDeploymentAvailableMakesInstanceReady(t *testing.T) {
	const name, tenant = "ready-basic", "acme-ready"
	createInstance(t, name, tenant, "6.5.0")
	r, _ := newTestReconciler(k8sClient)
	reconcileName(t, r, name)
	markDeploymentReady(t, tenant)

	res := reconcileName(t, r, name)
	if res != (ctrl.Result{}) {
		t.Errorf("Ready instance should return empty Result, got %+v", res)
	}
	inst := getInstance(t, name)
	if inst.Status.Phase != fleetv1alpha1.PhaseReady {
		t.Errorf("phase = %q, want Ready", inst.Status.Phase)
	}
	assertCondition(t, inst, fleetv1alpha1.ConditionKubernetesReady, metav1.ConditionTrue, reasonDeploymentAvailable)
	assertCondition(t, inst, fleetv1alpha1.ConditionReady, metav1.ConditionTrue, string(fleetv1alpha1.PhaseReady))
}

func TestVersionBumpRollsOutNewImageAndTracksGeneration(t *testing.T) {
	ctx := context.Background()
	const name, tenant = "bump-version", "acme-bump"
	createInstance(t, name, tenant, "6.5.0")
	r, _ := newTestReconciler(k8sClient)
	reconcileName(t, r, name)
	markDeploymentReady(t, tenant)
	reconcileName(t, r, name)

	inst := getInstance(t, name)
	if inst.Generation != 1 || inst.Status.ObservedGeneration != 1 {
		t.Fatalf("before bump: generation=%d observedGeneration=%d, want 1/1", inst.Generation, inst.Status.ObservedGeneration)
	}
	inst.Spec.Version = "6.5.1"
	if err := k8sClient.Update(ctx, inst); err != nil {
		t.Fatalf("bump version: %v", err)
	}

	res := reconcileName(t, r, name)
	if res.RequeueAfter <= 0 {
		t.Errorf("rollout in progress should requeue, got %+v", res)
	}
	d := getDeployment(t, tenant)
	if got, want := d.Spec.Template.Spec.Containers[0].Image, testPodinfoRepo+":6.5.1"; got != want {
		t.Errorf("deployment image = %q, want %q", got, want)
	}
	inst = getInstance(t, name)
	if inst.Status.ObservedGeneration != 2 || inst.Status.ObservedGeneration != inst.Generation {
		t.Errorf("observedGeneration = %d, generation = %d, want both 2", inst.Status.ObservedGeneration, inst.Generation)
	}
	if inst.Status.Phase != fleetv1alpha1.PhaseProvisioning {
		t.Errorf("phase = %q during rollout, want Provisioning", inst.Status.Phase)
	}
	assertCondition(t, inst, fleetv1alpha1.ConditionKubernetesReady, metav1.ConditionFalse, reasonDeploymentProgressing)
}

func TestDeletedDeploymentIsRecreated(t *testing.T) {
	ctx := context.Background()
	const name, tenant = "drift-deployment", "acme-drift"
	createInstance(t, name, tenant, "6.5.0")
	r, _ := newTestReconciler(k8sClient)
	reconcileName(t, r, name)

	if err := k8sClient.Delete(ctx, getDeployment(t, tenant)); err != nil {
		t.Fatalf("delete deployment: %v", err)
	}
	reconcileName(t, r, name)

	d := getDeployment(t, tenant)
	if got, want := d.Spec.Template.Spec.Containers[0].Image, testPodinfoRepo+":6.5.0"; got != want {
		t.Errorf("recreated deployment image = %q, want %q", got, want)
	}
}

func TestManualReplicaEditIsCorrected(t *testing.T) {
	ctx := context.Background()
	const name, tenant = "drift-replicas", "acme-replicas"
	createInstance(t, name, tenant, "6.5.0")
	r, _ := newTestReconciler(k8sClient)
	reconcileName(t, r, name)

	d := getDeployment(t, tenant)
	five := int32(5)
	d.Spec.Replicas = &five
	if err := k8sClient.Update(ctx, d); err != nil {
		t.Fatalf("manual edit: %v", err)
	}

	reconcileName(t, r, name)
	d = getDeployment(t, tenant)
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 {
		t.Errorf("replicas = %v after reconcile, want 1 (drift not corrected)", d.Spec.Replicas)
	}
}

// writeCounter counts persisted writes issued through a client. Dry-run requests are not persisted and are not counted.
type writeCounter struct {
	writes []string
}

func (w *writeCounter) wrap(base client.WithWatch) client.Client {
	note := func(dryRun []string, op string, obj client.Object) {
		if len(dryRun) == 0 {
			w.writes = append(w.writes, fmt.Sprintf("%s %T %s", op, obj, obj.GetName()))
		}
	}
	return interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			o := &client.CreateOptions{}
			o.ApplyOptions(opts)
			note(o.DryRun, "create", obj)
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			o := &client.UpdateOptions{}
			o.ApplyOptions(opts)
			note(o.DryRun, "update", obj)
			return c.Update(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			o := &client.PatchOptions{}
			o.ApplyOptions(opts)
			note(o.DryRun, "patch", obj)
			return c.Patch(ctx, obj, patch, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			o := &client.DeleteOptions{}
			o.ApplyOptions(opts)
			note(o.DryRun, "delete", obj)
			return c.Delete(ctx, obj, opts...)
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			o := &client.SubResourceUpdateOptions{}
			o.ApplyOptions(opts)
			note(o.DryRun, "update/"+sub, obj)
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			o := &client.SubResourcePatchOptions{}
			o.ApplyOptions(opts)
			note(o.DryRun, "patch/"+sub, obj)
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		},
	})
}

func TestSteadyStateReconcileIssuesNoPersistedWrites(t *testing.T) {
	const name, tenant = "steady-state", "acme-steady"
	createInstance(t, name, tenant, "6.5.0")
	r, _ := newTestReconciler(k8sClient)
	reconcileName(t, r, name)
	markDeploymentReady(t, tenant)
	reconcileName(t, r, name) // settle into Ready

	before := getInstance(t, name)
	counter := &writeCounter{}
	steady, _ := newTestReconciler(counter.wrap(k8sClient))
	res := reconcileName(t, steady, name)

	if len(counter.writes) != 0 {
		t.Errorf("steady-state reconcile persisted %d writes: %v", len(counter.writes), counter.writes)
	}
	if res != (ctrl.Result{}) {
		t.Errorf("steady-state Result = %+v, want empty", res)
	}
	after := getInstance(t, name)
	if after.ResourceVersion != before.ResourceVersion {
		t.Errorf("instance resourceVersion changed %s -> %s", before.ResourceVersion, after.ResourceVersion)
	}
}

func TestNamespaceConflictFailsWithoutAdoption(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
	}{
		{"unlabelled namespace", nil},
		{"managed by fleet but other tenant", map[string]string{
			"app.kubernetes.io/managed-by": "fleet-operator",
			"fleet.example.com/tenant":     "someone-else",
		}},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tenant := fmt.Sprintf("conflict-%d", i)
			name := "conflict-instance-" + fmt.Sprint(i)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "inst-" + tenant, Labels: tc.labels}}
			if err := k8sClient.Create(ctx, ns); err != nil {
				t.Fatalf("create pre-existing namespace: %v", err)
			}
			createInstance(t, name, tenant, "6.5.0")
			r, rec := newTestReconciler(k8sClient)

			res := reconcileName(t, r, name)
			if res != (ctrl.Result{}) {
				t.Errorf("conflict Result = %+v, want empty (no requeue)", res)
			}
			inst := getInstance(t, name)
			if inst.Status.Phase != fleetv1alpha1.PhaseFailed {
				t.Errorf("phase = %q, want Failed", inst.Status.Phase)
			}
			assertCondition(t, inst, fleetv1alpha1.ConditionKubernetesReady, metav1.ConditionFalse, reasonNamespaceConflict)
			if c := meta.FindStatusCondition(inst.Status.Conditions, fleetv1alpha1.ConditionKubernetesReady); c == nil || c.Message == "" {
				t.Errorf("KubernetesReady message empty")
			}

			got := drainEvents(rec)
			if len(got) != 1 || !strings.HasPrefix(got[0], "Warning "+reasonNamespaceConflict) {
				t.Errorf("events = %v, want one Warning %s", got, reasonNamespaceConflict)
			}

			live := &corev1.Namespace{}
			if err := k8sClient.Get(ctx, client.ObjectKey{Name: ns.Name}, live); err != nil {
				t.Fatalf("get namespace: %v", err)
			}
			if len(live.OwnerReferences) != 0 {
				t.Errorf("pre-existing namespace was adopted: ownerReferences=%v", live.OwnerReferences)
			}
			if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: "podinfo"}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
				t.Errorf("deployment created inside conflicting namespace, get err = %v", err)
			}

			res = reconcileName(t, r, name)
			if res != (ctrl.Result{}) {
				t.Errorf("second conflict Result = %+v, want empty", res)
			}
			if again := drainEvents(rec); len(again) != 0 {
				t.Errorf("steady conflict emitted events: %v", again)
			}
		})
	}
}

func TestNotFoundInstanceIsNoOp(t *testing.T) {
	r, _ := newTestReconciler(k8sClient)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "no-such-instance"}})
	if err != nil {
		t.Fatalf("NotFound instance returned error: %v", err)
	}
	if res != (ctrl.Result{}) {
		t.Errorf("NotFound Result = %+v, want empty", res)
	}
}

func TestDeletionWaitsForNamespaceTerminationThenReleasesFinalizer(t *testing.T) {
	ctx := context.Background()
	const name, tenant = "delete-wait", "acme-delwait"
	createInstance(t, name, tenant, "6.5.0")
	r, rec := newTestReconciler(k8sClient)
	reconcileName(t, r, name)
	markDeploymentReady(t, tenant)
	reconcileName(t, r, name)
	drainEvents(rec)

	if err := k8sClient.Delete(ctx, getInstance(t, name)); err != nil {
		t.Fatalf("delete instance: %v", err)
	}

	res := reconcileName(t, r, name)
	if res.RequeueAfter <= 0 {
		t.Errorf("while namespace terminates Result = %+v, want requeue", res)
	}
	inst := getInstance(t, name)
	if inst.Status.Phase != fleetv1alpha1.PhaseDeleting {
		t.Errorf("phase = %q, want Deleting", inst.Status.Phase)
	}
	if !controllerutil.ContainsFinalizer(inst, instanceFinalizer) {
		t.Fatalf("finalizer released while namespace still exists")
	}
	ns := &corev1.Namespace{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "inst-" + tenant}, ns); err != nil {
		t.Fatalf("namespace should still exist while terminating: %v", err)
	}
	if ns.DeletionTimestamp == nil {
		t.Errorf("namespace not marked for deletion")
	}
	if got := drainEvents(rec); len(got) != 1 || !strings.HasPrefix(got[0], "Normal "+string(fleetv1alpha1.PhaseDeleting)) {
		t.Errorf("events on Deleting transition = %v, want one Normal Deleting", got)
	}

	// Still terminating on a second pass: still waiting.
	res = reconcileName(t, r, name)
	if res.RequeueAfter <= 0 || !controllerutil.ContainsFinalizer(getInstance(t, name), instanceFinalizer) {
		t.Errorf("second pass while terminating: Result=%+v, finalizer must stay", res)
	}

	forceFinalizeNamespace(t, "inst-"+tenant)
	res = reconcileName(t, r, name)
	if res != (ctrl.Result{}) {
		t.Errorf("after namespace gone Result = %+v, want empty", res)
	}
	err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, &fleetv1alpha1.Instance{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("instance still present after finalizer release, get err = %v", err)
	}
}

func TestDeletionReleasesFinalizerWhenNamespaceAlreadyGone(t *testing.T) {
	ctx := context.Background()
	const name, tenant = "delete-gone", "acme-delgone"
	createInstance(t, name, tenant, "6.5.0")
	r, _ := newTestReconciler(k8sClient)
	reconcileName(t, r, name)

	if err := k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "inst-" + tenant}}); err != nil {
		t.Fatalf("delete namespace: %v", err)
	}
	forceFinalizeNamespace(t, "inst-"+tenant)

	if err := k8sClient.Delete(ctx, getInstance(t, name)); err != nil {
		t.Fatalf("delete instance: %v", err)
	}
	res := reconcileName(t, r, name)
	if res != (ctrl.Result{}) {
		t.Errorf("Result = %+v, want empty once namespace is gone", res)
	}
	err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, &fleetv1alpha1.Instance{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("instance still present, get err = %v", err)
	}
}

func TestEventsEmittedOnTransitionsOnly(t *testing.T) {
	const name, tenant = "events-basic", "acme-events"
	createInstance(t, name, tenant, "6.5.0")
	r, rec := newTestReconciler(k8sClient)

	reconcileName(t, r, name)
	got := drainEvents(rec)
	if len(got) != 1 || !strings.HasPrefix(got[0], "Normal "+string(fleetv1alpha1.PhaseProvisioning)) {
		t.Errorf("events on first reconcile = %v, want one Normal Provisioning", got)
	}

	reconcileName(t, r, name)
	if got := drainEvents(rec); len(got) != 0 {
		t.Errorf("steady Provisioning emitted events: %v", got)
	}

	markDeploymentReady(t, tenant)
	reconcileName(t, r, name)
	got = drainEvents(rec)
	if len(got) != 1 || !strings.HasPrefix(got[0], "Normal "+string(fleetv1alpha1.PhaseReady)) {
		t.Errorf("events on Ready transition = %v, want one Normal Ready", got)
	}

	reconcileName(t, r, name)
	if got := drainEvents(rec); len(got) != 0 {
		t.Errorf("steady Ready emitted events: %v", got)
	}
}

func TestComputePhase(t *testing.T) {
	tests := []struct {
		name string
		in   phaseInput
		want fleetv1alpha1.Phase
	}{
		{"deleting wins over ready", phaseInput{Deleting: true, Applied: true, KubernetesReady: true}, fleetv1alpha1.PhaseDeleting},
		{"conflict fails", phaseInput{Conflict: true}, fleetv1alpha1.PhaseFailed},
		{"conflict beats ready", phaseInput{Conflict: true, Applied: true, KubernetesReady: true}, fleetv1alpha1.PhaseFailed},
		{"not applied is pending", phaseInput{}, fleetv1alpha1.PhasePending},
		{"applied and ready", phaseInput{Applied: true, KubernetesReady: true}, fleetv1alpha1.PhaseReady},
		{"applied not ready", phaseInput{Applied: true}, fleetv1alpha1.PhaseProvisioning},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := computePhase(tc.in); got != tc.want {
				t.Errorf("computePhase(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDeploymentReadiness(t *testing.T) {
	one, two := int32(1), int32(2)
	tests := []struct {
		name       string
		gen        int64
		replicas   *int32
		status     appsv1.DeploymentStatus
		wantReady  bool
		wantReason string
	}{
		{"generation not observed", 2, &one,
			appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 1, AvailableReplicas: 1}, false, reasonDeploymentProgressing},
		{"updated replicas lag", 1, &one,
			appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 0, AvailableReplicas: 1}, false, reasonDeploymentProgressing},
		{"available replicas lag", 1, &two,
			appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, AvailableReplicas: 1}, false, reasonDeploymentProgressing},
		{"fully available", 1, &one,
			appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 1, AvailableReplicas: 1}, true, reasonDeploymentAvailable},
		{"more available than desired", 1, &one,
			appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 1, AvailableReplicas: 2}, true, reasonDeploymentAvailable},
		{"nil replicas defaults to one", 1, nil,
			appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 1, AvailableReplicas: 1}, true, reasonDeploymentAvailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: tc.gen},
				Spec:       appsv1.DeploymentSpec{Replicas: tc.replicas},
				Status:     tc.status,
			}
			ready, reason, msg := deploymentReadiness(d)
			if ready != tc.wantReady || reason != tc.wantReason {
				t.Errorf("deploymentReadiness = (%v, %s, %q), want (%v, %s)", ready, reason, msg, tc.wantReady, tc.wantReason)
			}
		})
	}
}
