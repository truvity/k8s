// Package clusterout is the Pulumi output type of the provider-neutral
// contract: a pulumi.Output whose element is a cluster.Outputs. Every
// provider component reports its contract as an Output, so a consumer reads
// any cluster the same way:
//
//	c.Contract.ApplyT(func(o cluster.Outputs) (string, error) {
//		if err := o.Validate(); err != nil {
//			return "", err
//		}
//		return o.Endpoint, nil
//	})
//
// It is its own package so pkg/cluster stays free of the Pulumi SDK.
package clusterout

import (
	"context"
	"reflect"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/cluster"
)

// Output is a pulumi.Output of cluster.Outputs.
type Output struct{ *pulumi.OutputState }

// ElementType is cluster.Outputs.
func (Output) ElementType() reflect.Type { return reflect.TypeFor[cluster.Outputs]() }

// ToOutput returns the output itself.
func (o Output) ToOutput(context.Context) Output { return o }

// Endpoint is the API server's https URL. It and the other field methods
// are the fields a consumer most often wires on, as string outputs.
func (o Output) Endpoint() pulumi.StringOutput {
	return o.ApplyT(func(v cluster.Outputs) string { return v.Endpoint }).(pulumi.StringOutput)
}

// CertificateAuthorityPEM is the PEM bundle of the API server's CA.
func (o Output) CertificateAuthorityPEM() pulumi.StringOutput {
	return o.ApplyT(func(v cluster.Outputs) string { return v.CertificateAuthorityPEM }).(pulumi.StringOutput)
}

// OIDCIssuer is the service-account token issuer, empty without
// workload identity.
func (o Output) OIDCIssuer() pulumi.StringOutput {
	return o.ApplyT(func(v cluster.Outputs) string { return v.OIDCIssuer }).(pulumi.StringOutput)
}

// Name is the cluster's name.
func (o Output) Name() pulumi.StringOutput {
	return o.ApplyT(func(v cluster.Outputs) string { return v.Name }).(pulumi.StringOutput)
}

// Validated is the output with cluster.Outputs.Validate applied: it fails
// (and with it every resource that depends on it) when the provider
// reported something a consumer cannot use.
func (o Output) Validated() Output {
	return o.ApplyT(func(v cluster.Outputs) (cluster.Outputs, error) { return v, v.Validate() }).(Output)
}

func init() {
	pulumi.RegisterOutputType(Output{})
}
