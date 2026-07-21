package certprocessor

import (
	"testing"
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
		password, err := GeneratePassword(12, true, true)
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
