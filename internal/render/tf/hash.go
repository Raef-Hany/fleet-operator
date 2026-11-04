// Package tf renders fleet Instances into Terraform JSON via CDKTF.
package tf

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	fleetv1alpha1 "github.com/Raef-Hany/fleet-operator/api/v1alpha1"
)

// Hash returns the sha256 hex digest of the Terraform-relevant inputs of spec.
// Version and size are deliberately excluded: they do not change the rendered
// Terraform. Defaults are applied before hashing so that an omitted field and
// its explicit default produce the same digest.
func Hash(spec fleetv1alpha1.InstanceSpec) string {
	mode := fleetv1alpha1.ConnectivityNone
	var projects []string
	if spec.Connectivity != nil {
		mode = connectivityMode(spec.Connectivity.Mode)
		projects = append(projects, spec.Connectivity.AllowedProjects...)
	}
	sort.Strings(projects)

	var b strings.Builder
	fmt.Fprintf(&b, "tenant=%q\n", spec.Tenant)
	fmt.Fprintf(&b, "region=%q\n", region(spec.Region))
	fmt.Fprintf(&b, "ring=%q\n", ring(spec.Ring))
	fmt.Fprintf(&b, "mode=%q\n", string(mode))
	for _, p := range projects {
		fmt.Fprintf(&b, "project=%q\n", p)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
