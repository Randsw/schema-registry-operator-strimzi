package testutil

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"software.sslmate.com/src/go-pkcs12"
)

func TestGenerateTestCA(t *testing.T) {
	ca, err := GenerateTestCA()
	if err != nil {
		t.Fatalf("GenerateTestCA() returned error: %v", err)
	}

	if ca.CACertPEM == "" {
		t.Fatal("CACertPEM is empty")
	}
	if ca.CAKeyPEM == "" {
		t.Fatal("CAKeyPEM is empty")
	}
	if ca.CACert == nil {
		t.Fatal("CACert is nil")
	}
	if ca.CAKey == nil {
		t.Fatal("CAKey is nil")
	}

	// Verify the certificate is a valid CA
	if !ca.CACert.IsCA {
		t.Fatal("Generated cert is not marked as CA")
	}
	if ca.CACert.Subject.CommonName != "cluster-ca v0" {
		t.Errorf("Expected CN 'cluster-ca v0', got '%s'", ca.CACert.Subject.CommonName)
	}
	if len(ca.CACert.Subject.Organization) != 1 || ca.CACert.Subject.Organization[0] != "io.strimzi" {
		t.Errorf("Expected Org 'io.strimzi', got '%v'", ca.CACert.Subject.Organization)
	}

	// Verify PEM encoding is valid
	block, _ := pem.Decode([]byte(ca.CACertPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("CACertPEM is not a valid PEM-encoded certificate")
	}
	block, _ = pem.Decode([]byte(ca.CAKeyPEM))
	if block == nil {
		t.Fatal("CAKeyPEM is not a valid PEM-encoded key")
	}
}

func TestGenerateTestUserCert(t *testing.T) {
	ca, err := GenerateTestCA()
	if err != nil {
		t.Fatalf("GenerateTestCA() returned error: %v", err)
	}

	uc, err := GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("GenerateTestUserCert() returned error: %v", err)
	}

	if uc.UserCertPEM == "" {
		t.Fatal("UserCertPEM is empty")
	}
	if uc.UserKeyPEM == "" {
		t.Fatal("UserKeyPEM is empty")
	}
	if uc.UserCert == nil {
		t.Fatal("UserCert is nil")
	}
	if uc.UserKey == nil {
		t.Fatal("UserKey is nil")
	}
	if uc.Password != "test1234" {
		t.Errorf("Expected password 'test1234', got '%s'", uc.Password)
	}

	// Verify the certificate properties
	if uc.UserCert.Subject.CommonName != "confluent-schema-registry" {
		t.Errorf("Expected CN 'confluent-schema-registry', got '%s'", uc.UserCert.Subject.CommonName)
	}
	if len(uc.UserCert.DNSNames) == 0 {
		t.Fatal("UserCert has no DNSNames")
	}

	// Verify the user cert was signed by the CA
	if uc.UserCert.Issuer.CommonName != ca.CACert.Subject.CommonName {
		t.Errorf("User cert issuer CN '%s' doesn't match CA CN '%s'",
			uc.UserCert.Issuer.CommonName, ca.CACert.Subject.CommonName)
	}

	// Verify PEM encoding is valid
	block, _ := pem.Decode([]byte(uc.UserCertPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("UserCertPEM is not a valid PEM-encoded certificate")
	}
	block, _ = pem.Decode([]byte(uc.UserKeyPEM))
	if block == nil {
		t.Fatal("UserKeyPEM is not a valid PEM-encoded key")
	}
}

func TestGenerateTestUserCert_DefaultPassword(t *testing.T) {
	ca, err := GenerateTestCA()
	if err != nil {
		t.Fatalf("GenerateTestCA() returned error: %v", err)
	}

	uc, err := GenerateTestUserCert(ca, "")
	if err != nil {
		t.Fatalf("GenerateTestUserCert() returned error: %v", err)
	}

	if uc.Password != "test1234" {
		t.Errorf("Expected default password 'test1234', got '%s'", uc.Password)
	}
}

func TestGenerateClusterCACert(t *testing.T) {
	clusterName := "STIMZI-SR-TEST"
	ca, err := GenerateClusterCACert(clusterName)
	if err != nil {
		t.Fatalf("GenerateClusterCACert() returned error: %v", err)
	}

	if ca.CACertPEM == "" {
		t.Fatal("CACertPEM is empty")
	}
	if ca.CAKeyPEM == "" {
		t.Fatal("CAKeyPEM is empty")
	}
	if ca.CACert == nil {
		t.Fatal("CACert is nil")
	}
	if ca.CAKey == nil {
		t.Fatal("CAKey is nil")
	}

	// Verify the certificate is a valid CA
	if !ca.CACert.IsCA {
		t.Fatal("Generated cert is not marked as CA")
	}
	if ca.CACert.Subject.CommonName != clusterName {
		t.Errorf("Expected CN '%s', got '%s'", clusterName, ca.CACert.Subject.CommonName)
	}

	// Verify validity period is ~7 years
	expectedNotAfter := ca.CACert.NotBefore.AddDate(7, 0, 0)
	if !ca.CACert.NotAfter.Equal(expectedNotAfter) {
		t.Errorf("Expected validity period to be exactly 7 years. Expected NotAfter: %v, got: %v",
			expectedNotAfter, ca.CACert.NotAfter)
	}
	// Verify PEM encoding is valid
	block, _ := pem.Decode([]byte(ca.CACertPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("CACertPEM is not a valid PEM-encoded certificate")
	}
	block, _ = pem.Decode([]byte(ca.CAKeyPEM))
	if block == nil {
		t.Fatal("CAKeyPEM is not a valid PEM-encoded key")
	}
}

func TestVerifyUserCertWithCA(t *testing.T) {
	ca, err := GenerateTestCA()
	if err != nil {
		t.Fatalf("GenerateTestCA() returned error: %v", err)
	}

	uc, err := GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("GenerateTestUserCert() returned error: %v", err)
	}

	// Create a cert pool with the CA
	roots := x509.NewCertPool()
	roots.AddCert(ca.CACert)

	// Verify the user cert against the CA
	_, err = uc.UserCert.Verify(x509.VerifyOptions{
		Roots: roots,
	})
	if err != nil {
		t.Fatalf("User cert verification failed: %v", err)
	}
}

func TestGenerateTestUserCert_PKCS12Data(t *testing.T) {
	ca, err := GenerateTestCA()
	if err != nil {
		t.Fatalf("GenerateTestCA() returned error: %v", err)
	}

	uc, err := GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("GenerateTestUserCert() returned error: %v", err)
	}

	// Verify PKCS#12 data is non-empty
	if len(uc.PKCS12Data) == 0 {
		t.Fatal("PKCS12Data is empty")
	}

	// Verify PKCS#12 can be decoded with the correct password
	privateKey, userCert, caCerts, err := pkcs12.DecodeChain(uc.PKCS12Data, uc.Password)
	if err != nil {
		t.Fatalf("Failed to decode PKCS#12 bundle: %v", err)
	}

	// Verify private key was extracted and can be cast to RSA
	if privateKey == nil {
		t.Fatal("PKCS#12 decoded private key is nil")
	}
	if _, ok := privateKey.(*rsa.PrivateKey); !ok {
		t.Fatal("PKCS#12 decoded private key is not a valid RSA private key")
	}

	// Verify user certificate was successfully extracted
	if userCert == nil {
		t.Fatal("PKCS#12 decoded user certificate is nil")
	}
	if userCert.Subject.CommonName != "confluent-schema-registry" {
		t.Errorf("Unexpected CommonName in decoded cert: %s", userCert.Subject.CommonName)
	}

	// Verify CA certificates were included in the chain
	if len(caCerts) == 0 {
		t.Fatal("PKCS#12 decoded CA certificates slice is empty")
	}

	if caCerts[0].Subject.CommonName != "cluster-ca v0" {
		t.Errorf("Unexpected CommonName in decoded CA cert: %s", caCerts[0].Subject.CommonName)
	}
}

func TestGenerateTestUserCert_PKCS12WrongPassword(t *testing.T) {
	ca, err := GenerateTestCA()
	if err != nil {
		t.Fatalf("GenerateTestCA() returned error: %v", err)
	}

	uc, err := GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("GenerateTestUserCert() returned error: %v", err)
	}

	// Verify that wrong password fails to decode
	_, _, err = pkcs12.Decode(uc.PKCS12Data, "wrongpassword")
	if err == nil {
		t.Fatal("Expected error when decoding PKCS#12 with wrong password")
	}
}
