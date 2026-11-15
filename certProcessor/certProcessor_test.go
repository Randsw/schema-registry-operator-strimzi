package certprocessor

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/go-logr/logr"
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
		t.Error(err)
	}
	if len(truststore) <= 0 {
		t.Error("Empty Truststore")
		return
	}
	if password != "test1234" {
		t.Errorf("Password no equal. Want - %s, got - %s", "test1234", password)
		return
	}
	t.Log("Truststore is created")
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
		t.Error(err)
	}
	if len(keystore) <= 0 {
		t.Error("Empty Keystore")
		return
	}
	if password != "test1234" {
		t.Errorf("Password no equal. Want - %s, got - %s", "test1234", password)
		return
	}
	t.Log("Keystore is created")
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
		t.Error(err)
	}
	if len(keystore) <= 0 {
		t.Error("Empty Keystore. Bad")
		return
	}
	if password != "test1234" {
		t.Errorf("Password no equal. Want - %s, got - %s", "test1234", password)
		return
	}
	t.Log("Keystore is created")
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
		t.Error(err)
	}
	if len(keystore) <= 0 {
		t.Error("Empty Keystore")
		return
	}
	if password != "test1234" {
		t.Errorf("Password no equal. Want - %s, got - %s", "test1234", password)
		return
	}
	t.Log("Keystore is created")
}

