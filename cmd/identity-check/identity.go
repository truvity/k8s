package main

import (
	"errors"
	"fmt"
	"strings"
)

// The two ways EKS hands a pod a cloud identity, and the variables each one
// leaves in the container. Both are injected by the cluster's pod-admission
// webhook when the pod is CREATED, which is the whole reason this command
// exists: a pod admitted before the webhook could see its association has
// neither, and nothing that happens inside that pod afterwards adds them.
const (
	kindPodIdentity = "pod-identity"
	kindIRSA        = "irsa"
)

// errNoIdentity is the failure this command exists to surface. It is a
// distinct value so the caller can say what to do about it: a new pod, not a
// retry in this one.
var errNoIdentity = errors.New("no workload identity was injected: the pod was admitted before its association was visible; the Job retries with a new pod")

// detect names the workload identity the pod was admitted with, from the
// environment alone. It reads nothing else: in particular it never asks the
// instance metadata service, which on a node would answer with the NODE's
// role and make a pod that has no identity of its own look like it has one.
func detect(getenv func(string) string) (string, error) {
	switch {
	case getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "" &&
		getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE") != "":
		return kindPodIdentity, nil
	case getenv("AWS_ROLE_ARN") != "" && getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != "":
		return kindIRSA, nil
	default:
		return "", errNoIdentity
	}
}

// roleName is the role in an assumed-role ARN:
// arn:<partition>:sts::<account>:assumed-role/<role>/<session>.
//
// A role's path is not part of an assumed-role ARN, so the name compared
// with -expect-role is the role's own name and never `path/name`.
func roleName(arn string) (string, error) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "sts" {
		return "", fmt.Errorf("%q is not an STS ARN", arn)
	}
	resource := strings.Split(parts[5], "/")
	if len(resource) != 3 || resource[0] != "assumed-role" || resource[1] == "" {
		return "", fmt.Errorf("%q is not an assumed-role ARN", arn)
	}
	return resource[1], nil
}
