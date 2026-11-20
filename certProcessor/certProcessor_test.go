package certprocessor

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/go-logr/logr"
	"github.com/pavlo-v-chernykh/keystore-go/v4"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
)

// TestGeneratePassword consolidates all password generation tests into subtests (M6).
func TestGeneratePassword(t *testing.T) {
	t.Run("correct length", func(t *testing.T) {
		expectedLength := 24
		password, err := GeneratePassword(24, true, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(password) != expectedLength {
			t.Errorf("password length = %d, want %d", len(password), expectedLength)
		}
	})

	t.Run("letters only", func(t *testing.T) {
		password, err := GeneratePassword(12, false, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, r := range password {
			if !unicode.IsLetter(r) {
				t.Errorf("password contains non-letter character: %q", r)
			}
		}
	})

	t.Run("letters and digits", func(t *testing.T) {
		password, err := GeneratePassword(12, true, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, r := range password {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				t.Errorf("password contains unexpected character: %q", r)
			}
		}
	})

	t.Run("includes special characters", func(t *testing.T) {
		// Use a longer password (50 chars) to virtually guarantee
		// special characters appear. With a short password (e.g., 12 chars)
		// the random generator may select only letters and digits.
		password, err := GeneratePassword(50, true, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		hasSpecial := false
		for _, r := range password {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				hasSpecial = true
				break
			}
		}
		if !hasSpecial {
			t.Error("password does not contain any special character")
		}
	})
}

func TestCreate_truststore(t *testing.T) {
	// Generate test cluster CA dynamically
	clusterCA, err := testutil.GenerateClusterCACert("STIMZI-SR-TEST")
	if err != nil {
		t.Fatalf("Failed to generate cluster CA: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	truststore, password, err := cp.CreateTruststore(context.Background(), clusterCA.CACertPEM, "test1234")
	if err != nil {
		t.Fatal(err)
	}
	if len(truststore) <= 0 {
		t.Fatal("Empty Truststore")
	}
	if password != "test1234" {
		t.Errorf("Password no equal. Want - %s, got - %s", "test1234", password)
	}
	// The JKS must decode in Go with the returned password and hold the
	// cluster CA under the historical CARoot alias.
	entries := decodeJKS(t, truststore, password)
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 truststore entry, got %d (%s)", len(entries), jksAliases(entries))
	}
	entry := findJKSEntry(entries, "CARoot")
	if entry == nil {
		t.Fatalf("expected the CARoot alias in the truststore, got %s", jksAliases(entries))
	}
	if entry.isKey {
		t.Error("expected a trusted certificate entry, not a private key entry")
	}
	if len(entry.chain) != 1 || !bytes.Equal(entry.chain[0].Raw, clusterCA.CACert.Raw) {
		t.Errorf("expected the truststore to hold the cluster CA certificate, got %s", certSubjects(entry.chain))
	}
	// A wrong password must fail the JKS integrity check.
	if _, err := loadJKS(t, truststore, "wrong-password"); err == nil {
		t.Error("expected the truststore to fail to open with a wrong password, got nil")
	}
}

func TestCreateKeystore(t *testing.T) {
	// Generate test CA and user cert dynamically
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("Failed to generate CA: %v", err)
	}
	uc, err := testutil.GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("Failed to generate user cert: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	keystore, password, err := cp.CreateKeystore(context.Background(), ca.CACertPEM, uc.UserCertPEM, uc.UserKeyPEM, "", "test1234")
	if err != nil {
		t.Fatal(err)
	}
	if len(keystore) <= 0 {
		t.Fatal("Empty Keystore")
	}
	if password != "test1234" {
		t.Errorf("Password no equal. Want - %s, got - %s", "test1234", password)
	}
	// The JKS must decode in Go with the returned password and hold the user
	// certificate and its CA under the confluent-schema-registry alias.
	entries := decodeJKS(t, keystore, password)
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 keystore entry, got %d (%s)", len(entries), jksAliases(entries))
	}
	entry := findJKSEntry(entries, "confluent-schema-registry")
	if entry == nil {
		t.Fatalf("expected the confluent-schema-registry alias in the keystore, got %s", jksAliases(entries))
	}
	if !entry.isKey {
		t.Error("expected a private key entry")
	}
	if len(entry.chain) != 2 {
		t.Fatalf("expected the keystore chain to hold the leaf and the CA, got %d certs: %s", len(entry.chain), certSubjects(entry.chain))
	}
	if !bytes.Equal(entry.chain[0].Raw, uc.UserCert.Raw) {
		t.Errorf("expected the leaf certificate in the keystore, got %s", certSubjects(entry.chain))
	}
	if !bytes.Equal(entry.chain[1].Raw, ca.CACert.Raw) {
		t.Errorf("expected the CA certificate in the keystore chain, got %s", certSubjects(entry.chain))
	}
	assertJKSKeyMatches(t, entry, uc.UserKey.Public())
}

func TestCreateKeystorep12(t *testing.T) {
	// Generate test CA and user cert dynamically
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("Failed to generate CA: %v", err)
	}
	uc, err := testutil.GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("Failed to generate user cert: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	keystore, password, err := cp.CreateKeystore(context.Background(), ca.CACertPEM, uc.UserCertPEM, uc.UserKeyPEM, string(uc.PKCS12Data), "test1234")
	if err != nil {
		t.Fatal(err)
	}
	if len(keystore) <= 0 {
		t.Fatal("Empty Keystore. Bad")
	}
	if password != "test1234" {
		t.Errorf("Password no equal. Want - %s, got - %s", "test1234", password)
	}
	// The pre-built PKCS12 must produce the same JKS shape as the PEM path:
	// one private key entry holding the user certificate and its CA.
	entries := decodeJKS(t, keystore, password)
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 keystore entry, got %d (%s)", len(entries), jksAliases(entries))
	}
	entry := findJKSEntry(entries, "confluent-schema-registry")
	if entry == nil {
		t.Fatalf("expected the confluent-schema-registry alias in the keystore, got %s", jksAliases(entries))
	}
	if !entry.isKey {
		t.Error("expected a private key entry")
	}
	if len(entry.chain) != 2 {
		t.Fatalf("expected the keystore chain to hold the leaf and the CA, got %d certs: %s", len(entry.chain), certSubjects(entry.chain))
	}
	if !bytes.Equal(entry.chain[0].Raw, uc.UserCert.Raw) {
		t.Errorf("expected the leaf certificate in the keystore, got %s", certSubjects(entry.chain))
	}
	if !bytes.Equal(entry.chain[1].Raw, ca.CACert.Raw) {
		t.Errorf("expected the CA certificate in the keystore chain, got %s", certSubjects(entry.chain))
	}
	assertJKSKeyMatches(t, entry, uc.UserKey.Public())
}

func TestCreateTLSKeystore(t *testing.T) {
	// Generate test CA dynamically
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("Failed to generate CA: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	keystore, password, err := cp.GenerateTLSforHTTP(context.Background(), ca.CACertPEM, ca.CAKeyPEM, "test1234", "confluent-schema-registry", "kafka")
	if err != nil {
		t.Fatal(err)
	}
	if len(keystore) <= 0 {
		t.Fatal("Empty Keystore")
	}
	if password != "test1234" {
		t.Errorf("Password no equal. Want - %s, got - %s", "test1234", password)
	}
	// The JKS must decode in Go with the returned password and hold the
	// freshly signed server certificate under the
	// confluent-schema-registry-tls alias.
	entries := decodeJKS(t, keystore, password)
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 keystore entry, got %d (%s)", len(entries), jksAliases(entries))
	}
	entry := findJKSEntry(entries, "confluent-schema-registry-tls")
	if entry == nil {
		t.Fatalf("expected the confluent-schema-registry-tls alias in the keystore, got %s", jksAliases(entries))
	}
	if !entry.isKey {
		t.Error("expected a private key entry")
	}
	if len(entry.chain) != 2 {
		t.Fatalf("expected the keystore chain to hold the server certificate and the CA, got %d certs: %s", len(entry.chain), certSubjects(entry.chain))
	}
	leaf := entry.chain[0]
	if leaf.Subject.CommonName != "confluent-schema-registry" {
		t.Errorf("expected the server certificate CN %q, got %q", "confluent-schema-registry", leaf.Subject.CommonName)
	}
	if err := leaf.CheckSignatureFrom(ca.CACert); err != nil {
		t.Errorf("expected the server certificate to be signed by the cluster CA, but signature verification failed: %v", err)
	}
	if !bytes.Equal(entry.chain[1].Raw, ca.CACert.Raw) {
		t.Errorf("expected the CA certificate in the keystore chain, got %s", certSubjects(entry.chain))
	}
}

// TestConvertPEMToJKS verifies the PEM → JKS conversion used for standard
// kubernetes.io/tls user secrets: the produced JKS decodes in Go with the
// keystore-go library, the returned password unlocks it while a wrong
// password does not, and the entry holds the certificate and its key.
func TestConvertPEMToJKS(t *testing.T) {
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	uc, err := testutil.GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("failed to generate user cert: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})

	t.Run("converts PEM cert and key into a parseable JKS", func(t *testing.T) {
		keystore, password, err := cp.ConvertPEMToJKS(context.Background(), uc.UserCertPEM, uc.UserKeyPEM, "", "test1234")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(keystore) == 0 {
			t.Fatal("empty keystore")
		}
		if password != "test1234" {
			t.Errorf("expected the provided password to be returned, got %q", password)
		}
		entries := decodeJKS(t, keystore, password)
		if len(entries) != 1 {
			t.Fatalf("expected exactly 1 keystore entry, got %d (%s)", len(entries), jksAliases(entries))
		}
		entry := findJKSEntry(entries, "confluent-schema-registry")
		if entry == nil {
			t.Fatalf("expected the confluent-schema-registry alias, got %s", jksAliases(entries))
		}
		if !entry.isKey {
			t.Error("expected a private key entry")
		}
		if len(entry.chain) != 1 || !bytes.Equal(entry.chain[0].Raw, uc.UserCert.Raw) {
			t.Errorf("expected the keystore to hold the leaf certificate, got %s", certSubjects(entry.chain))
		}
		assertJKSKeyMatches(t, entry, uc.UserKey.Public())
	})

	t.Run("wrong password cannot open the keystore", func(t *testing.T) {
		keystore, _, err := cp.ConvertPEMToJKS(context.Background(), uc.UserCertPEM, uc.UserKeyPEM, "", "test1234")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := loadJKS(t, keystore, "wrong-password"); err == nil {
			t.Error("expected the JKS integrity check to fail with a wrong password, got nil")
		}
	})

	t.Run("empty password is auto-generated", func(t *testing.T) {
		keystore, password, err := cp.ConvertPEMToJKS(context.Background(), uc.UserCertPEM, uc.UserKeyPEM, "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(password) != 24 {
			t.Errorf("expected a generated 24-character password, got %d characters", len(password))
		}
		// The generated password must unlock the produced keystore.
		entries := decodeJKS(t, keystore, password)
		if len(entries) != 1 {
			t.Fatalf("expected exactly 1 keystore entry, got %d (%s)", len(entries), jksAliases(entries))
		}
	})

	t.Run("invalid PEM certificate fails", func(t *testing.T) {
		if _, _, err := cp.ConvertPEMToJKS(context.Background(), "not a valid PEM certificate", uc.UserKeyPEM, "", "test1234"); err == nil {
			t.Error("expected error when converting invalid PEM data, got nil")
		}
	})
}

// TestConvertPEMToJKS_WithCACert covers the user-provided issuing CA (the
// optional ca.crt key of a kubernetes.io/tls secret): when present it must be
// embedded into the exported PKCS12 as the chain so the derived keystore
// validates for clients trusting that CA even when tls.crt holds only the
// leaf; when absent the keystore keeps holding only the leaf, exactly as
// before; and an unparseable ca.crt is rejected with an actionable error.
func TestConvertPEMToJKS_WithCACert(t *testing.T) {
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	uc, err := testutil.GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("failed to generate user cert: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})

	t.Run("includes the issuing CA chain in the keystore", func(t *testing.T) {
		keystore, password, err := cp.ConvertPEMToJKS(context.Background(), uc.UserCertPEM, uc.UserKeyPEM, ca.CACertPEM, "test1234")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(keystore) == 0 {
			t.Fatal("empty keystore")
		}
		entries := decodeJKS(t, keystore, password)
		if len(entries) != 1 {
			t.Fatalf("expected exactly 1 keystore entry, got %d (%s)", len(entries), jksAliases(entries))
		}
		if len(entries[0].chain) != 2 {
			t.Fatalf("expected the keystore to hold the leaf and the CA chain, got %d certs: %s", len(entries[0].chain), certSubjects(entries[0].chain))
		}
		got := certRawSet(entries[0].chain)
		if _, ok := got[string(uc.UserCert.Raw)]; !ok {
			t.Errorf("expected the leaf certificate in the keystore, got %s", certSubjects(entries[0].chain))
		}
		if _, ok := got[string(ca.CACert.Raw)]; !ok {
			t.Errorf("expected the provided ca.crt certificate in the keystore chain, got %s", certSubjects(entries[0].chain))
		}
	})

	t.Run("without ca.crt the keystore holds only the leaf", func(t *testing.T) {
		keystore, password, err := cp.ConvertPEMToJKS(context.Background(), uc.UserCertPEM, uc.UserKeyPEM, "", "test1234")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		entries := decodeJKS(t, keystore, password)
		if len(entries) != 1 {
			t.Fatalf("expected exactly 1 keystore entry, got %d (%s)", len(entries), jksAliases(entries))
		}
		if len(entries[0].chain) != 1 || !bytes.Equal(entries[0].chain[0].Raw, uc.UserCert.Raw) {
			t.Errorf("expected the keystore to hold only the leaf certificate, got %s", certSubjects(entries[0].chain))
		}
	})

	t.Run("whitespace-only ca.crt is treated as absent", func(t *testing.T) {
		keystore, password, err := cp.ConvertPEMToJKS(context.Background(), uc.UserCertPEM, uc.UserKeyPEM, "  \n\t ", "test1234")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		entries := decodeJKS(t, keystore, password)
		if len(entries) != 1 {
			t.Fatalf("expected exactly 1 keystore entry, got %d (%s)", len(entries), jksAliases(entries))
		}
		if len(entries[0].chain) != 1 || !bytes.Equal(entries[0].chain[0].Raw, uc.UserCert.Raw) {
			t.Errorf("expected the keystore to hold only the leaf certificate, got %s", certSubjects(entries[0].chain))
		}
	})

	t.Run("invalid ca.crt fails with an actionable error", func(t *testing.T) {
		_, _, err := cp.ConvertPEMToJKS(context.Background(), uc.UserCertPEM, uc.UserKeyPEM, "not a valid PEM certificate", "test1234")
		if err == nil {
			t.Fatal("expected an error for an unparseable ca.crt, got nil")
		}
		if !strings.Contains(err.Error(), "ca.crt") {
			t.Errorf("expected the error to name the offending input ca.crt, got: %v", err)
		}
	})
}

// M7: Edge Case Tests for Certificate Processing

// TestCreateTruststore_InvalidPEM verifies that CreateTruststore returns an error
// when provided with invalid PEM certificate data.
func TestCreateTruststore_InvalidPEM(t *testing.T) {
	cp := NewCertProcessor(logr.Logger{})
	_, _, err := cp.CreateTruststore(context.Background(), "not a valid PEM certificate data", "test1234")
	if err == nil {
		t.Error("expected error when creating truststore with invalid PEM data, got nil")
	}
}

// TestCreateKeystore_EmptyPassword verifies that CreateKeystore properly handles
// an empty password by auto-generating one and still producing a valid keystore.
func TestCreateKeystore_EmptyPassword(t *testing.T) {
	// Generate test CA and user cert dynamically
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	uc, err := testutil.GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("failed to generate user cert: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	keystore, password, err := cp.CreateKeystore(context.Background(), ca.CACertPEM, uc.UserCertPEM, uc.UserKeyPEM, "", "")
	if err != nil {
		t.Fatalf("unexpected error when password is empty (should auto-generate): %v", err)
	}
	if len(keystore) <= 0 {
		t.Error("keystore is empty when password was auto-generated")
	}
	if password == "" {
		t.Error("password should be auto-generated when empty password is provided")
	}
}

// TestCreateKeystore_KeyCertMismatch pins the validation the openssl
// pkcs12 -export pipeline used to enforce: a private key that matches none
// of the provided certificates must fail the call instead of producing a
// keystore whose entry cannot be used.
func TestCreateKeystore_KeyCertMismatch(t *testing.T) {
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	uc, err := testutil.GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("failed to generate user cert: %v", err)
	}
	other, err := testutil.GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("failed to generate a second user cert: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	_, _, err = cp.CreateKeystore(context.Background(), ca.CACertPEM, uc.UserCertPEM, other.UserKeyPEM, "", "test1234")
	if err == nil {
		t.Fatal("expected an error when the private key matches no provided certificate, got nil")
	}
	if !strings.Contains(err.Error(), "matches the provided private key") {
		t.Errorf("expected the error to name the key/certificate mismatch, got: %v", err)
	}
}

// TestCreateKeystore_UnrelatedCA pins the chain validation the openssl
// -chain -CAfile pipeline used to enforce: a user CA certificate that does
// not issue the user certificate must fail the call instead of producing a
// keystore whose chain clients cannot validate.
func TestCreateKeystore_UnrelatedCA(t *testing.T) {
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	other, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate an unrelated CA: %v", err)
	}
	uc, err := testutil.GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("failed to generate user cert: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	_, _, err = cp.CreateKeystore(context.Background(), other.CACertPEM, uc.UserCertPEM, uc.UserKeyPEM, "", "test1234")
	if err == nil {
		t.Fatal("expected an error when the CA certificate does not issue the user certificate, got nil")
	}
	if !strings.Contains(err.Error(), "ca.crt") {
		t.Errorf("expected the error to name the offending input ca.crt, got: %v", err)
	}
}

// TestConvertPEMToJKS_KeyCertMismatch pins the key/certificate match
// validation for the user TLS secret path.
func TestConvertPEMToJKS_KeyCertMismatch(t *testing.T) {
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	uc, err := testutil.GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("failed to generate user cert: %v", err)
	}
	other, err := testutil.GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("failed to generate a second user cert: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	if _, _, err := cp.ConvertPEMToJKS(context.Background(), uc.UserCertPEM, other.UserKeyPEM, "", "test1234"); err == nil {
		t.Fatal("expected an error when the private key matches no provided certificate, got nil")
	}
}

// TestKeystoreCertExpiry verifies that KeystoreCertExpiry decodes a JKS
// keystore in Go and extracts the NotAfter timestamp of its first
// certificate — for the operator-produced keystores that is the end-entity
// certificate of the single private key entry, the certificate whose expiry
// drives the controller's renewal window.
func TestKeystoreCertExpiry(t *testing.T) {
	cp := NewCertProcessor(logr.Logger{})
	ctx := context.Background()

	t.Run("REST API TLS keystore expires in about one year", func(t *testing.T) {
		ca, err := testutil.GenerateTestCA()
		if err != nil {
			t.Fatalf("failed to generate CA: %v", err)
		}
		keystore, password, err := cp.GenerateTLSforHTTP(ctx, ca.CACertPEM, ca.CAKeyPEM, "test1234", "confluent-schema-registry", "kafka")
		if err != nil {
			t.Fatalf("failed to generate TLS keystore: %v", err)
		}

		notAfter, err := cp.KeystoreCertExpiry(ctx, keystore, password)
		if err != nil {
			t.Fatalf("unexpected error reading keystore cert expiry: %v", err)
		}

		now := time.Now()
		if !notAfter.After(now) {
			t.Errorf("expected certificate expiry in the future, got %v", notAfter)
		}
		// The generated certificate is valid for one year; allow some slack.
		if notAfter.Before(now.Add(300*24*time.Hour)) || notAfter.After(now.Add(400*24*time.Hour)) {
			t.Errorf("expected expiry about one year from now, got %v", notAfter)
		}
	})

	t.Run("the leaf certificate of the chain is selected, not an issuer", func(t *testing.T) {
		// A private key entry whose chain holds a short-lived leaf followed by
		// a long-lived issuer: the returned expiry must be the leaf's, exactly
		// like the first PEM block the keytool -list -rfc pipeline used to
		// parse.
		ca, err := testutil.GenerateTestCA()
		if err != nil {
			t.Fatalf("failed to generate CA: %v", err)
		}
		uc, err := testutil.GenerateTestUserCertWithValidity(ca, "test1234", time.Now(), time.Now().AddDate(0, 0, 10))
		if err != nil {
			t.Fatalf("failed to generate user cert: %v", err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(uc.UserKey)
		if err != nil {
			t.Fatalf("failed to encode the private key: %v", err)
		}
		keystore, err := encodeJKSKeystore("confluent-schema-registry", keyDER, jksChain([]*x509.Certificate{uc.UserCert, ca.CACert}), "test1234")
		if err != nil {
			t.Fatalf("failed to encode the keystore: %v", err)
		}

		notAfter, err := cp.KeystoreCertExpiry(ctx, keystore, "test1234")
		if err != nil {
			t.Fatalf("unexpected error reading keystore cert expiry: %v", err)
		}
		if !notAfter.Equal(uc.UserCert.NotAfter) {
			t.Errorf("expected the leaf certificate's NotAfter %v, got %v", uc.UserCert.NotAfter, notAfter)
		}
	})

	t.Run("trusted certificate entries are inspected too", func(t *testing.T) {
		ca, err := testutil.GenerateTestCA()
		if err != nil {
			t.Fatalf("failed to generate CA: %v", err)
		}
		truststore, _, err := cp.CreateTruststore(ctx, ca.CACertPEM, "test1234")
		if err != nil {
			t.Fatalf("failed to build the truststore: %v", err)
		}

		notAfter, err := cp.KeystoreCertExpiry(ctx, truststore, "test1234")
		if err != nil {
			t.Fatalf("unexpected error reading truststore cert expiry: %v", err)
		}
		if !notAfter.Equal(ca.CACert.NotAfter) {
			t.Errorf("expected the CA certificate's NotAfter %v, got %v", ca.CACert.NotAfter, notAfter)
		}
	})

	t.Run("a wrong password fails the JKS integrity check", func(t *testing.T) {
		ca, err := testutil.GenerateTestCA()
		if err != nil {
			t.Fatalf("failed to generate CA: %v", err)
		}
		keystore, password, err := cp.GenerateTLSforHTTP(ctx, ca.CACertPEM, ca.CAKeyPEM, "test1234", "confluent-schema-registry", "kafka")
		if err != nil {
			t.Fatalf("failed to generate TLS keystore: %v", err)
		}

		if _, err := cp.KeystoreCertExpiry(ctx, keystore, password+"x"); err == nil {
			t.Fatal("expected an error for a wrong keystore password, got nil")
		}
	})

	t.Run("unparseable keystore content is rejected", func(t *testing.T) {
		if _, err := cp.KeystoreCertExpiry(ctx, []byte("not a keystore"), "test1234"); err == nil {
			t.Fatal("expected an error for unparseable keystore content, got nil")
		}
	})
}

// TestKeystoreCertExpiry_Empty verifies that KeystoreCertExpiry returns an
// error for an empty keystore.
func TestKeystoreCertExpiry_Empty(t *testing.T) {
	cp := NewCertProcessor(logr.Logger{})
	if _, err := cp.KeystoreCertExpiry(context.Background(), nil, "test1234"); err == nil {
		t.Error("expected error for empty keystore, got nil")
	}
}

// TestGenerateCSRKeyBits covers B18: the RSA key size of the REST API server
// certificate key is the package-level rsaKeyBits variable (2048 by default
// for fast renewals), not a hardcoded 4096, and raising the variable is
// honored.
func TestGenerateCSRKeyBits(t *testing.T) {
	original := rsaKeyBits
	defer func() { rsaKeyBits = original }()

	cp := NewCertProcessor(logr.Logger{})

	t.Run("default key size is 2048 bits", func(t *testing.T) {
		rsaKeyBits = 2048
		key, _, err := cp.generateCSR("schema-registry", "kafka")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bits := key.N.BitLen(); bits != 2048 {
			t.Errorf("expected a 2048-bit key, got %d bits", bits)
		}
	})

	t.Run("key size follows the configurable variable", func(t *testing.T) {
		// A distinct value proves the variable is honored (and documents the
		// supported way to raise the key size).
		rsaKeyBits = 3072
		key, _, err := cp.generateCSR("schema-registry", "kafka")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bits := key.N.BitLen(); bits != 3072 {
			t.Errorf("expected a 3072-bit key after raising rsaKeyBits, got %d bits", bits)
		}
	})
}

// TestGenerateCSRDNSNames covers B22: the CSR DNS names must include the
// standard Kubernetes service DNS forms but not the non-standard
// <cn>.svc.cluster entry, which is not a resolvable name.
func TestGenerateCSRDNSNames(t *testing.T) {
	cp := NewCertProcessor(logr.Logger{})
	_, csr, err := cp.generateCSR("test-sr", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := make(map[string]struct{}, len(csr.DNSNames))
	for _, name := range csr.DNSNames {
		got[name] = struct{}{}
	}
	for _, want := range []string{"test-sr", "test-sr.default", "test-sr.default.svc", "test-sr.default.svc.cluster.local"} {
		if _, ok := got[want]; !ok {
			t.Errorf("expected DNS name %q to be present, got %v", want, csr.DNSNames)
		}
	}
	// B22: the non-standard <cn>.svc.cluster entry must be gone.
	if _, ok := got["test-sr.default.svc.cluster"]; ok {
		t.Errorf("expected the non-standard <cn>.svc.cluster DNS name to be dropped, got %v", csr.DNSNames)
	}
	// The subject CN carries the full CR name (clamped to the PKI limit).
	if csr.Subject.CommonName != "test-sr" {
		t.Errorf("expected the CN to be the full CR name %q, got %q", "test-sr", csr.Subject.CommonName)
	}
}

// TestGenerateCSRDottedName covers the dot-in-CR-name fix: a CR named
// my.registry in namespace ns must get DNS SANs built from the FULL name
// (my.registry, my.registry.ns, ...) and the full name as the subject CN —
// the pre-fix code split the combined name.namespace argument on the first
// dot and produced my everywhere.
func TestGenerateCSRDottedName(t *testing.T) {
	cp := NewCertProcessor(logr.Logger{})
	_, csr, err := cp.generateCSR("my.registry", "ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"my.registry", "my.registry.ns", "my.registry.ns.svc", "my.registry.ns.svc.cluster.local"}
	if !slices.Equal(csr.DNSNames, want) {
		t.Errorf("expected DNS SANs %v, got %v", want, csr.DNSNames)
	}
	if csr.Subject.CommonName != "my.registry" {
		t.Errorf("expected the CN to be the full CR name %q, got %q", "my.registry", csr.Subject.CommonName)
	}
}

// TestGenerateCSRCommonNameClamped covers the 64-character PKI limit of the
// subject CommonName: a CR name longer than 64 characters is truncated in the
// CN while the DNS SANs keep the full, untruncated name.
func TestGenerateCSRCommonNameClamped(t *testing.T) {
	cp := NewCertProcessor(logr.Logger{})
	longName := strings.Repeat("a", 70)
	_, csr, err := cp.generateCSR(longName, "ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if csr.Subject.CommonName != longName[:64] {
		t.Errorf("expected the CN to be clamped to the 64-character prefix %q, got %q", longName[:64], csr.Subject.CommonName)
	}
	want := []string{longName, longName + ".ns", longName + ".ns.svc", longName + ".ns.svc.cluster.local"}
	if !slices.Equal(csr.DNSNames, want) {
		t.Errorf("expected the DNS SANs to keep the full name %v, got %v", want, csr.DNSNames)
	}
}

// TestSignCSRNotBeforeSkewMargin covers B9: the signed REST API certificate
// must carry a NotBefore in the past (a 5 minute back-dated skew margin) so
// clients and kubelets with slightly fast clocks do not reject the fresh
// certificate as not yet valid.
func TestSignCSRNotBeforeSkewMargin(t *testing.T) {
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	_, csr, err := cp.generateCSR("schema-registry", "kafka")
	if err != nil {
		t.Fatalf("failed to generate CSR: %v", err)
	}

	serverCert, err := signCSR(ca.CACert, ca.CAKey, csr)
	if err != nil {
		t.Fatalf("failed to sign CSR: %v", err)
	}

	now := time.Now()
	if !serverCert.NotBefore.Before(now) {
		t.Errorf("expected NotBefore %s to be in the past (skew margin)", serverCert.NotBefore)
	}
	// The margin must be roughly the documented 5 minutes, not days.
	skew := now.Sub(serverCert.NotBefore)
	if skew < 4*time.Minute || skew > 6*time.Minute {
		t.Errorf("expected a ~5 minute skew margin, got %s", skew)
	}
	// The skew margin must not shorten the validity window: NotAfter stays
	// about one year out.
	if !serverCert.NotAfter.After(now.Add(300 * 24 * time.Hour)) {
		t.Errorf("expected NotAfter about one year out, got %v", serverCert.NotAfter)
	}
}

// TestConcurrentCertGeneration verifies that concurrent invocations of the
// certificate generation functions do not interfere with each other. Before
// the per-call temp directory fix, all invocations shared fixed file names in
// /tmp (client.truststore.jks, user.p12, client.keystore.jks, tls.p12,
// tls.keystore.jks), so concurrent reconciles could corrupt each other's
// keystore material.
func TestConcurrentCertGeneration(t *testing.T) {
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	uc, err := testutil.GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("failed to generate user cert: %v", err)
	}

	const workers = 8
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			cp := NewCertProcessor(logr.Logger{})

			truststore, _, err := cp.CreateTruststore(context.Background(), ca.CACertPEM, "test1234")
			if err != nil {
				errCh <- fmt.Errorf("CreateTruststore: %w", err)
				return
			}
			if len(truststore) == 0 {
				errCh <- fmt.Errorf("CreateTruststore produced empty truststore")
				return
			}

			keystore, _, err := cp.CreateKeystore(context.Background(), ca.CACertPEM, uc.UserCertPEM, uc.UserKeyPEM, "", "test1234")
			if err != nil {
				errCh <- fmt.Errorf("CreateKeystore: %w", err)
				return
			}
			if len(keystore) == 0 {
				errCh <- fmt.Errorf("CreateKeystore produced empty keystore")
				return
			}

			tlsKeystore, _, err := cp.GenerateTLSforHTTP(context.Background(), ca.CACertPEM, ca.CAKeyPEM, "test1234", "confluent-schema-registry", "kafka")
			if err != nil {
				errCh <- fmt.Errorf("GenerateTLSforHTTP: %w", err)
				return
			}
			if len(tlsKeystore) == 0 {
				errCh <- fmt.Errorf("GenerateTLSforHTTP produced empty keystore")
				return
			}

			errCh <- nil
		}()
	}

	for i := 0; i < workers; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("concurrent generation failed: %v", err)
		}
	}
}

// TestGenerateTLSforHTTP_EmptyCN verifies that GenerateTLSforHTTP returns an error
// when provided with an empty common name (CN).
// An empty CN results in invalid DNSNames (e.g., ".svc", ".svc.cluster") which
// causes x509.CreateCertificateRequest to fail.
func TestGenerateTLSforHTTP_EmptyCN(t *testing.T) {
	// Generate test CA dynamically
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	keystore, _, err := cp.GenerateTLSforHTTP(context.Background(), ca.CACertPEM, ca.CAKeyPEM, "test1234", "", "default")
	if err == nil {
		// If no error returned, verify the keystore is empty (indicating failure)
		if len(keystore) == 0 {
			t.Error("GenerateTLSforHTTP with empty CN produced empty keystore without returning an error")
		} else {
			t.Error("expected error when generating TLS with empty CN, got nil")
		}
	}
}

// TestGenerateTLSforHTTP_EmptyNamespace verifies that GenerateTLSforHTTP
// rejects an empty namespace with an actionable error: the certificate DNS
// SANs are built from <name>.<namespace>.
func TestGenerateTLSforHTTP_EmptyNamespace(t *testing.T) {
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	keystore, _, err := cp.GenerateTLSforHTTP(context.Background(), ca.CACertPEM, ca.CAKeyPEM, "test1234", "confluent-schema-registry", "")
	if err == nil {
		t.Fatal("expected an error for an empty namespace, got nil")
	}
	if len(keystore) != 0 {
		t.Error("expected no keystore on invalid input")
	}
	if !strings.Contains(err.Error(), "namespace") {
		t.Errorf("expected the error to name the namespace, got: %v", err)
	}
}

// certRawSet maps the DER encodings of certs into a set for comparisons.
func certRawSet(certs []*x509.Certificate) map[string]struct{} {
	set := make(map[string]struct{}, len(certs))
	for _, c := range certs {
		set[string(c.Raw)] = struct{}{}
	}
	return set
}

// certSubjects renders the subjects of certs for test failure messages.
func certSubjects(certs []*x509.Certificate) string {
	names := make([]string, 0, len(certs))
	for _, c := range certs {
		names = append(names, c.Subject.String())
	}
	return strings.Join(names, ", ")
}

// jksEntry is one decoded JKS entry: its alias, whether it is a private key
// entry, the decrypted private key (nil for trusted certificate entries), and
// the certificates it holds — the trusted certificate, or the private key
// certificate chain in chain order.
type jksEntry struct {
	alias string
	isKey bool
	key   crypto.PrivateKey
	chain []*x509.Certificate
}

// loadJKS decodes JKS bytes with password using the keystore-go library — the
// same library the certProcessor encodes with. A wrong password fails the JKS
// integrity check and is returned as an error.
func loadJKS(t *testing.T, data []byte, password string) (*keystore.KeyStore, error) {
	t.Helper()
	store := keystore.New()
	if err := store.Load(bytes.NewReader(data), []byte(password)); err != nil {
		return nil, err
	}
	return &store, nil
}

// decodeJKS decodes JKS bytes with password and returns every entry of the
// store with its parsed certificate chain, failing the test when the store
// cannot be opened with the given password.
func decodeJKS(t *testing.T, data []byte, password string) []jksEntry {
	t.Helper()
	store, err := loadJKS(t, data, password)
	if err != nil {
		t.Fatalf("failed to decode the JKS with the provided password: %v", err)
	}
	var entries []jksEntry
	for _, alias := range store.Aliases() {
		switch {
		case store.IsPrivateKeyEntry(alias):
			pke, err := store.GetPrivateKeyEntry(alias, []byte(password))
			if err != nil {
				t.Fatalf("failed to decrypt the private key entry %q: %v", alias, err)
			}
			key, err := x509.ParsePKCS8PrivateKey(pke.PrivateKey)
			if err != nil {
				t.Fatalf("failed to parse the private key of the entry %q: %v", alias, err)
			}
			entries = append(entries, jksEntry{
				alias: alias,
				isKey: true,
				key:   key,
				chain: parseJKSCertificates(t, pke.CertificateChain),
			})
		case store.IsTrustedCertificateEntry(alias):
			tce, err := store.GetTrustedCertificateEntry(alias)
			if err != nil {
				t.Fatalf("failed to read the trusted certificate entry %q: %v", alias, err)
			}
			entries = append(entries, jksEntry{
				alias: alias,
				chain: parseJKSCertificates(t, []keystore.Certificate{tce.Certificate}),
			})
		default:
			t.Fatalf("entry %q has an unexpected type", alias)
		}
	}
	return entries
}

// parseJKSCertificates parses the DER content of keystore-go Certificates.
func parseJKSCertificates(t *testing.T, certs []keystore.Certificate) []*x509.Certificate {
	t.Helper()
	parsed := make([]*x509.Certificate, 0, len(certs))
	for i, c := range certs {
		cert, err := x509.ParseCertificate(c.Content)
		if err != nil {
			t.Fatalf("failed to parse certificate #%d of a JKS entry: %v", i, err)
		}
		parsed = append(parsed, cert)
	}
	return parsed
}

// findJKSEntry returns the entry with the given alias. JKS aliases are
// case-insensitive and keytool displays them lowercased, so the lookup is
// case-insensitive too.
func findJKSEntry(entries []jksEntry, alias string) *jksEntry {
	for i := range entries {
		if strings.EqualFold(entries[i].alias, alias) {
			return &entries[i]
		}
	}
	return nil
}

// jksAliases renders the aliases of entries for test failure messages.
func jksAliases(entries []jksEntry) string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.alias)
	}
	return strings.Join(names, ", ")
}

