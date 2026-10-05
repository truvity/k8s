// Package backupbucket is DEPRECATED: the cross-account backup bucket and its
// KMS key moved to github.com/truvity/cnpg/v2/pkg/aws/backupstore (K5 of the
// estate model: the backup bucket belongs to the CNPG family).
//
// Everything here forwards to that package, so an importer keeps compiling
// and keeps its resource URNs: the component's type changes from
// truvity:k8s/aws:BackupBucket to truvity:cnpg/aws:BackupStore, and the
// new component aliases itself from the old type. Import backupstore
// directly. This package is removed in the release after next.
//
// Deprecated: use github.com/truvity/cnpg/v2/pkg/aws/backupstore.
package backupbucket

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/truvity/cnpg/v2/pkg/aws/backupstore"
)

// TypeToken is the Pulumi type of the component.
//
// Deprecated: use backupstore.TypeToken.
const TypeToken = backupstore.TypeToken

// DefaultPartition is the AWS partition used when Args.Partition is empty.
//
// Deprecated: use backupstore.DefaultPartition.
const DefaultPartition = backupstore.DefaultPartition

type (
	// Replica configures the same-account replica in another region.
	//
	// Deprecated: use backupstore.Replica.
	Replica = backupstore.Replica
	// Args configures the component.
	//
	// Deprecated: use backupstore.Args.
	Args = backupstore.Args
	// BackstopArgs configures Backstop.
	//
	// Deprecated: use backupstore.BackstopArgs.
	BackstopArgs = backupstore.BackstopArgs
	// Transition is one storage-class transition of Backstop.
	//
	// Deprecated: use backupstore.Transition.
	Transition = backupstore.Transition
	// BackupBucket is the component.
	//
	// Deprecated: use backupstore.BackupStore.
	BackupBucket = backupstore.BackupStore
)

// Backstop builds the whole-bucket lifecycle rule.
//
// Deprecated: use backupstore.Backstop.
var Backstop = backupstore.Backstop

// New registers the component and its children.
//
// Deprecated: use backupstore.New.
func New(ctx *pulumi.Context, name string, args *Args, opts ...pulumi.ResourceOption) (*BackupBucket, error) {
	return backupstore.New(ctx, name, args, opts...)
}
