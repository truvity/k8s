package oidc_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/truvity/k8s/pkg/talos/oidc"
)

const issuer = "https://oidc.example.com/clusters/example"

func rsaKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	return k
}

func TestDiscoveryIsWhatTheAPIServerServes(t *testing.T) {
	k := rsaKey(t)

	docs, err := oidc.Generate(issuer, &k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	want := `{"issuer":"https://oidc.example.com/clusters/example",` +
		`"jwks_uri":"https://oidc.example.com/clusters/example/openid/v1/jwks",` +
		`"response_types_supported":["id_token"],"subject_types_supported":["public"],` +
		`"id_token_signing_alg_values_supported":["RS256"]}`
	if string(docs.Discovery) != want {
		t.Errorf("discovery\n got %s\nwant %s", docs.Discovery, want)
	}
}

func TestJWKSKeyIDAndFieldOrder(t *testing.T) {
	k := rsaKey(t)

	docs, err := oidc.Generate(issuer, &k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	sum := sha256.Sum256(der)
	kid := base64.RawURLEncoding.EncodeToString(sum[:])

	prefix := `{"keys":[{"use":"sig","kty":"RSA","kid":"` + kid + `","alg":"RS256","n":"`
	if !strings.HasPrefix(string(docs.JWKS), prefix) || !strings.HasSuffix(string(docs.JWKS), `","e":"AQAB"}]}`) {
		t.Errorf("jwks %s", docs.JWKS)
	}
}

// TestATokenVerifiesAgainstThePublishedKeys signs a token the way the API
// server does and verifies it with nothing but the published key set.
func TestATokenVerifiesAgainstThePublishedKeys(t *testing.T) {
	k := rsaKey(t)

	docs, err := oidc.Generate(issuer, &k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	kid, _ := oidc.KeyID(&k.PublicKey)

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: k}, (&jose.SignerOptions{}).WithHeader("kid", kid).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: issuer, Subject: "system:serviceaccount:a:b", Audience: jwt.Audience{"sts.amazonaws.com"},
		Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}

	var set jose.JSONWebKeySet
	if err := json.Unmarshal(docs.JWKS, &set); err != nil {
		t.Fatal(err)
	}

	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatal(err)
	}

	keys := set.Key(tok.Headers[0].KeyID)
	if len(keys) != 1 {
		t.Fatalf("the published set has no key %s", tok.Headers[0].KeyID)
	}

	var claims jwt.Claims
	if err := tok.Claims(keys[0].Key, &claims); err != nil {
		t.Fatalf("the token does not verify against the published key: %v", err)
	}

	if err := claims.Validate(jwt.Expected{Issuer: issuer, AnyAudience: jwt.Audience{"sts.amazonaws.com"}}); err != nil {
		t.Error(err)
	}
}

func TestRotationPublishesBothKeysAndEveryAlgorithm(t *testing.T) {
	old := rsaKey(t)

	next, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	docs, err := oidc.Generate(issuer, &old.PublicKey, &next.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	var set jose.JSONWebKeySet
	if err := json.Unmarshal(docs.JWKS, &set); err != nil || len(set.Keys) != 2 {
		t.Fatalf("keys %v %v", set.Keys, err)
	}

	if !strings.Contains(string(docs.Discovery), `["ES384","RS256"]`) {
		t.Errorf("discovery %s", docs.Discovery)
	}
}

func TestPublicKeysFromPEM(t *testing.T) {
	r := rsaKey(t)
	e, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalECPrivateKey(e)
	p8, _ := x509.MarshalPKCS8PrivateKey(r)
	pub, _ := x509.MarshalPKIXPublicKey(&e.PublicKey)

	data := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(r)})
	data = append(data, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER})...)
	data = append(data, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8})...)
	data = append(data, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})...)

	keys, err := oidc.PublicKeysFromPEM(data)
	if err != nil || len(keys) != 4 {
		t.Fatalf("keys %d, %v", len(keys), err)
	}

	for i, want := range []crypto.PublicKey{&r.PublicKey, &e.PublicKey, &r.PublicKey, &e.PublicKey} {
		a, _ := oidc.KeyID(keys[i])
		b, _ := oidc.KeyID(want)

		if a != b {
			t.Errorf("key %d is not the expected public key", i)
		}
	}

	if _, err := oidc.PublicKeysFromPEM([]byte("no pem")); err == nil {
		t.Error("no PEM parsed")
	}

	if _, err := oidc.PublicKeysFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}})); err == nil {
		t.Error("a certificate was taken for a key")
	}
}

func TestRefusals(t *testing.T) {
	k := rsaKey(t)

	for name, tc := range map[string]struct {
		issuer string
		keys   []crypto.PublicKey
		want   string
	}{
		"http":     {"http://oidc.example.com", []crypto.PublicKey{&k.PublicKey}, "not an https URL"},
		"slash":    {"https://oidc.example.com/", []crypto.PublicKey{&k.PublicKey}, "trailing slash"},
		"no key":   {issuer, nil, "no key"},
		"repeated": {issuer, []crypto.PublicKey{&k.PublicKey, &k.PublicKey}, "repeats"},
		"type":     {issuer, []crypto.PublicKey{"not a key"}, "unsupported key type"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := oidc.Generate(tc.issuer, tc.keys...); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