// assertJKSKeyMatches fails the test when the decrypted private key of the
// entry does not carry the expected public key.
func assertJKSKeyMatches(t *testing.T, entry *jksEntry, want crypto.PublicKey) {
	t.Helper()
	signer, ok := entry.key.(crypto.Signer)
	if !ok {
		t.Fatalf("the private key of the entry %q does not implement crypto.Signer: %T", entry.alias, entry.key)
	}
	if !publicKeyMatches(signer.Public(), want) {
		t.Errorf("the keystore private key of the entry %q does not match the expected key", entry.alias)
	}
}

// TestStringToCertificates covers the multi-PEM bundle parser used by
// CreateTruststore and GenerateTLSforHTTP: it must decode every CERTIFICATE
// block (Strimzi CA rotation bundles hold the old and the new CA), skip other
// content, and reject empty or unparseable input.
func TestStringToCertificates(t *testing.T) {
	ca1, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA 1: %v", err)
	}
	ca2, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA 2: %v", err)
	}

	t.Run("parses every certificate of a multi-PEM bundle in order", func(t *testing.T) {
		certs, err := StringToCertificates(ca1.CACertPEM + ca2.CACertPEM)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(certs) != 2 {
			t.Fatalf("expected 2 certificates, got %d", len(certs))
		}
		if !bytes.Equal(certs[0].Raw, ca1.CACert.Raw) || !bytes.Equal(certs[1].Raw, ca2.CACert.Raw) {
			t.Error("expected the certificates in bundle order")
		}
	})

	t.Run("parses a single-certificate bundle", func(t *testing.T) {
		certs, err := StringToCertificates(ca1.CACertPEM)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(certs) != 1 || !bytes.Equal(certs[0].Raw, ca1.CACert.Raw) {
			t.Errorf("expected the single CA certificate, got %d certs", len(certs))
		}
	})

	t.Run("skips non-certificate PEM blocks and surrounding data", func(t *testing.T) {
		noise := "leading garbage\n" + string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("not-a-key")})) + "\n"
		certs, err := StringToCertificates(noise + ca1.CACertPEM)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(certs) != 1 || !bytes.Equal(certs[0].Raw, ca1.CACert.Raw) {
			t.Errorf("expected only the CA certificate, got %d certs", len(certs))
		}
	})

	t.Run("errors when no certificate block is present", func(t *testing.T) {
		for _, in := range []string{"", "plain garbage", string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("x")}))} {
			if _, err := StringToCertificates(in); err == nil {
				t.Errorf("expected an error for input %q, got nil", in)
			}
		}
	})

	t.Run("errors on an unparseable certificate block", func(t *testing.T) {
		broken := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk-der")}))
		_, err := StringToCertificates(broken)
		if err == nil {
			t.Fatal("expected an error for an unparseable certificate block, got nil")
		}
		if !strings.Contains(err.Error(), "#0") {
			t.Errorf("expected the error to name the offending certificate position, got: %v", err)
		}
	})

	t.Run("names the position of a malformed later certificate", func(t *testing.T) {
		// A malformed second block must report position #1, not #0: an
		// off-by-one here sends operators looking at the wrong certificate
		// in a rotation bundle.
		broken := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk-der")}))
		_, err := StringToCertificates(ca1.CACertPEM + broken)
		if err == nil {
			t.Fatal("expected an error for a malformed second certificate, got nil")
		}
		if !strings.Contains(err.Error(), "#1") {
			t.Errorf("expected the error to name position #1, got: %v", err)
		}
	})

	t.Run("StringToCertificate returns the first certificate", func(t *testing.T) {
		cert, err := StringToCertificate(ca1.CACertPEM + ca2.CACertPEM)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !bytes.Equal(cert.Raw, ca1.CACert.Raw) {
			t.Error("expected StringToCertificate to return the first bundle certificate")
		}
	})
}

