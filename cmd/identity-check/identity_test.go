package main

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
		fail bool
	}{
		{
			name: "pod identity",
			env: map[string]string{
				"AWS_CONTAINER_CREDENTIALS_FULL_URI":     "http://169.254.170.23/v1/credentials",
				"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": "/var/run/secrets/pods.eks.amazonaws.com/serviceaccount/eks-pod-identity-token",
			},
			want: kindPodIdentity,
		},
		{
			name: "irsa",
			env: map[string]string{
				"AWS_ROLE_ARN":                "arn:example:iam::ACCOUNT:role/r",
				"AWS_WEB_IDENTITY_TOKEN_FILE": "/var/run/secrets/eks.amazonaws.com/serviceaccount/token",
			},
			want: kindIRSA,
		},
		{name: "nothing injected", env: map[string]string{}, fail: true},
		{
			name: "pod identity without its token file",
			env:  map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": "http://169.254.170.23/v1/credentials"},
			fail: true,
		},
		{
			name: "irsa without its token file",
			env:  map[string]string{"AWS_ROLE_ARN": "arn:example:iam::ACCOUNT:role/r"},
			fail: true,
		},
		{
			name: "static keys are not a workload identity",
			env:  map[string]string{"AWS_ACCESS_KEY_ID": "x", "AWS_SECRET_ACCESS_KEY": "y"},
			fail: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := detect(func(k string) string { return tt.env[k] })
			if tt.fail {
				if !errors.Is(err, errNoIdentity) {
					t.Fatalf("err = %v, want errNoIdentity", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("kind = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRoleName(t *testing.T) {
	tests := []struct {
		name string
		arn  string
		want string
		fail bool
	}{
		{name: "assumed role", arn: "arn:example:sts::ACCOUNT:assumed-role/app-role/session-1", want: "app-role"},
		{name: "another partition", arn: "arn:example-cn:sts::ACCOUNT:assumed-role/app-role/s", want: "app-role"},
		{name: "session with a dot", arn: "arn:example:sts::ACCOUNT:assumed-role/app-role/a.b@c", want: "app-role"},
		{name: "an IAM user", arn: "arn:example:iam::ACCOUNT:user/someone", fail: true},
		{name: "a role, not a session", arn: "arn:example:iam::ACCOUNT:role/app-role", fail: true},
		{name: "no session", arn: "arn:example:sts::ACCOUNT:assumed-role/app-role", fail: true},
		{name: "empty role", arn: "arn:example:sts::ACCOUNT:assumed-role//s", fail: true},
		{name: "not an ARN", arn: "app-role", fail: true},
		{name: "empty", arn: "", fail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := roleName(tt.arn)
			if tt.fail {
				if err == nil {
					t.Fatalf("roleName(%q) = %q, want an error", tt.arn, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("role = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateAccount(t *testing.T) {
	for _, id := range []string{"", "111122223333"} {
		if err := validateAccount(id); err != nil {
			t.Errorf("validateAccount(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range []string{"1111", "11112222333", "1111222233334", "11112222333a", " 111122223333", "arn:example:iam::111122223333:root"} {
		if err := validateAccount(id); err == nil {
			t.Errorf("validateAccount(%q) = nil, want an error", id)
		}
	}
}

func TestCheckAccount(t *testing.T) {
	const arn = "arn:example:sts::111122223333:assumed-role/app-role/s"
	tests := []struct {
		name, account, arn, want, errPart string
	}{
		{name: "match", account: "111122223333", arn: arn, want: "111122223333"},
		{name: "mismatch", account: "111122223333", arn: arn, want: "444455556666", errPart: "want"},
		{name: "arn and field disagree, field matches", account: "444455556666", arn: arn, want: "444455556666", errPart: "inconsistent"},
		{name: "arn and field disagree, arn matches", account: "444455556666", arn: arn, want: "111122223333", errPart: "inconsistent"},
		{name: "empty field", account: "", arn: arn, want: "111122223333", errPart: "inconsistent"},
		{name: "not an ARN", account: "111122223333", arn: "x", want: "111122223333", errPart: "inconsistent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkAccount(tt.account, tt.arn, tt.want)
			if tt.errPart == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.errPart) {
				t.Fatalf("err = %v, want one containing %q", err, tt.errPart)
			}
		})
	}
}

// TestRunMalformedExpectAccount pins that a malformed flag fails before
// detection: it is a usage error, not an identity failure.
func TestRunMalformedExpectAccount(t *testing.T) {
	err := run(t.Context(), discardLogger(), []string{"-expect-account", "123"}, func(string) string { return "" })
	if err == nil || errors.Is(err, errNoIdentity) {
		t.Fatalf("err = %v, want a malformed-flag error", err)
	}
}

// TestRunNoIdentity pins the order: with nothing injected the command fails
// on detection, before any SDK configuration exists to fall back to IMDS.
func TestRunNoIdentity(t *testing.T) {
	err := run(t.Context(), discardLogger(), nil, func(string) string { return "" })
	if !errors.Is(err, errNoIdentity) {
		t.Fatalf("err = %v, want errNoIdentity", err)
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }
