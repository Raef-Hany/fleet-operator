package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1alpha1 "github.com/Raef-Hany/fleet-operator/api/v1alpha1"
	"github.com/Raef-Hany/fleet-operator/internal/render/tfhash"
)

const (
	testTaskImage     = "fleet-operator/tfrender:test"
	testTaskNamespace = "fleet-operator-system"
	wantTaskScript    = `printf '%s' "$INSTANCE_JSON" | tfrender -out /work && cd /work && terraform init -backend=false -input=false && terraform validate`
)

func sampleTaskInstance(tenant string) *fleetv1alpha1.Instance {
	return &fleetv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "sample", UID: types.UID("uid-1")},
		Spec: fleetv1alpha1.InstanceSpec{
			Tenant:  tenant,
			Version: "6.5.0",
			Size:    fleetv1alpha1.SizeSmall,
			Region:  "europe-west1",
			Ring:    fleetv1alpha1.RingTrial,
			Connectivity: &fleetv1alpha1.Connectivity{
				Mode:            fleetv1alpha1.ConnectivityPSC,
				AllowedProjects: []string{"consumer-a"},
			},
		},
	}
}

func envValue(t *testing.T, c corev1.Container, name string) string {
	t.Helper()
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value
		}
	}
	t.Fatalf("container %s has no env %s", c.Name, name)
	return ""
}

func TestBuildTaskJobIsDeterministic(t *testing.T) {
	inst := sampleTaskInstance("acme")
	h := tfhash.Hash(inst.Spec)
	a := BuildTaskJob(inst, h, testTaskImage, "ns-ops")
	b := BuildTaskJob(inst, h, testTaskImage, "ns-ops")
	if !apiequality.Semantic.DeepEqual(a, b) {
		t.Fatalf("same input produced different Jobs:\n%+v\n%+v", a, b)
	}
}

func TestBuildTaskJobNameLabelsAndOwner(t *testing.T) {
	inst := sampleTaskInstance("acme")
	h := tfhash.Hash(inst.Spec)
	job := BuildTaskJob(inst, h, testTaskImage, "ns-ops")

	if want := "tf-acme-" + h[:8]; job.Name != want {
		t.Errorf("job name = %q, want %q", job.Name, want)
	}
	if job.Namespace != "ns-ops" {
		t.Errorf("job namespace = %q, want the task namespace ns-ops", job.Namespace)
	}
	wantLabels := map[string]string{
		tenantLabelKey: "acme",
		labelHash:      h[:8],
		managedByKey:   managedByValue,
	}
	for k, v := range wantLabels {
		if got := job.Labels[k]; got != v {
			t.Errorf("job label %s = %q, want %q", k, got, v)
		}
		if got := job.Spec.Template.Labels[k]; got != v {
			t.Errorf("pod template label %s = %q, want %q", k, got, v)
		}
	}
	owner := metav1.GetControllerOf(job)
	if owner == nil || !*owner.Controller || owner.Kind != "Instance" ||
		owner.APIVersion != fleetv1alpha1.GroupVersion.String() || owner.Name != "sample" || owner.UID != inst.UID {
		t.Errorf("controller owner = %+v, want controller Instance sample uid-1", owner)
	}
}

func TestBuildTaskJobPodSpec(t *testing.T) {
	inst := sampleTaskInstance("acme")
	job := BuildTaskJob(inst, tfhash.Hash(inst.Spec), testTaskImage, "ns-ops")
	spec := job.Spec

	if spec.TTLSecondsAfterFinished == nil || *spec.TTLSecondsAfterFinished != 3600 {
		t.Errorf("ttlSecondsAfterFinished = %v, want 3600", spec.TTLSecondsAfterFinished)
	}
	if spec.BackoffLimit == nil || *spec.BackoffLimit != 2 {
		t.Errorf("backoffLimit = %v, want 2", spec.BackoffLimit)
	}
	pod := spec.Template.Spec
	if pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %q, want Never", pod.RestartPolicy)
	}
	if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot {
		t.Errorf("pod securityContext.runAsNonRoot must be true, got %+v", pod.SecurityContext)
	}
	if len(pod.Volumes) != 1 || pod.Volumes[0].EmptyDir == nil {
		t.Fatalf("volumes = %+v, want one emptyDir", pod.Volumes)
	}

	if len(pod.Containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(pod.Containers))
	}
	c := pod.Containers[0]
	if c.Name != "tfrender" || c.Image != testTaskImage {
		t.Errorf("container = %s/%s, want tfrender/%s", c.Name, c.Image, testTaskImage)
	}
	if strings.Join(c.Command, "|") != "/bin/sh|-c|"+wantTaskScript {
		t.Errorf("command = %q, want sh -c with script %q", c.Command, wantTaskScript)
	}
	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].MountPath != "/work" || c.VolumeMounts[0].Name != pod.Volumes[0].Name {
		t.Errorf("volumeMounts = %+v, want the emptyDir mounted at /work", c.VolumeMounts)
	}
	sc := c.SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Errorf("allowPrivilegeEscalation must be false, got %+v", sc)
	}
	if sc == nil || sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("capabilities.drop must be [ALL], got %+v", sc)
	}
	req, lim := c.Resources.Requests, c.Resources.Limits
	for _, r := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		if req.Name(r, "").IsZero() || lim.Name(r, "").IsZero() {
			t.Errorf("%s request and limit must both be set", r)
		}
		if req.Name(r, "").Cmp(*lim.Name(r, "")) > 0 {
			t.Errorf("%s request exceeds limit", r)
		}
	}
}

func TestBuildTaskJobEnvRoundTripsSpec(t *testing.T) {
	inst := sampleTaskInstance("acme")
	job := BuildTaskJob(inst, tfhash.Hash(inst.Spec), testTaskImage, "ns-ops")
	if len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(job.Spec.Template.Spec.Containers))
	}
	raw := envValue(t, job.Spec.Template.Spec.Containers[0], "INSTANCE_JSON")

	var got fleetv1alpha1.Instance
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("INSTANCE_JSON is not valid Instance JSON: %v", err)
	}
	if !apiequality.Semantic.DeepEqual(got.Spec, inst.Spec) {
		t.Errorf("round-tripped spec = %+v, want %+v", got.Spec, inst.Spec)
	}
	if got.APIVersion != "fleet.example.com/v1alpha1" || got.Kind != "Instance" || got.Name != "sample" {
		t.Errorf("envelope = %s %s/%s, want fleet.example.com/v1alpha1 Instance sample", got.APIVersion, got.Kind, got.Name)
	}
}

func TestBuildTaskJobDifferentHashDifferentName(t *testing.T) {
	inst := sampleTaskInstance("acme")
	other := sampleTaskInstance("acme")
	other.Spec.Region = "us-east1"
	a := BuildTaskJob(inst, tfhash.Hash(inst.Spec), testTaskImage, "ns-ops")
	b := BuildTaskJob(other, tfhash.Hash(other.Spec), testTaskImage, "ns-ops")
	if a.Name == b.Name {
		t.Fatalf("different hashes produced the same job name %q", a.Name)
	}
}

func TestBuildTaskJobLongTenantIsValid(t *testing.T) {
	tenant := strings.Repeat("a", 40)
	inst := sampleTaskInstance(tenant)
	job := BuildTaskJob(inst, tfhash.Hash(inst.Spec), testTaskImage, "ns-ops")
	if len(job.Name) > 63 {
		t.Errorf("job name %q is %d characters, want <= 63", job.Name, len(job.Name))
	}
	if errs := validation.IsDNS1123Label(job.Name); len(errs) != 0 {
		t.Errorf("job name %q is not a DNS label: %v", job.Name, errs)
	}
	if errs := validation.IsValidLabelValue(tenant); len(errs) != 0 {
		t.Errorf("tenant label value rejected: %v", errs)
	}
}

// --- envtest-backed tests of the task flow ---

func taskJobFor(t *testing.T, name string) *batchv1.Job {
	t.Helper()
	inst := getInstance(t, name)
	job := &batchv1.Job{}
	key := client.ObjectKey{Namespace: testTaskNamespace, Name: taskJobName(inst.Spec.Tenant, tfhash.Hash(inst.Spec))}
	if err := k8sClient.Get(context.Background(), key, job); err != nil {
		t.Fatalf("get task job %s: %v", key, err)
	}
	return job
}

func taskJobsForTenant(t *testing.T, tenant string) []batchv1.Job {
	t.Helper()
	list := &batchv1.JobList{}
	if err := k8sClient.List(context.Background(), list, client.InNamespace(testTaskNamespace),
		client.MatchingLabels{tenantLabelKey: tenant}); err != nil {
		t.Fatalf("list task jobs: %v", err)
	}
	return list.Items
}

// completeTaskJob plays the role of the Job controller: it sets the status of a successful run.
func completeTaskJob(t *testing.T, name string) {
	t.Helper()
	job := taskJobFor(t, name)
	now := metav1.NewTime(time.Now())
	job.Status = batchv1.JobStatus{
		StartTime:      &now,
		CompletionTime: &now,
		Succeeded:      1,
		Conditions: []batchv1.JobCondition{
			{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now, Reason: "CompletionsReached"},
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now, Reason: "CompletionsReached"},
		},
	}
	if err := k8sClient.Status().Update(context.Background(), job); err != nil {
		t.Fatalf("complete task job: %v", err)
	}
}

// failTaskJob plays the role of the Job controller for a run that exhausted its backoff.
func failTaskJob(t *testing.T, name, message string) {
	t.Helper()
	job := taskJobFor(t, name)
	now := metav1.NewTime(time.Now())
	job.Status = batchv1.JobStatus{
		StartTime: &now,
		Failed:    3,
		Conditions: []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, LastTransitionTime: now, Reason: "BackoffLimitExceeded", Message: message},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, LastTransitionTime: now, Reason: "BackoffLimitExceeded", Message: message},
		},
	}
	if err := k8sClient.Status().Update(context.Background(), job); err != nil {
		t.Fatalf("fail task job: %v", err)
	}
}

func TestTaskJobCreatedOncePerHash(t *testing.T) {
	const name, tenant = "task-once", "acme-once"
	createInstance(t, name, tenant, "6.5.0")
	r, _ := newTestReconciler(k8sClient)
	for i := 0; i < 3; i++ {
		reconcileName(t, r, name)
	}

	jobs := taskJobsForTenant(t, tenant)
	inst := getInstance(t, name)
	wantName := taskJobName(tenant, tfhash.Hash(inst.Spec))
	if len(jobs) != 1 || jobs[0].Name != wantName {
		t.Fatalf("task jobs = %d (%v), want exactly one named %s", len(jobs), jobNames(jobs), wantName)
	}
	if inst.Status.TerraformHash != tfhash.Hash(inst.Spec) || inst.Status.LastTaskName != wantName {
		t.Errorf("status hash/task = %q/%q, want %q/%q", inst.Status.TerraformHash, inst.Status.LastTaskName, tfhash.Hash(inst.Spec), wantName)
	}
	assertCondition(t, inst, fleetv1alpha1.ConditionTerraformValidated, metav1.ConditionFalse, reasonTaskRunning)
	assertCondition(t, inst, fleetv1alpha1.ConditionTerraformRendered, metav1.ConditionUnknown, reasonTaskRunning)
}

func jobNames(jobs []batchv1.Job) []string {
	out := make([]string, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.Name)
	}
	return out
}