// TestConvertPEMToJKS verifies the PEM → JKS conversion used for standard
// kubernetes.io/tls user secrets: the produced JKS is parseable with keytool
// (via KeystoreCertExpiry, which shells out to keytool -list) and the
// returned password unlocks it, while a wrong password does not.
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
		// keytool -list must parse the JKS with the password and expose the
		// certificate; this proves both the format and the password.
		notAfter, err := cp.KeystoreCertExpiry(context.Background(), keystore, password)
		if err != nil {
			t.Fatalf("keytool failed to parse the converted JKS with the password: %v", err)
		}
		if !notAfter.After(time.Now()) {
			t.Errorf("expected a future certificate expiry, got %v", notAfter)
		}
	})

	t.Run("wrong password cannot open the keystore", func(t *testing.T) {
		keystore, _, err := cp.ConvertPEMToJKS(context.Background(), uc.UserCertPEM, uc.UserKeyPEM, "", "test1234")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := cp.KeystoreCertExpiry(context.Background(), keystore, "wrong-password"); err == nil {
			t.Error("expected keytool to fail with a wrong password, got nil")
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
		if _, err := cp.KeystoreCertExpiry(context.Background(), keystore, password); err != nil {
			t.Fatalf("keytool failed to parse the converted JKS with the generated password: %v", err)
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
		_, certs := listStoreEntries(t, cp, writeStoreFile(t, keystore), password)
		if len(certs) != 2 {
			t.Fatalf("expected the keystore to hold the leaf and the CA chain, got %d certs: %s", len(certs), certSubjects(certs))
		}
		got := certRawSet(certs)
		if _, ok := got[string(uc.UserCert.Raw)]; !ok {
			t.Errorf("expected the leaf certificate in the keystore, got %s", certSubjects(certs))
		}
		if _, ok := got[string(ca.CACert.Raw)]; !ok {
			t.Errorf("expected the provided ca.crt certificate in the keystore chain, got %s", certSubjects(certs))
		}
	})

	t.Run("without ca.crt the keystore holds only the leaf", func(t *testing.T) {
		keystore, password, err := cp.ConvertPEMToJKS(context.Background(), uc.UserCertPEM, uc.UserKeyPEM, "", "test1234")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_, certs := listStoreEntries(t, cp, writeStoreFile(t, keystore), password)
		if len(certs) != 1 || !bytes.Equal(certs[0].Raw, uc.UserCert.Raw) {
			t.Errorf("expected the keystore to hold only the leaf certificate, got %d certs: %s", len(certs), certSubjects(certs))
		}
	})

	t.Run("whitespace-only ca.crt is treated as absent", func(t *testing.T) {
		keystore, password, err := cp.ConvertPEMToJKS(context.Background(), uc.UserCertPEM, uc.UserKeyPEM, "  \n\t ", "test1234")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_, certs := listStoreEntries(t, cp, writeStoreFile(t, keystore), password)
		if len(certs) != 1 || !bytes.Equal(certs[0].Raw, uc.UserCert.Raw) {
			t.Errorf("expected the keystore to hold only the leaf certificate, got %d certs: %s", len(certs), certSubjects(certs))
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

// TestKeystoreCertExpiry verifies that KeystoreCertExpiry extracts the
// NotAfter timestamp of the certificate inside a JKS keystore produced by
// GenerateTLSforHTTP. The generated certificate is valid for one year, so
// the extracted expiry must be roughly one year in the future.
func TestKeystoreCertExpiry(t *testing.T) {
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}

	cp := NewCertProcessor(logr.Logger{})
	keystore, password, err := cp.GenerateTLSforHTTP(context.Background(), ca.CACertPEM, ca.CAKeyPEM, "test1234", "confluent-schema-registry", "kafka")
	if err != nil {
		t.Fatalf("failed to generate TLS keystore: %v", err)
	}

	notAfter, err := cp.KeystoreCertExpiry(context.Background(), keystore, password)
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

// listStoreEntries runs keytool -list -rfc on the JKS file at path and
// returns the alias names (as keytool displays them) and the certificates the
// store contains. It is a test helper for asserting the truststore and
// keystore contents produced by CreateTruststore and GenerateTLSforHTTP.
func listStoreEntries(t *testing.T, cp *CertProcessor, path, password string) ([]string, []*x509.Certificate) {
	t.Helper()
	out, err := runCertCommand(context.Background(), cp, keytoolListCmd(context.Background(), path, password))
	if err != nil {
		t.Fatalf("keytool -list failed: %v", err)
	}
	var aliases []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if alias, ok := strings.CutPrefix(line, "Alias name: "); ok {
			aliases = append(aliases, strings.TrimSpace(alias))
		}
	}
	var certs []*x509.Certificate
	rest := out
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = next
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("failed to parse a certificate from the keytool -list output: %v", err)
		}
		certs = append(certs, cert)
	}
	return aliases, certs
}

// writeStoreFile writes JKS bytes to a fresh temporary file and returns its
// path, ready for a keytool -list inspection.
func writeStoreFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.jks")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("failed to write the store temp file: %v", err)
	}
	return path
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

	aliases, certs := listStoreEntries(t, cp, writeStoreFile(t, truststore), password)

	// keytool displays JKS aliases lowercased; compare case-insensitively.
	gotAliases := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		gotAliases[strings.ToLower(alias)] = struct{}{}
	}
	if len(aliases) != 3 {
		t.Errorf("expected 3 truststore entries, got %d (%v)", len(aliases), aliases)
	}
	for _, alias := range []string{"caroot-0", "caroot-1", "caroot-2"} {
		if _, ok := gotAliases[alias]; !ok {
			t.Errorf("expected alias %q in the truststore, got %v", alias, aliases)
		}
	}

	// Every certificate of the bundle must be present in the truststore.
	wantCerts := certRawSet([]*x509.Certificate{caOld.CACert, caNew.CACert, caSpare.CACert})
	gotCerts := certRawSet(certs)
	for raw := range wantCerts {
		if _, ok := gotCerts[raw]; !ok {
			t.Errorf("a bundle CA certificate is missing from the truststore; got %s", certSubjects(certs))
		}
	}
	if len(gotCerts) != len(wantCerts) {
		t.Errorf("truststore contains unexpected certificates: got %d, want %d", len(gotCerts), len(wantCerts))
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

	aliases, certs := listStoreEntries(t, cp, writeStoreFile(t, truststore), password)
	if len(aliases) != 1 {
		t.Fatalf("expected exactly 1 truststore entry, got %d (%v)", len(aliases), aliases)
	}
	if !strings.EqualFold(aliases[0], "CARoot") {
		t.Errorf("expected the single-cert bundle to keep the CARoot alias, got %q", aliases[0])
	}
	if len(certs) != 1 || !bytes.Equal(certs[0].Raw, ca.CACert.Raw) {
		t.Errorf("expected the truststore to hold the CA certificate, got %s", certSubjects(certs))
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

	_, certs := listStoreEntries(t, cp, writeStoreFile(t, keystore), password)
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