// TestCreateTruststore_MultiCertBundle covers the Strimzi cluster CA rotation
// shape: ca.crt holds several concatenated PEM certificates (old + new CA).
// Every certificate must land in the truststore under its own CARoot-<index>
// alias — a single keytool -importcert call silently keeps only the first
// one, which made clients stop trusting servers signed by the other CA
// during rotation.
func TestCreateTruststore_MultiCertBundle(t *testing.T) {
	caOld, err := testutil.GenerateClusterCACert("rotation-old-ca")
	if err != nil {
		t.Fatalf("failed to generate old CA: %v", err)
	}
	caNew, err := testutil.GenerateClusterCACert("rotation-new-ca")
	if err != nil {
		t.Fatalf("failed to generate new CA: %v", err)
	}
	caSpare, err := testutil.GenerateClusterCACert("rotation-spare-ca")
	if err != nil {
		t.Fatalf("failed to generate spare CA: %v", err)
	}

	bundle := caOld.CACertPEM + caNew.CACertPEM + caSpare.CACertPEM

	cp := NewCertProcessor(logr.Logger{})
	truststore, password, err := cp.CreateTruststore(context.Background(), bundle, "test1234")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(truststore) == 0 {
		t.Fatal("empty truststore")
	}

	entries := decodeJKS(t, truststore, password)
	if len(entries) != 3 {
		t.Errorf("expected 3 truststore entries, got %d (%s)", len(entries), jksAliases(entries))
	}

	// Every certificate of the bundle must be present in the truststore,
	// under its own CARoot-<index> alias (JKS aliases are case-insensitive;
	// they are stored lowercased exactly as keytool writes them).
	var gotCerts []*x509.Certificate
	for _, alias := range []string{"CARoot-0", "CARoot-1", "CARoot-2"} {
		entry := findJKSEntry(entries, alias)
		if entry == nil {
			t.Errorf("expected alias %q in the truststore, got %s", alias, jksAliases(entries))
			continue
		}
		if entry.isKey {
			t.Errorf("alias %q must be a trusted certificate entry, not a private key entry", alias)
		}
		gotCerts = append(gotCerts, entry.chain...)
	}
	wantCerts := certRawSet([]*x509.Certificate{caOld.CACert, caNew.CACert, caSpare.CACert})
	gotSet := certRawSet(gotCerts)
	for raw := range wantCerts {
		if _, ok := gotSet[raw]; !ok {
			t.Errorf("a bundle CA certificate is missing from the truststore; got %s", certSubjects(gotCerts))
		}
	}
	if len(gotSet) != len(wantCerts) {
		t.Errorf("truststore contains unexpected certificates: got %d, want %d", len(gotSet), len(wantCerts))
	}
}

// TestCreateTruststore_SingleCertAliasPreserved pins the alias scheme for the
// common single-cert ca.crt: the certificate is imported under the historical
// "CARoot" alias, so existing deployments see no change.
func TestCreateTruststore_SingleCertAliasPreserved(t *testing.T) {
	ca, err := testutil.GenerateClusterCACert("single-ca")
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	truststore, password, err := cp.CreateTruststore(context.Background(), ca.CACertPEM, "test1234")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entries := decodeJKS(t, truststore, password)
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 truststore entry, got %d (%s)", len(entries), jksAliases(entries))
	}
	if !strings.EqualFold(entries[0].alias, "CARoot") {
		t.Errorf("expected the single-cert bundle to keep the CARoot alias, got %q", entries[0].alias)
	}
	if entries[0].isKey {
		t.Error("expected a trusted certificate entry, not a private key entry")
	}
	if len(entries[0].chain) != 1 || !bytes.Equal(entries[0].chain[0].Raw, ca.CACert.Raw) {
		t.Errorf("expected the truststore to hold the CA certificate, got %s", certSubjects(entries[0].chain))
	}
}

// TestCreateTruststore_EmptyCert verifies that an empty or whitespace-only
// ca.crt is rejected with an actionable error naming the offending input,
// instead of surfacing as a cryptic keytool failure.
func TestCreateTruststore_EmptyCert(t *testing.T) {
	cp := NewCertProcessor(logr.Logger{})
	tests := []struct {
		name string
		cert string
	}{
		{"empty ca.crt", ""},
		{"whitespace-only ca.crt", "  \n\t "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := cp.CreateTruststore(context.Background(), tt.cert, "test1234")
			if err == nil {
				t.Fatal("expected an error for an empty ca.crt, got nil")
			}
			if !strings.Contains(err.Error(), "ca.crt") {
				t.Errorf("expected the error to name the offending input ca.crt, got: %v", err)
			}
		})
	}
}

