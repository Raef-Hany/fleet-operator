package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/jsii-runtime-go"
)

func TestMain(m *testing.M) {
	code := m.Run()
	jsii.Close()
	os.Exit(code)
}

const validInstance = `{
  "apiVersion": "fleet.example.com/v1alpha1",
  "kind": "Instance",
  "metadata": {"name": "acme"},
  "spec": {
    "tenant": "acme",
    "version": "1.2.3",
    "size": "small",
    "region": "us-central1",
    "ring": "prod",
    "connectivity": {"mode": "none"}
  }
}`

func TestRun_WritesCDKFileAndPrintsPath(t *testing.T) {
	out := filepath.Join(t.TempDir(), "missing", "dir")
	var stdout, stderr bytes.Buffer

	code := run(strings.NewReader(validInstance), &stdout, &stderr, []string{"-out", out})

	if code != 0 {
		t.Fatalf("run() = %d, want 0; stderr=%q", code, stderr.String())
	}
	want := filepath.Join(out, "cdk.tf.json")
	if got := strings.TrimSpace(stdout.String()); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("expected output file: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		t.Fatalf("output is not a JSON object: %.40q", data)
	}
}

func TestRun_BadJSONFailsWithMessage(t *testing.T) {
	out := t.TempDir()
	var stdout, stderr bytes.Buffer

	code := run(strings.NewReader("{not json"), &stdout, &stderr, []string{"-out", out})

	if code == 0 {
		t.Fatalf("run() = 0 on bad JSON, want nonzero")
	}
	if stderr.Len() == 0 {
		t.Fatalf("stderr empty on bad JSON, want a message")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q on failure, want empty", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(out, "cdk.tf.json")); err == nil {
		t.Fatalf("cdk.tf.json written despite bad input")
	}
}

func TestRun_InvalidInstanceFailsWithMessage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	in := `{"spec": {"tenant": "", "version": "1.2.3"}}`

	code := run(strings.NewReader(in), &stdout, &stderr, []string{"-out", t.TempDir()})

	if code == 0 {
		t.Fatalf("run() = 0 for empty tenant, want nonzero")
	}
	if stderr.Len() == 0 {
		t.Fatalf("stderr empty for empty tenant, want a message")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q on failure, want empty", stdout.String())
	}
}

func TestRun_UnknownFlagIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run(strings.NewReader(validInstance), &stdout, &stderr, []string{"-bogus"})

	if code != 2 {
		t.Fatalf("run() = %d for unknown flag, want 2", code)
	}
	if stderr.Len() == 0 {
		t.Fatalf("stderr empty for unknown flag, want usage output")
	}
}
