package certprocessor

import (
	"fmt"
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
	truststore, password, err := cp.CreateTruststore(clusterCA.CACertPEM, "test1234")
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
	keystore, password, err := cp.CreateKeystore(ca.CACertPEM, uc.UserCertPEM, uc.UserKeyPEM, "", "test1234")
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
	keystore, password, err := cp.CreateKeystore(ca.CACertPEM, uc.UserCertPEM, uc.UserKeyPEM, string(uc.PKCS12Data), "test1234")
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
	keystore, password, err := cp.GenerateTLSforHTTP(ca.CACertPEM, ca.CAKeyPEM, "test1234", "confluent-schema-registry.kafka")
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
		keystore, password, err := cp.ConvertPEMToJKS(uc.UserCertPEM, uc.UserKeyPEM, "test1234")
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
		notAfter, err := cp.KeystoreCertExpiry(keystore, password)
		if err != nil {
			t.Fatalf("keytool failed to parse the converted JKS with the password: %v", err)
		}
		if !notAfter.After(time.Now()) {
			t.Errorf("expected a future certificate expiry, got %v", notAfter)
		}
	})

	t.Run("wrong password cannot open the keystore", func(t *testing.T) {
		keystore, _, err := cp.ConvertPEMToJKS(uc.UserCertPEM, uc.UserKeyPEM, "test1234")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := cp.KeystoreCertExpiry(keystore, "wrong-password"); err == nil {
			t.Error("expected keytool to fail with a wrong password, got nil")
		}
	})

	t.Run("empty password is auto-generated", func(t *testing.T) {
		keystore, password, err := cp.ConvertPEMToJKS(uc.UserCertPEM, uc.UserKeyPEM, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(password) != 24 {
			t.Errorf("expected a generated 24-character password, got %d characters", len(password))
		}
		if _, err := cp.KeystoreCertExpiry(keystore, password); err != nil {
			t.Fatalf("keytool failed to parse the converted JKS with the generated password: %v", err)
		}
	})

	t.Run("invalid PEM certificate fails", func(t *testing.T) {
		if _, _, err := cp.ConvertPEMToJKS("not a valid PEM certificate", uc.UserKeyPEM, "test1234"); err == nil {
			t.Error("expected error when converting invalid PEM data, got nil")
		}
	})
}

// M7: Edge Case Tests for Certificate Processing

// TestCreateTruststore_InvalidPEM verifies that CreateTruststore returns an error
// when provided with invalid PEM certificate data.
func TestCreateTruststore_InvalidPEM(t *testing.T) {
	cp := NewCertProcessor(logr.Logger{})
	_, _, err := cp.CreateTruststore("not a valid PEM certificate data", "test1234")
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
	keystore, password, err := cp.CreateKeystore(ca.CACertPEM, uc.UserCertPEM, uc.UserKeyPEM, "", "")
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
	keystore, password, err := cp.GenerateTLSforHTTP(ca.CACertPEM, ca.CAKeyPEM, "test1234", "confluent-schema-registry.kafka")
	if err != nil {
		t.Fatalf("failed to generate TLS keystore: %v", err)
	}

	notAfter, err := cp.KeystoreCertExpiry(keystore, password)
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
	if _, err := cp.KeystoreCertExpiry(nil, "test1234"); err == nil {
		t.Error("expected error for empty keystore, got nil")
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

			truststore, _, err := cp.CreateTruststore(ca.CACertPEM, "test1234")
			if err != nil {
				errCh <- fmt.Errorf("CreateTruststore: %w", err)
				return
			}
			if len(truststore) == 0 {
				errCh <- fmt.Errorf("CreateTruststore produced empty truststore")
				return
			}

			keystore, _, err := cp.CreateKeystore(ca.CACertPEM, uc.UserCertPEM, uc.UserKeyPEM, "", "test1234")
			if err != nil {
				errCh <- fmt.Errorf("CreateKeystore: %w", err)
				return
			}
			if len(keystore) == 0 {
				errCh <- fmt.Errorf("CreateKeystore produced empty keystore")
				return
			}

			tlsKeystore, _, err := cp.GenerateTLSforHTTP(ca.CACertPEM, ca.CAKeyPEM, "test1234", "confluent-schema-registry.kafka")
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
	keystore, _, err := cp.GenerateTLSforHTTP(ca.CACertPEM, ca.CAKeyPEM, "test1234", "")
	if err == nil {
		// If no error returned, verify the keystore is empty (indicating failure)
		if len(keystore) == 0 {
			t.Error("GenerateTLSforHTTP with empty CN produced empty keystore without returning an error")
		} else {
			t.Error("expected error when generating TLS with empty CN, got nil")
		}
	}
}