// TestGenerateTLSforHTTP_SelectsCAMatchingKey covers the CA-selection fix:
// when ca.crt is a multi-PEM bundle whose FIRST certificate does not match
// the provided ca.key but a later one does, the matching CA must be used for
// signing. Before the fix the first certificate was used blindly, producing
// server certificates with an issuer clients do not trust.
func TestGenerateTLSforHTTP_SelectsCAMatchingKey(t *testing.T) {
	caOld, err := testutil.GenerateClusterCACert("rotation-old-ca")
	if err != nil {
		t.Fatalf("failed to generate old CA: %v", err)
	}
	caNew, err := testutil.GenerateClusterCACert("rotation-new-ca")
	if err != nil {
		t.Fatalf("failed to generate new CA: %v", err)
	}

	// The first certificate in the bundle belongs to the old CA, but the
	// provided private key is the new (active) CA's key.
	bundle := caOld.CACertPEM + caNew.CACertPEM

	cp := NewCertProcessor(logr.Logger{})
	keystore, password, err := cp.GenerateTLSforHTTP(context.Background(), bundle, caNew.CAKeyPEM, "test1234", "confluent-schema-registry", "kafka")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keystore) == 0 {
		t.Fatal("empty keystore")
	}

	entries := decodeJKS(t, keystore, password)
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 keystore entry, got %d (%s)", len(entries), jksAliases(entries))
	}
	certs := entries[0].chain
	var leaf *x509.Certificate
	for _, cert := range certs {
		// The REST API server certificate carries the CSR's subject CN; the
		// CA certificates in the keystore have the cluster CA CNs.
		if cert.Subject.CommonName == "confluent-schema-registry" {
			leaf = cert
			break
		}
	}
	if leaf == nil {
		t.Fatalf("server certificate not found in the keystore; entries: %s", certSubjects(certs))
	}

	// The issued certificate must be signed by the CA whose key was provided
	// (the second bundle entry), not by the first bundle entry.
	if err := leaf.CheckSignatureFrom(caNew.CACert); err != nil {
		t.Errorf("expected the server certificate to be signed by the CA matching ca.key, but signature verification against it failed: %v", err)
	}
	if err := leaf.CheckSignatureFrom(caOld.CACert); err == nil {
		t.Error("expected the server certificate NOT to be signed by the first (non-matching) bundle certificate, but it verifies against it")
	}
	if leaf.Issuer.CommonName != "rotation-new-ca" {
		t.Errorf("expected the issuer to be the matching CA %q, got %q", "rotation-new-ca", leaf.Issuer.CommonName)
	}
	// The full chain must validate against the matching CA as root.
	roots := x509.NewCertPool()
	roots.AddCert(caNew.CACert)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots}); err != nil {
		t.Errorf("expected the server certificate chain to validate against the matching CA, got: %v", err)
	}
}

