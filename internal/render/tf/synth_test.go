package tf

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/jsii-runtime-go"

	fleetv1alpha1 "github.com/Raef-Hany/fleet-operator/api/v1alpha1"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/")

const placeholderText = "AWS PrivateLink is not implemented in this learning project; this output is a placeholder and no AWS PrivateLink resources are rendered"

var (
	drivePath     = regexp.MustCompile(`[^A-Za-z][A-Za-z]:[\\/]`)
	serviceAcctRe = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
)

func TestMain(m *testing.M) {
	code := m.Run()
	jsii.Close()
	os.Exit(code)
}

func modeInstance(mode fleetv1alpha1.ConnectivityMode) *fleetv1alpha1.Instance {
	conn := &fleetv1alpha1.Connectivity{Mode: mode}
	if mode != fleetv1alpha1.ConnectivityNone {
		conn.AllowedProjects = []string{"consumer-a"}
	}
	return &fleetv1alpha1.Instance{
		Spec: fleetv1alpha1.InstanceSpec{
			Tenant:       "acme",
			Version:      "1.2.3",
			Size:         fleetv1alpha1.SizeMedium,
			Region:       "europe-west1",
			Ring:         fleetv1alpha1.RingTrial,
			Connectivity: conn,
		},
	}
}

// resourcesOfType returns the bodies of every resource of the given Terraform type.
func resourcesOfType(t *testing.T, doc map[string]any, typ string) []map[string]any {
	t.Helper()
	var out []map[string]any
	res, _ := doc["resource"].(map[string]any)
	byLabel, _ := res[typ].(map[string]any)
	for label, body := range byLabel {
		b, ok := body.(map[string]any)
		if !ok {
			t.Fatalf("resource %s.%s is not an object", typ, label)
		}
		out = append(out, b)
	}
	return out
}

func requireOne(t *testing.T, doc map[string]any, typ string) map[string]any {
	t.Helper()
	bodies := resourcesOfType(t, doc, typ)
	if len(bodies) != 1 {
		t.Fatalf("%s: got %d resources, want 1", typ, len(bodies))
	}
	return bodies[0]
}

func requireNone(t *testing.T, doc map[string]any, typ string) {
	t.Helper()
	if n := len(resourcesOfType(t, doc, typ)); n != 0 {
		t.Fatalf("%s: got %d resources, want 0", typ, n)
	}
}

func requireField(t *testing.T, body map[string]any, key string, want any) {
	t.Helper()
	if got := body[key]; got != want {
		t.Errorf("field %q = %#v, want %#v", key, got, want)
	}
}

func requireLabels(t *testing.T, body map[string]any, tenant, ring string) {
	t.Helper()
	labels, _ := body["labels"].(map[string]any)
	want := map[string]any{"tenant": tenant, "ring": ring, "managed-by": "fleet-operator"}
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("labels[%q] = %#v, want %#v (labels=%v)", k, labels[k], v, labels)
		}
	}
}

func requireDescriptionFacts(t *testing.T, body map[string]any, tenant, ring string) {
	t.Helper()
	desc, _ := body["description"].(string)
	for _, fact := range []string{"tenant=" + tenant, "ring=" + ring, "managed-by=fleet-operator"} {
		if !strings.Contains(desc, fact) {
			t.Errorf("description %q lacks %q", desc, fact)
		}
	}
}