func TestTaskSuccessMakesInstanceReady(t *testing.T) {
	const name, tenant = "task-success", "acme-tasksuccess"
	createInstance(t, name, tenant, "6.5.0")
	r, rec := newTestReconciler(k8sClient)
	reconcileName(t, r, name)
	markDeploymentReady(t, tenant)

	// Kubernetes is ready but Terraform has not validated yet: not Ready, still waiting.
	res := reconcileName(t, r, name)
	inst := getInstance(t, name)
	if inst.Status.Phase == fleetv1alpha1.PhaseReady {
		t.Fatalf("phase Ready before the task Job completed")
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("waiting on task job should requeue, got %+v", res)
	}
	drainEvents(rec)

	completeTaskJob(t, name)
	res = reconcileName(t, r, name)
	if res != (ctrl.Result{}) {
		t.Errorf("Ready Result = %+v, want empty", res)
	}
	inst = getInstance(t, name)
	if inst.Status.Phase != fleetv1alpha1.PhaseReady {
		t.Errorf("phase = %q, want Ready", inst.Status.Phase)
	}
	assertCondition(t, inst, fleetv1alpha1.ConditionTerraformRendered, metav1.ConditionTrue, reasonTaskSucceeded)
	assertCondition(t, inst, fleetv1alpha1.ConditionTerraformValidated, metav1.ConditionTrue, reasonTaskSucceeded)
	assertCondition(t, inst, fleetv1alpha1.ConditionKubernetesReady, metav1.ConditionTrue, reasonDeploymentAvailable)
	if got := drainEvents(rec); len(got) != 1 || !strings.HasPrefix(got[0], "Normal Ready") {
		t.Errorf("events on Ready = %v, want one Normal Ready", got)
	}
}

func TestTaskFailureIsTerminal(t *testing.T) {
	const name, tenant = "task-failure", "acme-taskfail"
	createInstance(t, name, tenant, "6.5.0")
	r, rec := newTestReconciler(k8sClient)
	reconcileName(t, r, name)
	drainEvents(rec)

	failTaskJob(t, name, "terraform validate: Unsupported argument")
	res := reconcileName(t, r, name)
	if res != (ctrl.Result{}) {
		t.Errorf("failed task Result = %+v, want no requeue", res)
	}
	inst := getInstance(t, name)
	if inst.Status.Phase != fleetv1alpha1.PhaseFailed {
		t.Errorf("phase = %q, want Failed", inst.Status.Phase)
	}
	assertCondition(t, inst, fleetv1alpha1.ConditionTerraformValidated, metav1.ConditionFalse, reasonTaskFailed)
	if c := findCondition(inst, fleetv1alpha1.ConditionTerraformValidated); c == nil || !strings.Contains(c.Message, "Unsupported argument") {
		t.Errorf("TerraformValidated message = %v, want the job failure message", c)
	}
	if got := drainEvents(rec); len(got) != 1 || !strings.HasPrefix(got[0], "Warning "+reasonTaskFailed) {
		t.Errorf("events on failure = %v, want one Warning TaskFailed", got)
	}

	// Terminal: further reconciles neither requeue nor emit events.
	res = reconcileName(t, r, name)
	if res != (ctrl.Result{}) {
		t.Errorf("second failed Result = %+v, want no requeue", res)
	}
	if got := drainEvents(rec); len(got) != 0 {
		t.Errorf("events on steady failure = %v", got)
	}
}

func findCondition(inst *fleetv1alpha1.Instance, typ string) *metav1.Condition {
	for i := range inst.Status.Conditions {
		if inst.Status.Conditions[i].Type == typ {
			return &inst.Status.Conditions[i]
		}
	}
	return nil
}