// TestGenerateTLSforHTTP_NoCertMatchesKey verifies the actionable error when
// no certificate of the ca.crt bundle matches the provided ca.key: the
// operator must not silently issue server certificates with an issuer
// clients do not trust.
func TestGenerateTLSforHTTP_NoCertMatchesKey(t *testing.T) {
	caOld, err := testutil.GenerateClusterCACert("rotation-old-ca")
	if err != nil {
		t.Fatalf("failed to generate old CA: %v", err)
	}
	caNew, err := testutil.GenerateClusterCACert("rotation-new-ca")
	if err != nil {
		t.Fatalf("failed to generate new CA: %v", err)
	}
	caOther, err := testutil.GenerateClusterCACert("unrelated-ca")
	if err != nil {
		t.Fatalf("failed to generate unrelated CA: %v", err)
	}

	bundle := caOld.CACertPEM + caNew.CACertPEM

	cp := NewCertProcessor(logr.Logger{})
	keystore, _, err := cp.GenerateTLSforHTTP(context.Background(), bundle, caOther.CAKeyPEM, "test1234", "confluent-schema-registry", "kafka")
	if err == nil {
		t.Fatal("expected an error when no bundle certificate matches ca.key, got nil")
	}
	if len(keystore) != 0 {
		t.Error("expected no keystore when no bundle certificate matches ca.key")
	}
	if !strings.Contains(err.Error(), "ca.crt") || !strings.Contains(err.Error(), "ca.key") {
		t.Errorf("expected an actionable error naming ca.crt and ca.key, got: %v", err)
	}
}

// TestGenerateTLSforHTTP_EmptyInputs verifies that empty, whitespace-only or
// unparseable CA material is rejected with an actionable error naming the
// offending input (ca.crt or ca.key) instead of a cryptic tool failure.
func TestGenerateTLSforHTTP_EmptyInputs(t *testing.T) {
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	tests := []struct {
		name            string
		caCert          string
		caKey           string
		wantErrContains string
	}{
		{"empty ca.crt", "", ca.CAKeyPEM, "ca.crt"},
		{"whitespace-only ca.crt", "  \n\t ", ca.CAKeyPEM, "ca.crt"},
		{"unparseable ca.crt", "not a valid PEM certificate", ca.CAKeyPEM, "ca.crt"},
		{"empty ca.key", ca.CACertPEM, "", "ca.key"},
		{"whitespace-only ca.key", ca.CACertPEM, " \n\t ", "ca.key"},
		{"unparseable ca.key", ca.CACertPEM, "not a valid PEM private key", "ca.key"},
		{"both empty", "", "", "ca.crt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keystore, _, err := cp.GenerateTLSforHTTP(context.Background(), tt.caCert, tt.caKey, "test1234", "confluent-schema-registry", "kafka")
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if len(keystore) != 0 {
				t.Error("expected no keystore on invalid input")
			}
			if !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Errorf("expected the error to name %s, got: %v", tt.wantErrContains, err)
			}
		})
	}
}

// ecdsaTestCA is an ECDSA cluster CA fixture: the certificate plus the
// private key in both PEM encodings an ECDSA Strimzi cluster CA may ship
// (SEC1 "EC PRIVATE KEY" and PKCS#8 "PRIVATE KEY").
type ecdsaTestCA struct {
	CertPEM     string
	SEC1KeyPEM  string
	PKCS8KeyPEM string
	Cert        *x509.Certificate
	Key         *ecdsa.PrivateKey
}

