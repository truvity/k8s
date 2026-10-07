// Package oidc generates, offline, the two documents an external system
// reads to verify a cluster's ServiceAccount tokens: the OpenID discovery
// document (<issuer>/.well-known/openid-configuration) and the key set
// (<issuer>/openid/v1/jwks). They are built the way kube-apiserver builds the
// copies it serves itself (the same key IDs, algorithms, field order and
// encoding), from the ServiceAccount signing key in the cluster's secrets.
//
// Publishing them at the issuer URL (a public bucket, a CDN in front of one)
// lets AWS STS, or any OIDC relying party, verify tokens without reaching
// the cluster's API. Because they are derived from the secrets bundle, not
// fetched from a running cluster, they exist before the cluster does: a
// cold start never waits on the cluster to publish its own keys, and a
// rebuild from the same bundle publishes the same bytes.
package oidc

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	jose "github.com/go-jose/go-jose/v4"
)

// Paths of the documents under the issuer URL.
const (
	DiscoveryPath = "/.well-known/openid-configuration"
	JWKSPath      = "/openid/v1/jwks"
)

// Documents are the discovery document and the key set, as served.
type Documents struct {
	// Discovery is the openid-configuration JSON.
	Discovery []byte
	// JWKS is the key set JSON.
	JWKS []byte
}

// discovery mirrors kube-apiserver's openIDMetadata: same fields, same order.
type discovery struct {
	Issuer        string   `json:"issuer"`
	JWKSURI       string   `json:"jwks_uri"`
	ResponseTypes []string `json:"response_types_supported"`
	SubjectTypes  []string `json:"subject_types_supported"`
	SigningAlgs   []string `json:"id_token_signing_alg_values_supported"`
}

// Generate builds both documents for an issuer from the public keys tokens
// are verified with: the current signing key's, plus any key still trusted
// during a rotation. The JWKS URI is <issuer>/openid/v1/jwks, which is what
// the machine config passes as service-account-jwks-uri.
func Generate(issuer string, keys ...crypto.PublicKey) (*Documents, error) {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(u.Path, "/") {
		return nil, fmt.Errorf("oidc: issuer %q is not an https URL without a trailing slash", issuer)
	}

	if len(keys) == 0 {
		return nil, errors.New("oidc: no key")
	}

	set := jose.JSONWebKeySet{}
	algs := map[string]bool{}
	seen := map[string]bool{}

	for i, k := range keys {
		alg, err := algorithm(k)
		if err != nil {
			return nil, fmt.Errorf("oidc: key %d: %w", i, err)
		}

		kid, err := KeyID(k)
		if err != nil {
			return nil, fmt.Errorf("oidc: key %d: %w", i, err)
		}

		if seen[kid] {
			return nil, fmt.Errorf("oidc: key %d repeats key %s", i, kid)
		}

		seen[kid] = true
		algs[alg] = true

		set.Keys = append(set.Keys, jose.JSONWebKey{Key: k, KeyID: kid, Algorithm: alg, Use: "sig"})
	}

	jwks, err := json.Marshal(set)
	if err != nil {
		return nil, fmt.Errorf("oidc: jwks: %w", err)
	}

	sorted := make([]string, 0, len(algs))
	for a := range algs {
		sorted = append(sorted, a)
	}

	sort.Strings(sorted)

	disc, err := json.Marshal(discovery{
		Issuer:        issuer,
		JWKSURI:       issuer + JWKSPath,
		ResponseTypes: []string{"id_token"},
		SubjectTypes:  []string{"public"},
		SigningAlgs:   sorted,
	})
	if err != nil {
		return nil, fmt.Errorf("oidc: discovery: %w", err)
	}

	return &Documents{Discovery: disc, JWKS: jwks}, nil
}

// KeyID is the key ID kube-apiserver gives a public key: the unpadded
// base64url SHA-256 of its PKIX DER encoding. Tokens carry it as "kid".
func KeyID(k crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		return "", fmt.Errorf("marshal public key: %w", err)
	}

	sum := sha256.Sum256(der)

	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func algorithm(k crypto.PublicKey) (string, error) {
	switch key := k.(type) {
	case *rsa.PublicKey:
		return "RS256", nil
	case *ecdsa.PublicKey:
		switch key.Curve {
		case elliptic.P256():
			return "ES256", nil
		case elliptic.P384():
			return "ES384", nil
		case elliptic.P521():
			return "ES512", nil
		}

		return "", errors.New("unsupported ECDSA curve")
	default:
		return "", fmt.Errorf("unsupported key type %T", k)
	}
}

// PublicKeysFromPEM reads every public key in PEM data: a private key
// (PKCS#1, PKCS#8 or SEC 1, as the Talos secrets bundle holds the
// ServiceAccount key) contributes its public half; a PUBLIC KEY block is
// taken as it is.
func PublicKeysFromPEM(data []byte) ([]crypto.PublicKey, error) {
	var out []crypto.PublicKey

	for {
		var block *pem.Block

		block, data = pem.Decode(data)
		if block == nil {
			break
		}

		k, err := publicKey(block)
		if err != nil {
			return nil, fmt.Errorf("oidc: PEM %s: %w", block.Type, err)
		}

		out = append(out, k)
	}

	if len(out) == 0 {
		return nil, errors.New("oidc: no PEM key")
	}

	return out, nil
}

func publicKey(block *pem.Block) (crypto.PublicKey, error) {
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}

		return &k.PublicKey, nil
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}

		return &k.PublicKey, nil
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}

		signer, ok := k.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("unsupported private key %T", k)
		}

		return signer.Public(), nil
	case "PUBLIC KEY":
		return x509.ParsePKIXPublicKey(block.Bytes)
	case "RSA PUBLIC KEY":
		return x509.ParsePKCS1PublicKey(block.Bytes)
	default:
		return nil, errors.New("not a key")
	}
}
