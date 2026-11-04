/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	fleetv1alpha1 "github.com/Raef-Hany/fleet-operator/api/v1alpha1"
	renderk8s "github.com/Raef-Hany/fleet-operator/internal/render/k8s"
)

const (
	instanceFinalizer = "fleet.example.com/cleanup"

	fieldOwner          = "fleet-operator"
	managedByKey        = "app.kubernetes.io/managed-by"
	managedByValue      = "fleet-operator"
	tenantLabelKey      = "fleet.example.com/tenant"
	deploymentName      = "podinfo"
	provisioningRequeue = 10 * time.Second
	deletionRequeue     = 5 * time.Second

	reasonNamespaceConflict     = "NamespaceConflict"
	reasonDeploymentAvailable   = "DeploymentAvailable"
	reasonDeploymentProgressing = "DeploymentProgressing"
	reasonDeleting              = "Deleting"
)

// phaseInput is everything computePhase needs to choose the Instance phase.
// Applied is false only when no Kubernetes objects were applied in this pass.
type phaseInput struct {
	Deleting           bool
	Conflict           bool
	Applied            bool
	KubernetesReady    bool
	TerraformFailed    bool
	TerraformValidated bool
}

// InstanceReconciler reconciles a Instance object. Recorder must be non-nil.
type InstanceReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	Recorder      events.EventRecorder
	TaskNamespace string
	TaskImage     string
}

// +kubebuilder:rbac:groups=fleet.example.com,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=fleet.example.com,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=fleet.example.com,resources=instances/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts;resourcequotas;services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile moves the Kubernetes objects of an Instance towards the rendered desired state
// and reports the outcome in status. Terraform steps are not yet implemented.
func (r *InstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	inst := &fleetv1alpha1.Instance{}
	if err := r.Get(ctx, req.NamespacedName, inst); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !inst.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, inst)
	}

	if !controllerutil.ContainsFinalizer(inst, instanceFinalizer) {
		base := inst.DeepCopy()
		controllerutil.AddFinalizer(inst, instanceFinalizer)
		if err := r.Patch(ctx, inst, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
	}

	conflict, err := r.namespaceConflict(ctx, inst)
	if err != nil {
		return ctrl.Result{}, err
	}

	in := phaseInput{Conflict: conflict != ""}
	kube := metav1.Condition{Type: fleetv1alpha1.ConditionKubernetesReady, Status: metav1.ConditionFalse}
	if in.Conflict {
		log.Info("refusing to adopt pre-existing namespace", "namespace", namespaceName(inst))
		kube.Reason = reasonNamespaceConflict
		kube.Message = conflict
	} else {
		if err := r.applyDesired(ctx, inst); err != nil {
			return ctrl.Result{}, err
		}
		in.Applied = true

		dep := &appsv1.Deployment{}
		err := r.Get(ctx, client.ObjectKey{Namespace: namespaceName(inst), Name: deploymentName}, dep)
		switch {
		case apierrors.IsNotFound(err):
			kube.Reason = reasonDeploymentProgressing
			kube.Message = "deployment not observed yet"
		case err != nil:
			return ctrl.Result{}, fmt.Errorf("get deployment: %w", err)
		default:
			ready, reason, msg := deploymentReadiness(dep)
			in.KubernetesReady = ready
			kube.Reason = reason
			kube.Message = msg
		}
		if in.KubernetesReady {
			kube.Status = metav1.ConditionTrue
		}
	}

	task, err := r.reconcileTask(ctx, inst)
	if err != nil {
		return ctrl.Result{}, err
	}
	in.TerraformFailed = task.failed
	in.TerraformValidated = task.succeeded

	phase := computePhase(in)
	su := statusUpdate{
		phase:         phase,
		conditions:    append([]metav1.Condition{kube}, taskConditions(task)...),
		terraformHash: task.hash,
		lastTaskName:  task.name,
	}
	switch {
	case in.Conflict:
		su.failReason, su.failMessage = reasonNamespaceConflict, conflict
	case task.failed:
		su.failReason, su.failMessage = reasonTaskFailed, task.message
	}
	if err := r.setStatus(ctx, inst, su); err != nil {
		return ctrl.Result{}, err
	}
	if phase == fleetv1alpha1.PhaseProvisioning {
		return ctrl.Result{RequeueAfter: provisioningRequeue}, nil
	}
	return ctrl.Result{}, nil
}

// reconcileDelete deletes the owned namespace, waits for it to disappear, then releases the finalizer.
// A namespace this Instance does not own is never deleted.
func (r *InstanceReconciler) reconcileDelete(ctx context.Context, inst *fleetv1alpha1.Instance) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(inst, instanceFinalizer) {
		return ctrl.Result{}, nil
	}

	kube := metav1.Condition{
		Type:    fleetv1alpha1.ConditionKubernetesReady,
		Status:  metav1.ConditionFalse,
		Reason:  reasonDeleting,
		Message: "instance is being deleted",
	}
	phase := computePhase(phaseInput{Deleting: true})

	live := &corev1.Namespace{}
	err := r.Get(ctx, client.ObjectKey{Name: namespaceName(inst)}, live)
	switch {
	case apierrors.IsNotFound(err):
		// Nothing left to clean up.
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("get namespace: %w", err)
	case ownedBy(live, inst):
		if live.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, live); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, fmt.Errorf("delete namespace: %w", err)
			}
		}
		if err := r.setStatus(ctx, inst, statusUpdate{phase: phase, conditions: []metav1.Condition{kube}}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: deletionRequeue}, nil
	}

	if err := r.setStatus(ctx, inst, statusUpdate{phase: phase, conditions: []metav1.Condition{kube}}); err != nil {
		return ctrl.Result{}, err
	}
	base := inst.DeepCopy()
	controllerutil.RemoveFinalizer(inst, instanceFinalizer)
	if err := r.Patch(ctx, inst, client.MergeFrom(base)); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// namespaceConflict returns a non-empty explanation when the instance namespace exists but
// is not owned by this tenant's fleet-operator objects.
func (r *InstanceReconciler) namespaceConflict(ctx context.Context, inst *fleetv1alpha1.Instance) (string, error) {
	name := namespaceName(inst)
	ns := &corev1.Namespace{}
	err := r.Get(ctx, client.ObjectKey{Name: name}, ns)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get namespace: %w", err)
	}
	if ownedBy(ns, inst) {
		return "", nil
	}
	return fmt.Sprintf("namespace %s already exists and is not managed by fleet-operator for tenant %s",
		name, inst.Spec.Tenant), nil
}

// applyDesired server-side applies every rendered object, in render order, owned by inst.
func (r *InstanceReconciler) applyDesired(ctx context.Context, inst *fleetv1alpha1.Instance) error {
	for _, obj := range renderk8s.Render(inst) {
		if err := controllerutil.SetControllerReference(inst, obj, r.Scheme); err != nil {
			return fmt.Errorf("set owner on %s %s: %w", kindOf(obj), obj.GetName(), err)
		}
		dirty, err := r.needsApply(ctx, obj)
		if err != nil {
			return err
		}
		if !dirty {
			continue
		}
		//nolint:staticcheck // client.Apply patch type is deprecated in controller-runtime v0.25 in favour of Client.Apply(); migrating is tracked as a follow-up
		if err := r.Patch(ctx, obj, client.Apply, client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
			return fmt.Errorf("apply %s %s: %w", kindOf(obj), obj.GetName(), err)
		}
	}
	return nil
}

// needsApply reports whether obj must be applied for real. A server-side dry run is used to
// compute what the apply would store. The dry run is compared with the live object, ignoring
// managedFields and resourceVersion, because a dry run does not bump resourceVersion.
func (r *InstanceReconciler) needsApply(ctx context.Context, obj client.Object) (bool, error) {
	dry := obj.DeepCopyObject().(client.Object)
	//nolint:staticcheck // see above
	if err := r.Patch(ctx, dry, client.Apply, client.FieldOwner(fieldOwner), client.ForceOwnership, client.DryRunAll); err != nil {
		return false, fmt.Errorf("dry-run apply %s %s: %w", kindOf(obj), obj.GetName(), err)
	}
	live := reflect.New(reflect.TypeOf(obj).Elem()).Interface().(client.Object)
	if err := r.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("get %s %s: %w", kindOf(obj), obj.GetName(), err)
	}
	return !sameContent(dry, live), nil
}

// statusUpdate is the desired status for one reconcile pass.
type statusUpdate struct {
	phase      fleetv1alpha1.Phase
	conditions []metav1.Condition // KubernetesReady and, when reconciled, the Terraform conditions
	// terraformHash and lastTaskName are left unchanged when empty (deletion path).
	terraformHash string
	lastTaskName  string
	// failReason and failMessage describe the cause for the Warning event emitted when phase becomes Failed.
	failReason  string
	failMessage string
}

// setStatus writes the status only when it changes, and emits one event per phase transition.
func (r *InstanceReconciler) setStatus(ctx context.Context, inst *fleetv1alpha1.Instance, su statusUpdate) error {
	before := inst.Status.DeepCopy()

	inst.Status.Phase = su.phase
	inst.Status.ObservedGeneration = inst.Generation
	if su.terraformHash != "" {
		inst.Status.TerraformHash = su.terraformHash
	}
	if su.lastTaskName != "" {
		inst.Status.LastTaskName = su.lastTaskName
	}
	for _, c := range su.conditions {
		c.ObservedGeneration = inst.Generation
		meta.SetStatusCondition(&inst.Status.Conditions, c)
	}
	meta.SetStatusCondition(&inst.Status.Conditions, readyCondition(su.phase, inst.Generation))

	if apiequality.Semantic.DeepEqual(*before, inst.Status) {
		return nil
	}
	if err := r.Status().Update(ctx, inst); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	if before.Phase != su.phase {
		r.emitPhaseEvent(inst, su)
	}
	return nil
}

func (r *InstanceReconciler) emitPhaseEvent(inst *fleetv1alpha1.Instance, su statusUpdate) {
	if su.phase == fleetv1alpha1.PhaseFailed {
		r.Recorder.Eventf(inst, nil, corev1.EventTypeWarning, su.failReason, "PhaseChange", "%s", su.failMessage)
		return
	}
	r.Recorder.Eventf(inst, nil, corev1.EventTypeNormal, string(su.phase), "PhaseChange",
		"instance %s is now %s", inst.Name, su.phase)
}

// computePhase chooses the phase. Precedence: Deleting, Failed (conflict or Terraform failure),
// Pending (not yet applied), Ready (Kubernetes and Terraform both ready), then Provisioning.
func computePhase(in phaseInput) fleetv1alpha1.Phase {
	switch {
	case in.Deleting:
		return fleetv1alpha1.PhaseDeleting
	case in.Conflict, in.TerraformFailed:
		return fleetv1alpha1.PhaseFailed
	case !in.Applied:
		return fleetv1alpha1.PhasePending
	case in.KubernetesReady && in.TerraformValidated:
		return fleetv1alpha1.PhaseReady
	default:
		return fleetv1alpha1.PhaseProvisioning
	}
}

// deploymentReadiness reports whether the podinfo Deployment has rolled out the current spec.
func deploymentReadiness(d *appsv1.Deployment) (bool, string, string) {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	s := d.Status
	switch {
	case s.ObservedGeneration < d.Generation:
		return false, reasonDeploymentProgressing, fmt.Sprintf("generation %d not yet observed", d.Generation)
	case s.UpdatedReplicas != want:
		return false, reasonDeploymentProgressing, fmt.Sprintf("%d of %d replicas updated", s.UpdatedReplicas, want)
	case s.AvailableReplicas < want:
		return false, reasonDeploymentProgressing, fmt.Sprintf("%d of %d replicas available", s.AvailableReplicas, want)
	}
	return true, reasonDeploymentAvailable, fmt.Sprintf("%d of %d replicas available", s.AvailableReplicas, want)
}

func readyCondition(phase fleetv1alpha1.Phase, generation int64) metav1.Condition {
	c := metav1.Condition{
		Type:               fleetv1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             string(phase),
		Message:            fmt.Sprintf("instance is %s", phase),
		ObservedGeneration: generation,
	}
	if phase == fleetv1alpha1.PhaseReady {
		c.Status = metav1.ConditionTrue
	}
	return c
}

func namespaceName(inst *fleetv1alpha1.Instance) string {
	return "inst-" + inst.Spec.Tenant
}

// ownedBy reports whether ns carries the labels fleet-operator stamps on the namespace of a tenant.
func ownedBy(ns *corev1.Namespace, inst *fleetv1alpha1.Instance) bool {
	return ns.Labels[managedByKey] == managedByValue && ns.Labels[tenantLabelKey] == inst.Spec.Tenant
}

func kindOf(obj client.Object) string {
	return obj.GetObjectKind().GroupVersionKind().Kind
}

// sameContent compares two objects ignoring managedFields, resourceVersion and TypeMeta.
func sameContent(a, b client.Object) bool {
	a = a.DeepCopyObject().(client.Object)
	b = b.DeepCopyObject().(client.Object)
	for _, o := range []client.Object{a, b} {
		o.SetManagedFields(nil)
		o.SetResourceVersion("")
		o.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{})
	}
	return apiequality.Semantic.DeepEqual(a, b)
}

// SetupWithManager sets up the controller with the Manager.
func (r *InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&fleetv1alpha1.Instance{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.ResourceQuota{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.Namespace{}).
		Named("instance").
		Complete(r)
}