// generateECDSATestCA creates a self-signed ECDSA P-256 cluster CA for the
// ECDSA support tests (the shared testutil helpers only generate RSA CAs).
func generateECDSATestCA(t *testing.T, commonName string) *ecdsaTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		t.Fatalf("failed to generate the ECDSA CA key: %v", err)
	}
	serialNumber, err := cryptorand.Int(cryptorand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("failed to generate the serial number: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"io.strimzi"},
			CommonName:   commonName,
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(cryptorand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("failed to self-sign the ECDSA CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("failed to parse the ECDSA CA certificate: %v", err)
	}
	sec1, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("failed to marshal the ECDSA CA key (SEC1): %v", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("failed to marshal the ECDSA CA key (PKCS#8): %v", err)
	}
	return &ecdsaTestCA{
		CertPEM:     string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		SEC1KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1})),
		PKCS8KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
		Cert:        cert,
		Key:         key,
	}
}

// TestStringToPrivateKey covers the cluster CA private key parser: RSA keys
// in both the PKCS#1 and PKCS#8 encodings keep parsing (unchanged behavior),
// ECDSA keys parse from both the SEC1 "EC PRIVATE KEY" and the PKCS#8
// encoding so an ECDSA Strimzi cluster CA is supported, and garbage,
// non-key PEM or unsupported key types are rejected.
func TestStringToPrivateKey(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate the RSA fixture key: %v", err)
	}
	pkcs1DER := x509.MarshalPKCS1PrivateKey(rsaKey)
	rsaPKCS8DER, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatalf("failed to marshal the RSA fixture key (PKCS#8): %v", err)
	}
	ecCA := generateECDSATestCA(t, "ecdsa-ca")
	_, ed25519Key, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatalf("failed to generate the Ed25519 fixture key: %v", err)
	}
	ed25519DER, err := x509.MarshalPKCS8PrivateKey(ed25519Key)
	if err != nil {
		t.Fatalf("failed to marshal the Ed25519 fixture key: %v", err)
	}

	pkixOf := func(t *testing.T, key crypto.PublicKey) string {
		t.Helper()
		der, err := x509.MarshalPKIXPublicKey(key)
		if err != nil {
			t.Fatalf("failed to marshal the public key: %v", err)
		}
		return string(der)
	}
	rsaPKIX := pkixOf(t, rsaKey.Public())
	ecPKIX := pkixOf(t, ecCA.Key.Public())

	tests := []struct {
		name   string
		keyPEM string
		verify func(t *testing.T, key crypto.Signer)
	}{
		{
			name:   "RSA PKCS#1 (RSA PRIVATE KEY)",
			keyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: pkcs1DER})),
			verify: func(t *testing.T, key crypto.Signer) {
				rsaParsed, ok := key.(*rsa.PrivateKey)
				if !ok {
					t.Fatalf("expected *rsa.PrivateKey, got %T", key)
				}
				if got := pkixOf(t, rsaParsed.Public()); got != rsaPKIX {
					t.Error("parsed RSA key does not match the fixture")
				}
			},
		},
		{
			name:   "RSA PKCS#8 (PRIVATE KEY)",
			keyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rsaPKCS8DER})),
			verify: func(t *testing.T, key crypto.Signer) {
				rsaParsed, ok := key.(*rsa.PrivateKey)
				if !ok {
					t.Fatalf("expected *rsa.PrivateKey, got %T", key)
				}
				if got := pkixOf(t, rsaParsed.Public()); got != rsaPKIX {
					t.Error("parsed RSA key does not match the fixture")
				}
			},
		},
		{
			name:   "ECDSA SEC1 (EC PRIVATE KEY)",
			keyPEM: ecCA.SEC1KeyPEM,
			verify: func(t *testing.T, key crypto.Signer) {
				ecParsed, ok := key.(*ecdsa.PrivateKey)
				if !ok {
					t.Fatalf("expected *ecdsa.PrivateKey, got %T", key)
				}
				if got := pkixOf(t, ecParsed.Public()); got != ecPKIX {
					t.Error("parsed ECDSA key does not match the fixture")
				}
			},
		},
		{
			name:   "ECDSA PKCS#8 (PRIVATE KEY)",
			keyPEM: ecCA.PKCS8KeyPEM,
			verify: func(t *testing.T, key crypto.Signer) {
				ecParsed, ok := key.(*ecdsa.PrivateKey)
				if !ok {
					t.Fatalf("expected *ecdsa.PrivateKey, got %T", key)
				}
				if got := pkixOf(t, ecParsed.Public()); got != ecPKIX {
					t.Error("parsed ECDSA key does not match the fixture")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := StringToPrivateKey(tt.keyPEM)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.verify(t, key)
		})
	}

	t.Run("garbage input is rejected", func(t *testing.T) {
		if _, err := StringToPrivateKey("not a valid PEM private key"); err == nil {
			t.Error("expected an error for garbage input, got nil")
		}
	})
	t.Run("a certificate PEM block is not a private key", func(t *testing.T) {
		if _, err := StringToPrivateKey(ecCA.CertPEM); err == nil {
			t.Error("expected an error for a CERTIFICATE block, got nil")
		}
	})
	t.Run("unsupported PKCS#8 key types are rejected", func(t *testing.T) {
		_, err := StringToPrivateKey(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ed25519DER})))
		if err == nil {
			t.Fatal("expected an error for an Ed25519 PKCS#8 key, got nil")
		}
		if !strings.Contains(err.Error(), "not RSA or ECDSA") {
			t.Errorf("expected the error to name the supported key types, got: %v", err)
		}
	})
}

// TestSignCSRWithECDSACA covers the ECDSA cluster CA signing path: an
// ECDSA CA key must be able to sign the REST API server CSR, producing a
// certificate that verifies against the ECDSA CA and carries the CSR's
// public key. The RSA CA path is asserted unchanged in the same test.
func TestSignCSRWithECDSACA(t *testing.T) {
	ecCA := generateECDSATestCA(t, "ecdsa-cluster-ca")
	rsaCA, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate the RSA CA: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	serverKey, csr, err := cp.generateCSR("schema-registry", "kafka")
	if err != nil {
		t.Fatalf("failed to generate the CSR: %v", err)
	}

	t.Run("an ECDSA CA signs the CSR", func(t *testing.T) {
		serverCert, err := signCSR(ecCA.Cert, ecCA.Key, csr)
		if err != nil {
			t.Fatalf("failed to sign the CSR with the ECDSA CA: %v", err)
		}
		if err := serverCert.CheckSignatureFrom(ecCA.Cert); err != nil {
			t.Errorf("expected the server certificate to be signed by the ECDSA CA, but signature verification failed: %v", err)
		}
		roots := x509.NewCertPool()
		roots.AddCert(ecCA.Cert)
		if _, err := serverCert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "schema-registry"}); err != nil {
			t.Errorf("expected the certificate chain to validate against the ECDSA CA, got: %v", err)
		}
		if !publicKeyMatches(serverCert.PublicKey, serverKey.Public()) {
			t.Error("expected the server certificate to carry the CSR's public key")
		}
	})

	t.Run("the RSA CA path is unchanged", func(t *testing.T) {
		serverCert, err := signCSR(rsaCA.CACert, rsaCA.CAKey, csr)
		if err != nil {
			t.Fatalf("failed to sign the CSR with the RSA CA: %v", err)
		}
		if err := serverCert.CheckSignatureFrom(rsaCA.CACert); err != nil {
			t.Errorf("expected the server certificate to be signed by the RSA CA, but signature verification failed: %v", err)
		}
	})
}

// TestSelectCACertForKeyMismatch covers the RSA CA-cert/key mismatch case
// directly: a bundle that matches none of the provided key must fail with the
// explicit ca.crt/ca.key error instead of silently signing with an untrusted
// issuer. The ECDSA variant (including a mixed bundle) is covered by
// TestSelectCACertForKeyECDSA.
func TestSelectCACertForKeyMismatch(t *testing.T) {
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate the CA: %v", err)
	}
	other, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate the other CA: %v", err)
	}

	certs, err := StringToCertificates(ca.CACertPEM)
	if err != nil {
		t.Fatalf("failed to parse the CA bundle: %v", err)
	}

	selected, err := selectCACertForKey(certs, other.CAKey)
	if err == nil {
		t.Fatal("expected an error when no bundle certificate matches ca.key, got nil")
	}
	if selected != nil {
		t.Errorf("expected no certificate on the mismatch error, got %v", selected)
	}
	if !strings.Contains(err.Error(), "ca.crt") || !strings.Contains(err.Error(), "ca.key") {
		t.Errorf("expected the explicit ca.crt/ca.key mismatch error, got: %v", err)
	}
}

