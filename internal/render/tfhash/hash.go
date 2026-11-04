// Package tfhash computes the Terraform input hash of an Instance. It is
// separate from internal/render/tf so that the manager binary can use it
// without linking CDKTF.
package tfhash

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	fleetv1alpha1 "github.com/Raef-Hany/fleet-operator/api/v1alpha1"
)

// DefaultRegion is the region used when an Instance does not set one.
const DefaultRegion = "us-central1"

// Region returns r, or DefaultRegion when r is empty.
func Region(r string) string {
	if r == "" {
		return DefaultRegion
	}
	return r
}

// Ring returns r, or the prod ring when r is empty.
func Ring(r fleetv1alpha1.Ring) string {
	if r == "" {
		return string(fleetv1alpha1.RingProd)
	}
	return string(r)
}

// ConnectivityMode returns m, or none when m is empty.
func ConnectivityMode(m fleetv1alpha1.ConnectivityMode) fleetv1alpha1.ConnectivityMode {
	if m == "" {
		return fleetv1alpha1.ConnectivityNone
	}
	return m
}

// Hash returns the sha256 hex digest of the Terraform-relevant inputs of spec.
// Version and size are deliberately excluded: they do not change the rendered
// Terraform. Defaults are applied before hashing so that an omitted field and
// its explicit default produce the same digest.
func Hash(spec fleetv1alpha1.InstanceSpec) string {
	mode := fleetv1alpha1.ConnectivityNone
	var projects []string
	if spec.Connectivity != nil {
		mode = ConnectivityMode(spec.Connectivity.Mode)
		projects = append(projects, spec.Connectivity.AllowedProjects...)
	}
	sort.Strings(projects)

	var b strings.Builder
	fmt.Fprintf(&b, "tenant=%q\n", spec.Tenant)
	fmt.Fprintf(&b, "region=%q\n", Region(spec.Region))
	fmt.Fprintf(&b, "ring=%q\n", Ring(spec.Ring))
	fmt.Fprintf(&b, "mode=%q\n", string(mode))
	for _, p := range projects {
		fmt.Fprintf(&b, "project=%q\n", p)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