func requireNoLeakedPaths(t *testing.T, got []byte, outDir string) {
	t.Helper()
	for _, p := range []string{outDir, filepath.ToSlash(outDir), strings.ReplaceAll(outDir, `\`, `\\`)} {
		if strings.Contains(string(got), p) {
			t.Errorf("output leaks absolute path %q", p)
		}
	}
	if drivePath.Match(got) {
		t.Errorf("output contains a Windows drive path")
	}
	if regexp.MustCompile(`\d{4}-\d{2}-\d{2}T`).Match(got) {
		t.Errorf("output contains a timestamp")
	}
}

// checkCommon asserts resources that every connectivity mode renders.
func checkCommon(t *testing.T, doc map[string]any, tenant, saID, region, ring string) {
	t.Helper()

	net := requireOne(t, doc, "google_compute_network")
	requireField(t, net, "name", "net-"+tenant)
	requireField(t, net, "auto_create_subnetworks", false)
	requireDescriptionFacts(t, net, tenant, ring)

	sub := requireOne(t, doc, "google_compute_subnetwork")
	requireField(t, sub, "ip_cidr_range", "10.10.0.0/24")
	requireField(t, sub, "region", region)
	requireDescriptionFacts(t, sub, tenant, ring)

	sa := requireOne(t, doc, "google_service_account")
	requireField(t, sa, "account_id", saID)
	requireDescriptionFacts(t, sa, tenant, ring)

	bucket := requireOne(t, doc, "google_storage_bucket")
	requireField(t, bucket, "name", "fleet-"+tenant+"-data")
	requireField(t, bucket, "location", region)
	requireField(t, bucket, "uniform_bucket_level_access", true)
	requireField(t, bucket, "force_destroy", false)
	requireLabels(t, bucket, tenant, ring)

	provider := requireGoogleProvider(t, doc)
	requireField(t, provider, "region", region)
	requireField(t, provider, "project", "${var.project_id}")

	variables, _ := doc["variable"].(map[string]any)
	if _, ok := variables["project_id"]; !ok {
		t.Errorf("variable project_id missing")
	}
}

func requireGoogleProvider(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	providers, _ := doc["provider"].(map[string]any)
	switch p := providers["google"].(type) {
	case map[string]any:
		return p
	case []any:
		if len(p) == 1 {
			if m, ok := p[0].(map[string]any); ok {
				return m
			}
		}
	}
	t.Fatalf("google provider missing or malformed: %v", providers)
	return nil
}

func TestSynth_Golden(t *testing.T) {
	cases := []struct {
		name string
		mode fleetv1alpha1.ConnectivityMode
	}{
		{"none", fleetv1alpha1.ConnectivityNone},
		{"psc", fleetv1alpha1.ConnectivityPSC},
		{"privatelink", fleetv1alpha1.ConnectivityPrivateLink},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// One synth per mode; the golden comparison and every structural
			// assertion below share this single output.
			outDir := t.TempDir()
			path, err := Synth(modeInstance(tc.mode), outDir)
			if err != nil {
				t.Fatalf("Synth() error: %v", err)
			}
			if want := filepath.Join(outDir, "cdk.tf.json"); path != want {
				t.Fatalf("Synth() path = %q, want %q", path, want)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading synthesized output: %v", err)
			}
			requireNoLeakedPaths(t, got, outDir)

			golden := filepath.Join("testdata", tc.name+".cdk.tf.json")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("reading golden file (run with -update to create): %v", err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("output differs from %s; rerun with -update and review the diff", golden)
				}
			}

			var doc map[string]any
			if err := json.Unmarshal(got, &doc); err != nil {
				t.Fatalf("output is not JSON: %v", err)
			}
			checkCommon(t, doc, "acme", "sa-acme", "europe-west1", "trial")

			switch tc.mode {
			case fleetv1alpha1.ConnectivityNone:
				requireNone(t, doc, "google_compute_address")
				requireNone(t, doc, "google_compute_forwarding_rule")
				if _, ok := doc["output"]; ok {
					t.Errorf("none mode must not render outputs, got %v", doc["output"])
				}
				if vars, _ := doc["variable"].(map[string]any); vars["service_attachment_uri"] != nil {
					t.Errorf("none mode must not declare service_attachment_uri")
				}

			case fleetv1alpha1.ConnectivityPSC:
				addr := requireOne(t, doc, "google_compute_address")
				requireField(t, addr, "purpose", "GCE_ENDPOINT")
				requireField(t, addr, "address_type", "INTERNAL")
				requireField(t, addr, "region", "europe-west1")
				requireLabels(t, addr, "acme", "trial")

				fr := requireOne(t, doc, "google_compute_forwarding_rule")
				requireField(t, fr, "target", "${var.service_attachment_uri}")
				requireField(t, fr, "load_balancing_scheme", "")
				requireField(t, fr, "region", "europe-west1")
				requireLabels(t, fr, "acme", "trial")

				vars, _ := doc["variable"].(map[string]any)
				if vars["service_attachment_uri"] == nil {
					t.Errorf("psc mode must declare variable service_attachment_uri")
				}
				if _, ok := doc["output"]; ok {
					t.Errorf("psc mode must not render the privatelink output")
				}

			case fleetv1alpha1.ConnectivityPrivateLink:
				requireNone(t, doc, "google_compute_address")
				requireNone(t, doc, "google_compute_forwarding_rule")
				outputs, _ := doc["output"].(map[string]any)
				out, _ := outputs["privatelink_not_implemented"].(map[string]any)
				requireField(t, out, "value", placeholderText)
				if vars, _ := doc["variable"].(map[string]any); vars["service_attachment_uri"] != nil {
					t.Errorf("privatelink mode must not declare service_attachment_uri")
				}
			}
		})
	}
}

func TestSynth_DefaultsAndSecondTenant(t *testing.T) {
	// Empty region and ring must default to us-central1 and prod, and a
	// one-character tenant must still yield valid, distinct resource names.
	inst := &fleetv1alpha1.Instance{Spec: fleetv1alpha1.InstanceSpec{Tenant: "x", Version: "1.2.3"}}
	outDir := filepath.Join(t.TempDir(), "nested", "out")
	path, err := Synth(inst, outDir)
	if err != nil {
		t.Fatalf("Synth() error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading synthesized output: %v", err)
	}
	requireNoLeakedPaths(t, got, outDir)
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	checkCommon(t, doc, "x", "sa-x-sa", "us-central1", "prod")
	requireNone(t, doc, "google_compute_address")
}

func TestSynth_RejectsBadInput(t *testing.T) {
	good := modeInstance(fleetv1alpha1.ConnectivityNone)
	emptyTenant := modeInstance(fleetv1alpha1.ConnectivityNone)
	emptyTenant.Spec.Tenant = ""

	cases := map[string]struct {
		inst   *fleetv1alpha1.Instance
		outDir string
	}{
		"nil instance": {nil, t.TempDir()},
		"empty tenant": {emptyTenant, t.TempDir()},
		"empty outDir": {good, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path, err := Synth(tc.inst, tc.outDir)
			if err == nil {
				t.Fatalf("Synth() = %q, nil error; want error", path)
			}
		})
	}
}

func TestAccountID(t *testing.T) {
	cases := map[string]string{
		"acme":  "sa-acme",
		"x":     "sa-x-sa",
		"ab":    "sa-ab-sa",
		"a-b-c": "sa-a-b-c",
	}
	for tenant, want := range cases {
		if got := accountID(tenant); got != want {
			t.Errorf("accountID(%q) = %q, want %q", tenant, got, want)
		}
	}

	long := strings.Repeat("a", 40)
	got := accountID(long)
	if len(got) > 30 {
		t.Errorf("accountID(40-char tenant) = %q (len %d), want at most 30", got, len(got))
	}
	if !serviceAcctRe.MatchString(got) {
		t.Errorf("accountID(40-char tenant) = %q is not a valid service account id", got)
	}

	trailingHyphen := strings.Repeat("b", 26) + "-c"
	got = accountID(trailingHyphen)
	if !serviceAcctRe.MatchString(got) {
		t.Errorf("accountID(%q) = %q is not a valid service account id", trailingHyphen, got)
	}
}

func TestAccountID_LongTenantsSharingPrefixDoNotCollide(t *testing.T) {
	a := strings.Repeat("a", 35) + "one"
	b := strings.Repeat("a", 35) + "two"
	ia, ib := accountID(a), accountID(b)
	if ia == ib {
		t.Fatalf("accountID collision for distinct tenants: %q", ia)
	}
	for _, id := range []string{ia, ib} {
		if len(id) > 30 || len(id) < 6 {
			t.Errorf("accountID %q has length %d, want 6..30", id, len(id))
		}
	}
	if accountID(a) != ia {
		t.Errorf("accountID is not deterministic")
	}
}

func TestPrivateLinkPlaceholderDoesNotClaimNoResourcesAtAll(t *testing.T) {
	if strings.Contains(privateLinkMessage, "no resources are rendered") {
		t.Errorf("message %q is inaccurate: base network resources are still rendered", privateLinkMessage)
	}
	if !strings.Contains(privateLinkMessage, "not implemented") {
		t.Errorf("message %q must say the feature is not implemented", privateLinkMessage)
	}
}