// TestSelectCACertForKeyECDSA covers the CA-cert/key matching with an ECDSA
// key: in a mixed RSA+ECDSA rotation bundle the ECDSA key must select the
// ECDSA certificate, and a non-matching key is still rejected.
func TestSelectCACertForKeyECDSA(t *testing.T) {
	ecCA := generateECDSATestCA(t, "ecdsa-new-ca")
	rsaCA, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate the RSA CA: %v", err)
	}
	otherEC := generateECDSATestCA(t, "other-ecdsa-ca")

	certs, err := StringToCertificates(rsaCA.CACertPEM + ecCA.CertPEM)
	if err != nil {
		t.Fatalf("failed to parse the CA bundle: %v", err)
	}

	selected, err := selectCACertForKey(certs, ecCA.Key)
	if err != nil {
		t.Fatalf("failed to select the CA certificate for the ECDSA key: %v", err)
	}
	if selected.Subject.CommonName != "ecdsa-new-ca" {
		t.Errorf("expected the ECDSA certificate to be selected, got %q", selected.Subject.CommonName)
	}

	if _, err := selectCACertForKey(certs, otherEC.Key); err == nil {
		t.Error("expected an error when no bundle certificate matches the ECDSA key, got nil")
	}
}

// TestGenerateTLSforHTTP_ECDSACA covers the full REST API TLS pipeline with
// an ECDSA Strimzi cluster CA: the SEC1- and PKCS#8-encoded ECDSA CA keys
// must both produce a keystore whose server certificate is signed by the
// ECDSA CA, and a mixed RSA+ECDSA rotation bundle must be signed by the
// ECDSA CA matching the provided key.
func TestGenerateTLSforHTTP_ECDSACA(t *testing.T) {
	ecCA := generateECDSATestCA(t, "ecdsa-cluster-ca")
	rsaOld, err := testutil.GenerateClusterCACert("rotation-old-rsa-ca")
	if err != nil {
		t.Fatalf("failed to generate the old RSA CA: %v", err)
	}

	findLeaf := func(t *testing.T, keystore []byte, password string) *x509.Certificate {
		t.Helper()
		entries := decodeJKS(t, keystore, password)
		var certs []*x509.Certificate
		for _, entry := range entries {
			certs = append(certs, entry.chain...)
		}
		for _, cert := range certs {
			if cert.Subject.CommonName == "confluent-schema-registry" {
				return cert
			}
		}
		t.Fatalf("server certificate not found in the keystore; entries: %s", certSubjects(certs))
		return nil
	}
	verifyAgainst := func(t *testing.T, leaf *x509.Certificate, ca *x509.Certificate) {
		t.Helper()
		if err := leaf.CheckSignatureFrom(ca); err != nil {
			t.Errorf("expected the server certificate to be signed by %q, but signature verification failed: %v", ca.Subject.CommonName, err)
		}
		roots := x509.NewCertPool()
		roots.AddCert(ca)
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots}); err != nil {
			t.Errorf("expected the certificate chain to validate against %q, got: %v", ca.Subject.CommonName, err)
		}
	}

	cp := NewCertProcessor(logr.Logger{})

	t.Run("SEC1-encoded ECDSA CA key", func(t *testing.T) {
		keystore, password, err := cp.GenerateTLSforHTTP(context.Background(), ecCA.CertPEM, ecCA.SEC1KeyPEM, "test1234", "confluent-schema-registry", "kafka")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(keystore) == 0 {
			t.Fatal("empty keystore")
		}
		verifyAgainst(t, findLeaf(t, keystore, password), ecCA.Cert)
	})

	t.Run("PKCS#8-encoded ECDSA CA key", func(t *testing.T) {
		keystore, password, err := cp.GenerateTLSforHTTP(context.Background(), ecCA.CertPEM, ecCA.PKCS8KeyPEM, "test1234", "confluent-schema-registry", "kafka")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(keystore) == 0 {
			t.Fatal("empty keystore")
		}
		verifyAgainst(t, findLeaf(t, keystore, password), ecCA.Cert)
	})

	t.Run("mixed RSA+ECDSA rotation bundle is signed by the matching ECDSA CA", func(t *testing.T) {
		bundle := rsaOld.CACertPEM + ecCA.CertPEM
		keystore, password, err := cp.GenerateTLSforHTTP(context.Background(), bundle, ecCA.SEC1KeyPEM, "test1234", "confluent-schema-registry", "kafka")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(keystore) == 0 {
			t.Fatal("empty keystore")
		}
		leaf := findLeaf(t, keystore, password)
		verifyAgainst(t, leaf, ecCA.Cert)
		if leaf.Issuer.CommonName != "ecdsa-cluster-ca" {
			t.Errorf("expected the issuer to be the matching ECDSA CA, got %q", leaf.Issuer.CommonName)
		}
		if err := leaf.CheckSignatureFrom(rsaOld.CACert); err == nil {
			t.Error("expected the server certificate NOT to be signed by the non-matching RSA CA, but it verifies against it")
		}
	})
}

// TestCreateKeystore_CorruptPKCS12 guards the p12 decode error branches: a
// corrupted p12 bundle and a bundle sealed with a different password must
// both fail with an error naming the offending input (user.p12) instead of
// producing a partial or unusable keystore.
func TestCreateKeystore_CorruptPKCS12(t *testing.T) {
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	uc, err := testutil.GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("failed to generate user cert: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})

	t.Run("corrupted p12 bytes are rejected", func(t *testing.T) {
		corrupted := append([]byte(nil), uc.PKCS12Data...)
		// Flip bytes deep inside the bundle so the ASN.1 structure breaks
		// while the file keeps a plausible size.
		for i := 100; i < len(corrupted); i += 97 {
			corrupted[i] ^= 0xFF
		}
		keystore, _, err := cp.CreateKeystore(context.Background(), ca.CACertPEM, uc.UserCertPEM, uc.UserKeyPEM, string(corrupted), "test1234")
		if err == nil {
			t.Fatal("expected an error for corrupted p12 bytes, got nil")
		}
		if len(keystore) != 0 {
			t.Error("expected no keystore output on the p12 decode failure")
		}
		if !strings.Contains(err.Error(), "user.p12") {
			t.Errorf("expected the error to name the offending input user.p12, got: %v", err)
		}
	})

	t.Run("p12 sealed with a wrong password is rejected", func(t *testing.T) {
		keystore, _, err := cp.CreateKeystore(context.Background(), ca.CACertPEM, uc.UserCertPEM, uc.UserKeyPEM, string(uc.PKCS12Data), "wrong-password")
		if err == nil {
			t.Fatal("expected an error for a p12 opened with the wrong password, got nil")
		}
		if len(keystore) != 0 {
			t.Error("expected no keystore output on the p12 decode failure")
		}
		if !strings.Contains(err.Error(), "user.p12") {
			t.Errorf("expected the error to name the offending input user.p12, got: %v", err)
		}
	})
}

// TestCreateKeystore_EmptyPassword_PKCS12 pins the p12-path password
// contract: the p12 bundle is encrypted with its own password, so an empty
// password cannot be replaced by a generated one (the generated value would
// be used to DECODE the p12 and never opens it). The call fails with the
// user.p12 decode error; the operator's KafkaUser-secret validation rejects
// this combination up front for exactly this reason (k8s.go, the
// clientp12-without-user.password guard), so the contract here is the
// library-level safety net.
func TestCreateKeystore_EmptyPassword_PKCS12(t *testing.T) {
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	uc, err := testutil.GenerateTestUserCert(ca, "p12-source-password")
	if err != nil {
		t.Fatalf("failed to generate user cert: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	keystore, _, err := cp.CreateKeystore(context.Background(), ca.CACertPEM, uc.UserCertPEM, uc.UserKeyPEM, string(uc.PKCS12Data), "")
	if err == nil {
		t.Fatal("expected an error for a p12 with an empty password (the generated password cannot decode the p12), got nil")
	}
	if len(keystore) != 0 {
		t.Error("expected no keystore output on the p12 decode failure")
	}
	if !strings.Contains(err.Error(), "user.p12") {
		t.Errorf("expected the error to name the offending input user.p12, got: %v", err)
	}
}

// TestKeystoreCertExpiry_StructurallyValidNonJKS verifies that
// KeystoreCertExpiry rejects structurally valid data that is not a JKS
// keystore (here: a valid PEM certificate) with an error instead of
// panicking — the caller logs and skips, so a panic would crash the
// reconciliation worker.
func TestKeystoreCertExpiry_StructurallyValidNonJKS(t *testing.T) {
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	if _, err := cp.KeystoreCertExpiry(context.Background(), []byte(ca.CACertPEM), "test1234"); err == nil {
		t.Fatal("expected an error for PEM bytes that are not a JKS keystore, got nil")
	}
}
