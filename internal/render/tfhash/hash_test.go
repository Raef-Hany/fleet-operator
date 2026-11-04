package tfhash

import (
	"regexp"
	"testing"

	fleetv1alpha1 "github.com/Raef-Hany/fleet-operator/api/v1alpha1"
)

var hexHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func baseSpec() fleetv1alpha1.InstanceSpec {
	return fleetv1alpha1.InstanceSpec{
		Tenant:  "acme",
		Version: "1.2.3",
		Size:    fleetv1alpha1.SizeSmall,
		Region:  "europe-west1",
		Ring:    fleetv1alpha1.RingTrial,
		Connectivity: &fleetv1alpha1.Connectivity{
			Mode:            fleetv1alpha1.ConnectivityPSC,
			AllowedProjects: []string{"consumer-a", "consumer-b"},
		},
	}
}

func TestHash_IsDeterministicSha256Hex(t *testing.T) {
	h1 := Hash(baseSpec())
	h2 := Hash(baseSpec())
	if !hexHash.MatchString(h1) {
		t.Fatalf("Hash() = %q, want 64 lowercase hex characters", h1)
	}
	if h1 != h2 {
		t.Fatalf("Hash() not deterministic: %q != %q", h1, h2)
	}
}

func TestHash_TerraformInputsChangeTheHash(t *testing.T) {
	base := Hash(baseSpec())
	cases := map[string]func(s *fleetv1alpha1.InstanceSpec){
		"tenant": func(s *fleetv1alpha1.InstanceSpec) {
			s.Tenant = "globex"
		},
		"region": func(s *fleetv1alpha1.InstanceSpec) {
			s.Region = "us-east1"
		},
		"ring": func(s *fleetv1alpha1.InstanceSpec) {
			s.Ring = fleetv1alpha1.RingProd
		},
		"connectivity mode": func(s *fleetv1alpha1.InstanceSpec) {
			s.Connectivity.Mode = fleetv1alpha1.ConnectivityPrivateLink
		},
		"allowed project added": func(s *fleetv1alpha1.InstanceSpec) {
			s.Connectivity.AllowedProjects = append(s.Connectivity.AllowedProjects, "consumer-c")
		},
		"allowed project replaced": func(s *fleetv1alpha1.InstanceSpec) {
			s.Connectivity.AllowedProjects = []string{"consumer-a", "consumer-z"}
		},
		"connectivity removed": func(s *fleetv1alpha1.InstanceSpec) {
			s.Connectivity = nil
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := baseSpec()
			mutate(&s)
			if got := Hash(s); got == base {
				t.Fatalf("Hash() unchanged after changing %s: %q", name, got)
			}
		})
	}
}

func TestHash_AllowedProjectOrderDoesNotMatter(t *testing.T) {
	a := baseSpec()
	a.Connectivity.AllowedProjects = []string{"consumer-a", "consumer-b"}
	b := baseSpec()
	b.Connectivity.AllowedProjects = []string{"consumer-b", "consumer-a"}
	if Hash(a) != Hash(b) {
		t.Fatalf("Hash() differs for reordered allowedProjects: %q vs %q", Hash(a), Hash(b))
	}
}

func TestHash_DoesNotReorderCallersSlice(t *testing.T) {
	s := baseSpec()
	s.Connectivity.AllowedProjects = []string{"consumer-b", "consumer-a"}
	_ = Hash(s)
	if got := s.Connectivity.AllowedProjects; got[0] != "consumer-b" || got[1] != "consumer-a" {
		t.Fatalf("Hash() mutated caller's allowedProjects: %v", got)
	}
}

func TestHash_VersionAndSizeDoNotAffectHash(t *testing.T) {
	base := Hash(baseSpec())
	s := baseSpec()
	s.Version = "9.9.9"
	if got := Hash(s); got != base {
		t.Errorf("Hash() changed with version: %q != %q", got, base)
	}
	s = baseSpec()
	s.Size = fleetv1alpha1.SizeLarge
	if got := Hash(s); got != base {
		t.Errorf("Hash() changed with size: %q != %q", got, base)
	}
}

func TestHash_DefaultsAreAppliedBeforeHashing(t *testing.T) {
	bare := fleetv1alpha1.InstanceSpec{Tenant: "acme", Version: "1.2.3"}
	explicit := fleetv1alpha1.InstanceSpec{
		Tenant:       "acme",
		Version:      "1.2.3",
		Region:       "us-central1",
		Ring:         fleetv1alpha1.RingProd,
		Connectivity: &fleetv1alpha1.Connectivity{Mode: fleetv1alpha1.ConnectivityNone},
	}
	if Hash(bare) != Hash(explicit) {
		t.Errorf("empty defaults hash differently from explicit defaults")
	}

	emptyMode := explicit
	emptyMode.Connectivity = &fleetv1alpha1.Connectivity{}
	if Hash(emptyMode) != Hash(explicit) {
		t.Errorf("empty connectivity mode hashes differently from mode none")
	}

	emptyProjects := explicit
	emptyProjects.Connectivity = &fleetv1alpha1.Connectivity{Mode: fleetv1alpha1.ConnectivityNone, AllowedProjects: []string{}}
	if Hash(emptyProjects) != Hash(explicit) {
		t.Errorf("empty allowedProjects hashes differently from nil")
	}

	otherRegion := explicit
	otherRegion.Region = "europe-west1"
	if Hash(otherRegion) == Hash(explicit) {
		t.Errorf("default region equals a non-default region hash")
	}
}
