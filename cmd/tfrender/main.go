// Command tfrender renders a fleet Instance (JSON on stdin) into Terraform JSON.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/aws/jsii-runtime-go"

	fleetv1alpha1 "github.com/Raef-Hany/fleet-operator/api/v1alpha1"
	"github.com/Raef-Hany/fleet-operator/internal/render/tf"
)

// run reads an Instance from stdin, writes cdk.tf.json into the -out directory,
// and prints the written path to stdout. It returns the process exit code.
func run(stdin io.Reader, stdout, stderr io.Writer, args []string) int {
	fs := flag.NewFlagSet("tfrender", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outDir := fs.String("out", ".", "directory to write cdk.tf.json into")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var inst fleetv1alpha1.Instance
	if err := json.NewDecoder(stdin).Decode(&inst); err != nil {
		_, _ = fmt.Fprintf(stderr, "tfrender: decoding Instance JSON: %v\n", err)
		return 1
	}

	path, err := tf.Synth(&inst, *outDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "tfrender: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, path); err != nil {
		_, _ = fmt.Fprintf(stderr, "tfrender: writing result: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	code := func() int {
		defer jsii.Close()
		return run(os.Stdin, os.Stdout, os.Stderr, os.Args[1:])
	}()
	os.Exit(code)
}
