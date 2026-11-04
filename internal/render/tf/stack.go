package tf

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/aws/jsii-runtime-go"
	"github.com/hashicorp/terraform-cdk-go/cdktf"

	"github.com/cdktf/cdktf-provider-google-go/google/v16/computeaddress"
	"github.com/cdktf/cdktf-provider-google-go/google/v16/computeforwardingrule"
	"github.com/cdktf/cdktf-provider-google-go/google/v16/computenetwork"
	"github.com/cdktf/cdktf-provider-google-go/google/v16/computesubnetwork"
	"github.com/cdktf/cdktf-provider-google-go/google/v16/provider"
	"github.com/cdktf/cdktf-provider-google-go/google/v16/serviceaccount"
	"github.com/cdktf/cdktf-provider-google-go/google/v16/storagebucket"

	fleetv1alpha1 "github.com/Raef-Hany/fleet-operator/api/v1alpha1"
	"github.com/Raef-Hany/fleet-operator/internal/render/tfhash"
)

const (
	stackName          = "inst"
	subnetCIDR         = "10.10.0.0/24"
	privateLinkMessage = "AWS PrivateLink is not implemented in this learning project; this output is a placeholder and no AWS PrivateLink resources are rendered"
	privateLinkOutput  = "privatelink_not_implemented"
	managedBy          = "fleet-operator"
)

// accountID builds a service account id of the form sa-<tenant>. It is kept
// within the 6..30 character range and ends in a letter or digit.
func accountID(tenant string) string {
	id := "sa-" + tenant
	if len(id) > 30 {
		sum := sha256.Sum256([]byte(tenant))
		id = id[:21] + "-" + hex.EncodeToString(sum[:])[:8]
	}
	id = strings.TrimRight(id, "-")
	for len(id) < 6 {
		id += "-sa"
	}
	return id
}

func description(tenant, ring string) string {
	return fmt.Sprintf("tenant=%s ring=%s managed-by=%s", tenant, ring, managedBy)
}

// buildStack declares the stack for spec in app.
func buildStack(app cdktf.App, spec fleetv1alpha1.InstanceSpec) {
	tenant := spec.Tenant
	reg := tfhash.Region(spec.Region)
	rng := tfhash.Ring(spec.Ring)
	mode := fleetv1alpha1.ConnectivityNone
	if spec.Connectivity != nil {
		mode = tfhash.ConnectivityMode(spec.Connectivity.Mode)
	}
	labels := &map[string]*string{
		"tenant":     jsii.String(tenant),
		"ring":       jsii.String(rng),
		"managed-by": jsii.String(managedBy),
	}
	desc := jsii.String(description(tenant, rng))

	st := cdktf.NewTerraformStack(app, jsii.String(stackName))
	cdktf.NewLocalBackend(st, &cdktf.LocalBackendConfig{Path: jsii.String("terraform.tfstate")})

	projectID := cdktf.NewTerraformVariable(st, jsii.String("project_id"), &cdktf.TerraformVariableConfig{
		Type: jsii.String("string"),
	})
	provider.NewGoogleProvider(st, jsii.String("google"), &provider.GoogleProviderConfig{
		Region:  jsii.String(reg),
		Project: projectID.StringValue(),
	})

	net := computenetwork.NewComputeNetwork(st, jsii.String("net"), &computenetwork.ComputeNetworkConfig{
		Name:                  jsii.String("net-" + tenant),
		AutoCreateSubnetworks: jsii.Bool(false),
		Description:           desc,
	})
	sub := computesubnetwork.NewComputeSubnetwork(st, jsii.String("subnet"), &computesubnetwork.ComputeSubnetworkConfig{
		Name:        jsii.String("subnet-" + tenant),
		Network:     net.Name(),
		IpCidrRange: jsii.String(subnetCIDR),
		Region:      jsii.String(reg),
		Description: desc,
	})
	serviceaccount.NewServiceAccount(st, jsii.String("sa"), &serviceaccount.ServiceAccountConfig{
		AccountId:   jsii.String(accountID(tenant)),
		Description: desc,
	})
	storagebucket.NewStorageBucket(st, jsii.String("bucket"), &storagebucket.StorageBucketConfig{
		Name:                     jsii.String("fleet-" + tenant + "-data"),
		Location:                 jsii.String(reg),
		UniformBucketLevelAccess: jsii.Bool(true),
		ForceDestroy:             jsii.Bool(false),
		Labels:                   labels,
	})

	switch mode {
	case fleetv1alpha1.ConnectivityPSC:
		attachment := cdktf.NewTerraformVariable(st, jsii.String("service_attachment_uri"), &cdktf.TerraformVariableConfig{
			Type: jsii.String("string"),
		})
		addr := computeaddress.NewComputeAddress(st, jsii.String("psc_address"), &computeaddress.ComputeAddressConfig{
			Name:        jsii.String("psc-" + tenant),
			AddressType: jsii.String("INTERNAL"),
			Purpose:     jsii.String("GCE_ENDPOINT"),
			Region:      jsii.String(reg),
			Subnetwork:  sub.Name(),
			Labels:      labels,
		})
		computeforwardingrule.NewComputeForwardingRule(st, jsii.String("psc_endpoint"), &computeforwardingrule.ComputeForwardingRuleConfig{
			Name:                jsii.String("psc-" + tenant),
			Region:              jsii.String(reg),
			Network:             net.Name(),
			IpAddress:           addr.Address(),
			Target:              attachment.StringValue(),
			LoadBalancingScheme: jsii.String(""),
			Labels:              labels,
		})
	case fleetv1alpha1.ConnectivityPrivateLink:
		cdktf.NewTerraformOutput(st, jsii.String(privateLinkOutput), &cdktf.TerraformOutputConfig{
			Value: jsii.String(privateLinkMessage),
		})
	}
}
