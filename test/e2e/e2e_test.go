//go:build e2e

// Package e2e runs the operator against a real kind cluster. It is driven by
// hack/e2e.sh (or `make e2e`), which builds and loads the images and deploys the
// operator before running these tests. Unlike envtest, this exercises real
// Deployment rollouts, namespace termination and Job execution.
package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type instance struct{ name, tenant, ring, version string }

var instances = []instance{
	{"e2e-internal", "e2e-internal", "internal", "6.7.0"},
	{"e2e-trial", "e2e-trial", "trial", "6.7.0"},
	{"e2e-prod", "e2e-prod", "prod", "6.7.0"},
}

func kubectl(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("kubectl", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func manifest(i instance) string {
	return fmt.Sprintf(`apiVersion: fleet.example.com/v1alpha1
kind: Instance
metadata:
  name: %s
spec:
  tenant: %s
  version: "%s"
  ring: %s
`, i.name, i.tenant, i.version, i.ring)
}

func eventually(t *testing.T, what string, timeout time.Duration, f func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		ok, detail := f()
		if ok {
			return
		}
		last = detail
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("timed out waiting for %s; last state: %s", what, last)
}

func phase(name string) string {
	out, err := exec.Command("kubectl", "get", "instance", name, "-o", "jsonpath={.status.phase}").CombinedOutput()
	if err != nil {
		return "error: " + string(out)
	}
	return strings.TrimSpace(string(out))
}

func image(tenant string) string {
	out, _ := exec.Command("kubectl", "-n", "inst-"+tenant, "get", "deploy", "podinfo",
		"-o", "jsonpath={.spec.template.spec.containers[0].image}").CombinedOutput()
	return strings.TrimSpace(string(out))
}

func TestFleetLifecycle(t *testing.T) {
	for _, i := range instances {
		kubectl(t, manifest(i), "apply", "-f", "-")
	}

	for _, i := range instances {
		eventually(t, i.name+" Ready", 12*time.Minute, func() (bool, string) {
			p := phase(i.name)
			return p == "Ready", p
		})
		if got, want := image(i.tenant), "ghcr.io/stefanprodan/podinfo:"+i.version; got != want {
			t.Errorf("%s image = %q, want %q", i.name, got, want)
		}
	}

	// Version bump rolls out the new image and returns to Ready.
	bumped := instances[1]
	kubectl(t, "", "patch", "instance", bumped.name, "--type=merge", "-p", `{"spec":{"version":"6.7.1"}}`)
	eventually(t, "new image on "+bumped.name, 5*time.Minute, func() (bool, string) {
		img := image(bumped.tenant)
		return img == "ghcr.io/stefanprodan/podinfo:6.7.1", img
	})
	kubectl(t, "", "-n", "inst-"+bumped.tenant, "rollout", "status", "deploy/podinfo", "--timeout=300s")
	eventually(t, bumped.name+" Ready after upgrade", 5*time.Minute, func() (bool, string) {
		p := phase(bumped.name)
		return p == "Ready", p
	})

	// Delete one Instance and confirm the finalizer cleans up its namespace.
	gone := instances[2]
	kubectl(t, "", "delete", "instance", gone.name, "--wait=true", "--timeout=300s")
	eventually(t, "namespace inst-"+gone.tenant+" removed", 3*time.Minute, func() (bool, string) {
		out, err := exec.Command("kubectl", "get", "ns", "inst-"+gone.tenant, "-o", "name").CombinedOutput()
		return err != nil && strings.Contains(string(out), "NotFound"), string(out)
	})

	// The other instances are untouched.
	for _, i := range instances[:2] {
		if p := phase(i.name); p != "Ready" {
			t.Errorf("%s phase = %q after deleting a sibling, want Ready", i.name, p)
		}
	}
}
