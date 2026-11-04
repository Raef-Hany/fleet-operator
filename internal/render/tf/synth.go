package tf

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aws/jsii-runtime-go"
	"github.com/hashicorp/terraform-cdk-go/cdktf"

	fleetv1alpha1 "github.com/Raef-Hany/fleet-operator/api/v1alpha1"
)

const outFileName = "cdk.tf.json"

// Synth renders inst into <outDir>/cdk.tf.json and returns that path. The
// directory is created if missing. CDKTF writes into a private temporary
// workspace that is removed before returning, so only the flat file remains.
//
// Synth starts the jsii runtime on first use. Callers own the process-wide
// jsii.Close.
func Synth(inst *fleetv1alpha1.Instance, outDir string) (path string, err error) {
	if inst == nil {
		return "", errors.New("tf: instance is nil")
	}
	if inst.Spec.Tenant == "" {
		return "", errors.New("tf: tenant is required")
	}
	if outDir == "" {
		return "", errors.New("tf: output directory is required")
	}

	work, err := os.MkdirTemp("", "tfsynth-")
	if err != nil {
		return "", fmt.Errorf("tf: creating synth workspace: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(work); rmErr != nil && err == nil {
			err = fmt.Errorf("tf: removing synth workspace: %w", rmErr)
		}
	}()

	app := cdktf.NewApp(&cdktf.AppConfig{Outdir: jsii.String(work)})
	buildStack(app, inst.Spec)
	app.Synth()

	data, err := os.ReadFile(filepath.Join(work, "stacks", stackName, outFileName))
	if err != nil {
		return "", fmt.Errorf("tf: reading synthesized stack: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("tf: creating output directory: %w", err)
	}
	path = filepath.Join(outDir, outFileName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("tf: writing %s: %w", path, err)
	}
	return path, nil
}
