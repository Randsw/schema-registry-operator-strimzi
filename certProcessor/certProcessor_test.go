package certprocessor

import (
	"encoding/base64"
	"testing"
	"unicode"

	"github.com/go-logr/logr"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
)

func TestGeneratePasswordLenght(t *testing.T) {
	expectedLength := 24
	password, err := GeneratePassword(24, true, false)
	if err != nil {
		t.Error(err)
	}

	if len(password) != expectedLength {
		t.Errorf("Output %d not equal to expected %d", len(password), expectedLength)
		return
	}
	t.Log("Password length correct")
}

func TestGeneratePasswordLetter(t *testing.T) {
	result := true
	var badSymbol rune
	password, err := GeneratePassword(12, false, false)
	if err != nil {
		t.Error(err)
	}
	for _, r := range password {
		if !unicode.IsLetter(r) {
			result = false
			badSymbol = r
		}
	}
	if !result {
		t.Errorf("Output has not only letter. Bad- %s", string(badSymbol))
		return
	}
	t.Log("Password consist of letters")
}

func TestGeneratePasswordNumber(t *testing.T) {
	result := true
	var badSymbol rune
	password, err := GeneratePassword(12, true, false)
	if err != nil {
		t.Error(err)
	}
	for _, r := range password {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			result = false
			badSymbol = r
		}
	}
	if !result {
		t.Errorf("Output has not only letter and digits. Bad - %s", string(badSymbol))
		return
	}
	t.Log("Password consist of letters and digits")
}

func TestGeneratePasswordSpecial(t *testing.T) {
	result := false
	password, err := GeneratePassword(12, true, true)
	if err != nil {
		t.Error(err)
	}
	for _, r := range password {
		if !unicode.IsLetter(r) || !unicode.IsDigit(r) {
			result = true
		}
	}
	if !result {
		t.Error("No special symbol in password")
		return
	}
	t.Log("Password has special symbol")
}

func TestCreate_truststore(t *testing.T) {
	// Generate test cluster CA dynamically
	clusterCA, err := testutil.GenerateClusterCACert("STIMZI-SR-TEST")
	if err != nil {
		t.Fatalf("Failed to generate cluster CA: %v", err)
	}

	cp := NewCertProcessor(&logr.Logger{})
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

	cp := NewCertProcessor(&logr.Logger{})
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

	cp := NewCertProcessor(&logr.Logger{})
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

	cp := NewCertProcessor(&logr.Logger{})
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

func TestDecode(t *testing.T) {
	// Generate test CA dynamically and encode/decode to verify Decode_secret_field
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("Failed to generate CA: %v", err)
	}
	base64EncodedCert := base64.StdEncoding.EncodeToString([]byte(ca.CACertPEM))
	// Encode the CA cert to base64 (mimicking the old cacertb64 function)
	decode, err := Decode_secret_field(base64EncodedCert)
	if err != nil {
		t.Error(err)
	}
	if decode != ca.CACertPEM {
		t.Error("Error while decode")
		return
	}
	t.Log("Keystore is Ok")
}