func TestHashChangeCreatesNewJobAndOldIsNeverRecreated(t *testing.T) {
	ctx := context.Background()
	const name, tenant = "task-region", "acme-region"
	createInstance(t, name, tenant, "6.5.0")
	r, _ := newTestReconciler(k8sClient)
	reconcileName(t, r, name)
	oldName := taskJobName(tenant, tfhash.Hash(getInstance(t, name).Spec))

	inst := getInstance(t, name)
	inst.Spec.Region = "europe-west1"
	if err := k8sClient.Update(ctx, inst); err != nil {
		t.Fatalf("change region: %v", err)
	}
	reconcileName(t, r, name)

	inst = getInstance(t, name)
	newName := taskJobName(tenant, tfhash.Hash(inst.Spec))
	if newName == oldName {
		t.Fatalf("region change did not change the task hash")
	}
	if inst.Status.LastTaskName != newName || inst.Status.TerraformHash != tfhash.Hash(inst.Spec) {
		t.Errorf("status task/hash = %q/%q, want %q/%q", inst.Status.LastTaskName, inst.Status.TerraformHash, newName, tfhash.Hash(inst.Spec))
	}
	if got := jobNamesOf(t, tenant); len(got) != 2 || !contains(got, oldName) || !contains(got, newName) {
		t.Fatalf("jobs after hash change = %v, want old %s and new %s", got, oldName, newName)
	}

	// Old job is left alone (and not recreated) once it is gone.
	oldJob := &batchv1.Job{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: testTaskNamespace, Name: oldName}, oldJob); err != nil {
		t.Fatalf("old job should remain: %v", err)
	}
	reconcileName(t, r, name)
	if got := jobNamesOf(t, tenant); len(got) != 2 {
		t.Errorf("jobs after another reconcile = %v, want 2", got)
	}
	// Background propagation: envtest runs no garbage collector, so orphan propagation would
	// leave the Job terminating forever with an orphan finalizer.
	if err := k8sClient.Delete(ctx, oldJob, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
		t.Fatalf("delete old job: %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: testTaskNamespace, Name: oldName}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old job still present after delete, get err = %v", err)
	}
	reconcileName(t, r, name)
	got := jobNamesOf(t, tenant)
	if contains(got, oldName) {
		t.Errorf("old job %s was recreated: %v", oldName, got)
	}
	if len(got) != 1 || got[0] != newName {
		t.Errorf("jobs = %v, want only %s", got, newName)
	}
}

func jobNamesOf(t *testing.T, tenant string) []string {
	t.Helper()
	return jobNames(taskJobsForTenant(t, tenant))
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestVersionAndSizeChangeDoNotCreateJob(t *testing.T) {
	ctx := context.Background()
	const name, tenant = "task-version", "acme-taskver"
	createInstance(t, name, tenant, "6.5.0")
	r, _ := newTestReconciler(k8sClient)
	reconcileName(t, r, name)
	markDeploymentReady(t, tenant)
	completeTaskJob(t, name)
	reconcileName(t, r, name)

	inst := getInstance(t, name)
	inst.Spec.Version = "6.5.1"
	if err := k8sClient.Update(ctx, inst); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	reconcileName(t, r, name)

	inst = getInstance(t, name)
	inst.Spec.Size = fleetv1alpha1.SizeLarge
	if err := k8sClient.Update(ctx, inst); err != nil {
		t.Fatalf("change size: %v", err)
	}
	reconcileName(t, r, name)

	if got := jobNamesOf(t, tenant); len(got) != 1 {
		t.Errorf("version/size change created jobs %v, want still exactly 1", got)
	}
	assertCondition(t, getInstance(t, name), fleetv1alpha1.ConditionTerraformValidated, metav1.ConditionTrue, reasonTaskSucceeded)
}

// The prebuilt Google provider bundle is huge; with a 512Mi limit the Node child
// process behind jsii is OOM-killed (exit 137) in a real cluster. envtest cannot
// catch this, the kind e2e run did.
func TestBuildTaskJobMemoryLimitFitsJsiiGoogleProvider(t *testing.T) {
	inst := sampleTaskInstance("acme")
	job := BuildTaskJob(inst, tfhash.Hash(inst.Spec), testTaskImage, "ns-ops")
	lim := job.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory]
	min := resource.MustParse("2Gi")
	if lim.Cmp(min) < 0 {
		t.Errorf("memory limit = %s, want at least %s", lim.String(), min.String())
	}
}
