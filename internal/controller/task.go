package controller

import (
	"context"
	"encoding/json"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1alpha1 "github.com/Raef-Hany/fleet-operator/api/v1alpha1"
	"github.com/Raef-Hany/fleet-operator/internal/render/tfhash"
)

const (
	labelHash = "fleet.example.com/hash"

	taskContainerName = "tfrender"
	taskVolumeName    = "work"
	taskWorkDir       = "/work"
	taskEnvName       = "INSTANCE_JSON"
	taskTTLSeconds    = int32(3600)
	taskBackoffLimit  = int32(2)
	// taskScript never interpolates the Instance: the JSON reaches tfrender through the environment.
	taskScript = `printf '%s' "$INSTANCE_JSON" | tfrender -out /work && cd /work && terraform init -backend=false -input=false && terraform validate`

	reasonTaskRunning   = "TaskRunning"
	reasonTaskSucceeded = "TaskSucceeded"
	reasonTaskFailed    = "TaskFailed"
)

// taskState is the outcome of the Terraform task for the current Terraform hash.
type taskState struct {
	hash      string
	name      string
	succeeded bool
	failed    bool
	message   string
}

// taskJobName returns the deterministic Job name for a tenant and Terraform hash (ADR-9).
// hash must be a full tfhash.Hash digest.
func taskJobName(tenant, hash string) string {
	return "tf-" + tenant + "-" + hash[:8]
}

// BuildTaskJob returns the Terraform task Job for inst at the given hash. It is a pure function:
// the same inputs always produce the same Job. The Job lives in namespace, the operator
// namespace, and is controlled by inst.
func BuildTaskJob(inst *fleetv1alpha1.Instance, hash, image, namespace string) *batchv1.Job {
	tenant := inst.Spec.Tenant
	labels := map[string]string{
		tenantLabelKey: tenant,
		labelHash:      hash[:8],
		managedByKey:   managedByValue,
	}

	envJSON, err := taskInstanceJSON(inst)
	if err != nil {
		// json.Marshal of an Instance spec of strings and pointers cannot fail.
		panic(fmt.Sprintf("marshal Instance for task job: %v", err))
	}

	controller, blockOwnerDeletion := true, true
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskJobName(tenant, hash),
			Namespace: namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         fleetv1alpha1.SchemeGroupVersion.String(),
				Kind:               "Instance",
				Name:               inst.Name,
				UID:                inst.UID,
				Controller:         &controller,
				BlockOwnerDeletion: &blockOwnerDeletion,
			}},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: ptrTo(taskTTLSeconds),
			BackoffLimit:            ptrTo(taskBackoffLimit),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptrTo(true),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Volumes: []corev1.Volume{{
						Name:         taskVolumeName,
						VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
					}},
					Containers: []corev1.Container{{
						Name:    taskContainerName,
						Image:   image,
						Command: []string{"/bin/sh", "-c", taskScript},
						Env:     []corev1.EnvVar{{Name: taskEnvName, Value: string(envJSON)}},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      taskVolumeName,
							MountPath: taskWorkDir,
						}},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptrTo(false),
							RunAsNonRoot:             ptrTo(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("512Mi"),
							},
						},
					}},
				},
			},
		},
	}
}

// taskInstanceJSON is the minimal Instance envelope that tfrender decodes from stdin.
func taskInstanceJSON(inst *fleetv1alpha1.Instance) ([]byte, error) {
	return json.Marshal(&fleetv1alpha1.Instance{
		TypeMeta:   metav1.TypeMeta{APIVersion: fleetv1alpha1.SchemeGroupVersion.String(), Kind: "Instance"},
		ObjectMeta: metav1.ObjectMeta{Name: inst.Name},
		Spec:       inst.Spec,
	})
}

// reconcileTask ensures the task Job for the current hash exists and reports its outcome.
// Once the outcome for the current hash is recorded in status it is settled and the Job is not
// read again, so a finished Job being garbage collected never triggers a new run.
func (r *InstanceReconciler) reconcileTask(ctx context.Context, inst *fleetv1alpha1.Instance) (taskState, error) {
	hash := tfhash.Hash(inst.Spec)
	st := taskState{hash: hash, name: taskJobName(inst.Spec.Tenant, hash)}

	if inst.Status.TerraformHash == hash {
		if settled, ok := settledTask(inst); ok {
			settled.hash, settled.name = st.hash, st.name
			return settled, nil
		}
	}

	job := &batchv1.Job{}
	err := r.Get(ctx, client.ObjectKey{Namespace: r.TaskNamespace, Name: st.name}, job)
	if apierrors.IsNotFound(err) {
		job = BuildTaskJob(inst, hash, r.TaskImage, r.TaskNamespace)
		if err := r.Create(ctx, job); client.IgnoreAlreadyExists(err) != nil {
			return st, fmt.Errorf("create task job %s: %w", st.name, err)
		}
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("get task job %s: %w", st.name, err)
	}
	return jobOutcome(st, job), nil
}

// settledTask reads a terminal outcome for the current hash from status conditions.
func settledTask(inst *fleetv1alpha1.Instance) (taskState, bool) {
	c := meta.FindStatusCondition(inst.Status.Conditions, fleetv1alpha1.ConditionTerraformValidated)
	switch {
	case c == nil:
		return taskState{}, false
	case c.Status == metav1.ConditionTrue:
		return taskState{succeeded: true}, true
	case c.Status == metav1.ConditionFalse && c.Reason == reasonTaskFailed:
		return taskState{failed: true, message: c.Message}, true
	}
	return taskState{}, false
}

// jobOutcome maps Job conditions onto a task state. A Job with neither terminal condition is running.
func jobOutcome(st taskState, job *batchv1.Job) taskState {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			st.succeeded = true
			return st
		case batchv1.JobFailed:
			st.failed = true
			st.message = c.Message
			if st.message == "" {
				st.message = c.Reason
			}
			return st
		}
	}
	return st
}

// taskConditions maps a task state onto the TerraformRendered and TerraformValidated conditions.
func taskConditions(t taskState) []metav1.Condition {
	switch {
	case t.succeeded:
		return []metav1.Condition{
			{Type: fleetv1alpha1.ConditionTerraformRendered, Status: metav1.ConditionTrue,
				Reason: reasonTaskSucceeded, Message: "terraform rendered by task " + t.name},
			{Type: fleetv1alpha1.ConditionTerraformValidated, Status: metav1.ConditionTrue,
				Reason: reasonTaskSucceeded, Message: "terraform validated by task " + t.name},
		}
	case t.failed:
		return []metav1.Condition{
			{Type: fleetv1alpha1.ConditionTerraformRendered, Status: metav1.ConditionUnknown,
				Reason: reasonTaskFailed, Message: "task " + t.name + " failed"},
			{Type: fleetv1alpha1.ConditionTerraformValidated, Status: metav1.ConditionFalse,
				Reason: reasonTaskFailed, Message: t.message},
		}
	default:
		return []metav1.Condition{
			{Type: fleetv1alpha1.ConditionTerraformRendered, Status: metav1.ConditionUnknown,
				Reason: reasonTaskRunning, Message: "task " + t.name + " is running"},
			{Type: fleetv1alpha1.ConditionTerraformValidated, Status: metav1.ConditionFalse,
				Reason: reasonTaskRunning, Message: "task " + t.name + " is running"},
		}
	}
}

func ptrTo[T any](v T) *T {
	return &v
}
