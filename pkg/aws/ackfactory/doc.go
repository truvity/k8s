// Package ackfactory deploys the AWS identity of the ACK controllers: the
// roles, the policies and the Pod Identity associations the controllers that
// cannot bootstrap themselves need, plus the per-project capability roles
// the controllers assume.
//
// Four functions, each run from the caller's cluster program:
//
//   - IAM and EKS: the identities of the ACK IAM and EKS controllers. The IAM
//     controller mints every other in-bundle IAM role, the EKS controller
//     mints Pod Identity associations for workloads. Neither can be created
//     by ACK itself, so they are Pulumi's.
//   - Services: the identity of the service controllers (s3, kms, dynamodb).
//     Adoption is check-then-import: a role, policy or association that is
//     already live is adopted instead of created. The lookups are the
//     caller's (a Lookup), so this package carries no cloud SDK.
//   - ProjectRoles: one capability role per project, which the controllers
//     assume for the project's resources, under a permissions boundary that
//     requires the project and cluster tags and entraps what the role creates.
//
// These are plain functions, not components. They register the same
// resources, under the same logical names, as the program they were lifted
// from, so adopting them is no change to the stack: names are API (see
// docs/decisions/0002-urn-stability.md). The identities that are Pod Identity
// roles are podidentity components, registered with LegacyTopLevel.
//
// What the package does not know: the cluster's name, account, region and
// partition, the names of the permissions boundaries and which projects have
// which capabilities. All are inputs.
package ackfactory
