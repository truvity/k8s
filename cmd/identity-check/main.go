// Command identity-check proves that the pod it runs in was admitted WITH a
// workload identity, and that the identity works, then exits.
//
// WHY. An EKS Pod Identity association is eventually consistent
// (https://docs.aws.amazon.com/eks/latest/APIReference/API_CreatePodIdentityAssociation.html):
// it can be reported as created seconds before the cluster's pod-admission
// path can see it. Credentials are injected only when a pod is created, so a
// pod admitted in that gap has no credentials for its whole life, and
// restarting its containers does not change that. The gate is therefore a
// Job with restartPolicy Never whose retries are NEW pods, each admitted
// again; this command is what that Job runs. The decision is
// truvity/policy's 0013, and charts reach this image through that
// repository's `service-lib.identityCheck` template.
//
// It runs as the ServiceAccount the release's workloads will run as. It asks
// STS who it is, which needs no IAM permission, and it never reads the
// instance metadata service: a node's role would answer, and a pod with no
// identity of its own would look like it has one.
//
// Flags:
//
//	-expect-role NAME  fail unless the assumed role's name is NAME (optional)
//	-timeout D         bound on the STS call, retries included (default 30s)
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(context.Background(), log, os.Args[1:], os.Getenv); err != nil {
		log.ErrorContext(context.Background(), "identity check failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger, args []string, getenv func(string) string) error {
	flags := flag.NewFlagSet("identity-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	expect := flags.String("expect-role", "", "the role name the pod must have assumed (optional)")
	timeout := flags.Duration("timeout", 30*time.Second, "how long to wait for STS, retries included")
	if err := flags.Parse(args); err != nil {
		return err
	}

	// Before anything is built that could reach for another credential
	// source: with no injected identity the SDK's default chain ends at the
	// instance metadata service.
	kind, err := detect(getenv)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	// Belt and braces beside detect: whatever else is true of the
	// environment, this process does not ask the node who it is.
	cfg, err := config.LoadDefaultConfig(ctx, config.WithEC2IMDSClientEnableState(imds.ClientDisabled))
	if err != nil {
		return fmt.Errorf("load the SDK configuration: %w", err)
	}

	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("sts GetCallerIdentity with the injected %s identity: %w", kind, err)
	}
	arn := aws.ToString(out.Arn)

	role, err := roleName(arn)
	if err != nil {
		return fmt.Errorf("the caller is not an assumed role: %w", err)
	}
	if *expect != "" && role != *expect {
		return fmt.Errorf("the pod assumed role %q, want %q", role, *expect)
	}

	log.InfoContext(ctx, "workload identity verified",
		slog.String("kind", kind), slog.String("role", role), slog.String("arn", arn))
	return nil
}
