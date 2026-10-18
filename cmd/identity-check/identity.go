package main

import (
	"errors"
	"fmt"
	"regexp"
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

var accountPattern = regexp.MustCompile(`^[0-9]{12}$`)

// validateAccount refuses a malformed -expect-account at startup, so a typo
// fails the Job at once instead of reading as an account mismatch. Empty
// means the flag was not given.
func validateAccount(id string) error {
	if id != "" && !accountPattern.MatchString(id) {
		return fmt.Errorf("-expect-account %q is not a 12-digit AWS account id", id)
	}
	return nil
}

// arnAccount is the account segment of an ARN
// (arn:<partition>:<service>:<region>:<account>:<resource>).
func arnAccount(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 {
		return ""
	}
	return parts[4]
}

// accountOf is the caller's account for logging: the GetCallerIdentity
// Account field, or the ARN's segment when the field is empty.
func accountOf(account, arn string) string {
	if account != "" {
		return account
	}
	return arnAccount(arn)
}

// checkAccount verifies the caller runs in the expected account. The
// GetCallerIdentity Account field and the ARN's account segment must both
// equal want; if they disagree with each other the response is not trusted
// and the check fails, whichever of them matches.
func checkAccount(account, arn, want string) error {
	fromARN := arnAccount(arn)
	if account != fromARN {
		return fmt.Errorf("the caller's account is inconsistent: GetCallerIdentity says %q, its ARN says %q", account, fromARN)
	}
	if account != want {
		return fmt.Errorf("the pod runs in account %q, want %q", account, want)
	}
	return nil
}
