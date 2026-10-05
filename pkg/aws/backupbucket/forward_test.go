package backupbucket_test

import (
	"testing"

	"github.com/truvity/cnpg/v2/pkg/aws/backupstore"
	"github.com/truvity/k8s/pkg/aws/backupbucket"
)

// The forwarder must hand out the very types of the new home, so an importer
// that has not migrated yet still compiles and keeps its resource URNs.
func TestForwardsToBackupstore(t *testing.T) {
	var _ *backupstore.Args = &backupbucket.Args{}
	var _ *backupstore.BackupStore = (*backupbucket.BackupBucket)(nil)

	if backupbucket.TypeToken != backupstore.TypeToken {
		t.Fatalf("TypeToken %q, want %q", backupbucket.TypeToken, backupstore.TypeToken)
	}
}
