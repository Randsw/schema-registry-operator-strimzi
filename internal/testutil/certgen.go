package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"time"

	"software.sslmate.com/src/go-pkcs12"
	//"golang.org/x/crypto/pkcs12"
)

// TestCA represents a generated test CA with cert and key in PEM format.
type TestCA struct {
	CACertPEM string            // PEM-encoded CA certificate
	CAKeyPEM  string            // PEM-encoded CA private key
	CACert    *x509.Certificate // Parsed CA certificate
	CAKey     *rsa.PrivateKey   // Parsed CA private key
}

// TestUserCert represents a user certificate signed by a CA.
type TestUserCert struct {
	UserCertPEM string            // PEM-encoded user certificate
	UserKeyPEM  string            // PEM-encoded user private key
	UserCert    *x509.Certificate // Parsed user certificate
	UserKey     *rsa.PrivateKey   // Parsed user private key
	PKCS12Data  []byte            // PKCS#12 bundle (cert + key + CA)
	Password    string            // PKCS#12 password
}

const (
	// TestKeySize is the RSA key size for test certificates.
	// 2048 bits is sufficient for testing and significantly faster than 4096.
	TestKeySize = 2048
)

// GenerateTestCA generates a self-signed CA certificate for testing.
//
// The generated CA has:
//   - 2048-bit RSA key (faster than 4096 for tests)
//   - Organization: "io.strimzi"
//   - CommonName: "cluster-ca v0"
//   - Validity: 1 year
//   - CA: true, BasicConstraintsValid: true
//
// Thread-safe: uses crypto/rand which is safe for concurrent use.
func GenerateTestCA() (*TestCA, error) {
	// Generate CA private key
	caKey, err := rsa.GenerateKey(rand.Reader, TestKeySize)
	if err != nil {
		return nil, err
	}

	// Create CA certificate template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	caTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"io.strimzi"},
			CommonName:   "cluster-ca v0",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}

	// Self-sign the CA certificate
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}

	// Parse the generated certificate
	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return nil, err
	}

	// Encode to PEM
	caCertPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caCertDER,
	}))

	caKeyPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(caKey),
	}))

	return &TestCA{
		CACertPEM: caCertPEM,
		CAKeyPEM:  caKeyPEM,
		CACert:    caCert,
		CAKey:     caKey,
	}, nil
}

// GenerateTestUserCert generates a user certificate signed by the provided CA.
//
// The generated user cert has:
//   - 2048-bit RSA key
//   - CommonName: "confluent-schema-registry"
//   - DNSNames: ["confluent-schema-registry", "confluent-schema-registry.svc", ...]
//   - Validity: 1 year
//   - KeyUsage: DigitalSignature | KeyEncipherment
//   - ExtKeyUsage: ServerAuth
//
// The PKCS#12 bundle includes: user cert + user key + CA cert.
func GenerateTestUserCert(ca *TestCA, password string) (*TestUserCert, error) {
	if password == "" {
		password = "test1234"
	}

	// Generate user private key
	userKey, err := rsa.GenerateKey(rand.Reader, TestKeySize)
	if err != nil {
		return nil, err
	}

	// Create user certificate template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	cn := "confluent-schema-registry"
	userTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: cn,
		},
		DNSNames:    []string{cn, cn + ".svc", cn + ".svc.cluster", cn + ".svc.cluster.local"},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().AddDate(1, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	// Sign the user certificate with the CA
	userCertDER, err := x509.CreateCertificate(rand.Reader, userTemplate, ca.CACert, &userKey.PublicKey, ca.CAKey)
	if err != nil {
		return nil, err
	}

	// Parse the generated certificate
	userCert, err := x509.ParseCertificate(userCertDER)
	if err != nil {
		return nil, err
	}

	// Encode to PEM
	userCertPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: userCertDER,
	}))

	userKeyPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(userKey),
	}))

	// Create PKCS#12 bundle containing user cert + user key + CA cert
	pkcs12Data, err := pkcs12.Modern.Encode(
		userKey,
		userCert,
		[]*x509.Certificate{ca.CACert},
		password,
	)
	if err != nil {
		return nil, err
	}

	return &TestUserCert{
		UserCertPEM: userCertPEM,
		UserKeyPEM:  userKeyPEM,
		UserCert:    userCert,
		UserKey:     userKey,
		PKCS12Data:  pkcs12Data,
		Password:    password,
	}, nil
}

// GenerateClusterCACert generates a self-signed cluster CA for Kafka cluster TLS.
//
// The generated cluster CA has:
//   - 2048-bit RSA key
//   - CommonName: <clusterName> (e.g., "STIMZI-SR-TEST")
//   - Validity: 7 years (matching production-like setup)
//   - CA: true, BasicConstraintsValid: true
func GenerateClusterCACert(clusterName string) (*TestCA, error) {
	// Generate CA private key
	caKey, err := rsa.GenerateKey(rand.Reader, TestKeySize)
	if err != nil {
		return nil, err
	}

	// Create CA certificate template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	caTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: clusterName,
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(7, 0, 0), // 7 years like production
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}

	// Self-sign the CA certificate
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}

	// Parse the generated certificate
	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return nil, err
	}

	// Encode to PEM
	caCertPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caCertDER,
	}))

	caKeyPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(caKey),
	}))

	return &TestCA{
		CACertPEM: caCertPEM,
		CAKeyPEM:  caKeyPEM,
		CACert:    caCert,
		CAKey:     caKey,
	}, nil
}
