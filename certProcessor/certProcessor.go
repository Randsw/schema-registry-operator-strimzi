// Utilities for generating the truststore and keystore for the Schema Registry
// based on the cluster's CA cert and the KafkaUser's key.
package certprocessor

import (
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

type CertProcessor struct {
	log logr.Logger
}

func NewCertProcessor(logger logr.Logger) *CertProcessor {
	return &CertProcessor{logger}
}

func GeneratePassword(length int, includeNumber bool, includeSpecial bool) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	var charSource string
	if includeNumber {
		charSource += "0123456789"
	}
	if includeSpecial {
		charSource += "!@#$%^&*()_+=-"
	}
	charSource += charset

	max := big.NewInt(int64(len(charSource)))
	password := make([]byte, length)
	for i := range length {
		b, err := cryptorand.Int(cryptorand.Reader, max)
		if err != nil {
			return "", err
		}
		password[i] = charSource[b.Int64()]
	}
	return string(password), nil
}

// writeTempFile creates a temporary file with the given pattern and content,
// closes it immediately, and returns the file path. The caller is responsible
// for removing the file when it is no longer needed.
func writeTempFile(pattern, content string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file %q: %w", pattern, err)
	}
	name := f.Name()
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("failed to write temp file %q: %w", name, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("failed to close temp file %q: %w", name, err)
	}
	return name, nil
}

// removeIfExists removes a file if it exists, logging a warning on failure.
// It is safe to call on paths that may not exist.
func (cp *CertProcessor) removeIfExists(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		cp.log.V(1).Info("Failed to remove temp file", "path", path, "error", err.Error())
	}
}

// Create a JKS-formatted truststore using the cluster's CA certificate.
// Parameters
//     ----------
//     cert : `string`
//         The content of the Kafka cluster CA certificate. You can get this from
//         a Kubernetes Secret named ``<cluster>-cluster-ca-cert``, and
//         specifially the secret key named ``ca.crt``. See
//         `get_cluster_ca_cert`.

//	Returns
//	-------
//	truststore_content : `bytes`
//	    The content of a JKS truststore containing the cluster CA certificate.
//	password : `str`
//	    The password generated for the truststore.
//
// Notes
// -----
// Internally this function calls out to the “keytool“ command-line tool.
//
// Security: temporary files are removed immediately after they are consumed,
// rather than deferred to function exit. This minimizes the window during which
// sensitive certificate material exists on disk.
func (cp *CertProcessor) CreateTruststore(cert string, password string) ([]byte, string, error) {
	if password == "" {
		var err error
		password, err = GeneratePassword(24, true, false)
		if err != nil {
			cp.log.Error(err, "Failed to generate cryptographically secure random number")
			return nil, "", err
		}
	}

	// Write CA certificate to a temp file; removed immediately after keytool consumes it.
	caCertPath, err := writeTempFile("ca_cert", cert)
	if err != nil {
		cp.log.Error(err, "Failed to write temp file", "File", "ca_cert")
		return nil, "", err
	}
	defer cp.removeIfExists(caCertPath)

	// Truststore output path — use a dedicated subdirectory when available to
	// reduce exposure in shared /tmp. Falls back to os.TempDir().
	outputPath := filepath.Join(os.TempDir(), "client.truststore.jks")
	defer cp.removeIfExists(outputPath)

	// Generate truststore
	cmd := exec.Command("keytool", "-importcert", "-keystore", outputPath, "-alias", "CARoot", "-file",
		caCertPath, "-storepass", password, "-storetype", "jks", "-trustcacerts", "-noprompt")
	out, err := cmd.Output()
	if err != nil {
		cp.log.Error(err, "Error while exec command", "cmdout", string(out))
		return nil, "", err
	}

	// Remove the CA cert temp file now that keytool has consumed it.
	cp.removeIfExists(caCertPath)

	// Check if truststore exists
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		cp.log.Error(err, "File not exist", "File", outputPath)
		return nil, "", err
	}

	// Read truststore from file to save in kubernetes secret
	b, err := os.ReadFile(outputPath)
	if err != nil {
		cp.log.Error(err, "File read fail", "File", outputPath)
		return nil, "", err
	}

	// Remove the truststore output now that we've read it into memory.
	cp.removeIfExists(outputPath)

	return b, password, nil
}

// Create a JKS-formatted keystore using the client's CA certificate,
// certificate, and key.

// Parameters
// ----------
// user_ca_cert : `str`
// 	The content of the KafkaUser's CA certificate. You can get this from
// 	the Kubernetes Secret named after the KafkaUser and specifically the
// 	``ca.crt`` field. See the `get_user_certs` function.
// user_cert : `str`
// 	The content of the KafkaUser's certificate. You can get this from
// 	the Kubernetes Secret named after the KafkaUser and specifically the
// 	``user.crt`` field. See the `get_user_certs` function.
// user_key : `str`
// 	The content of the KafkaUser's private key. You can get this from
// 	the Kubernetes Secret named after the KafkaUser and specifically the
// 	``user.key`` field. See the `get_user_certs` function.

// Returns
// -------
// keytore_content : `bytes`
// 	The content of a JKS keystore.
// password : `str`
// 	Password to protect the output keystore (``keystore_content``)

// Notes
// -----
// Internally this function calls out to the “openssl“ and “keytool“
// command-line tool.
//
// Security: temporary files containing private key material are removed
// immediately after consumption. The p12_path and keystore_path are also
// removed as soon as their content is read into memory.
func (cp *CertProcessor) CreateKeystore(userCACert string, userCert string, userKey string, userp12 string, password string) ([]byte, string, error) {
	var err error

	if password == "" {
		password, err = GeneratePassword(24, true, false)
		if err != nil {
			cp.log.Error(err, "Failed to generate cryptographically secure random number")
			return nil, "", err
		}
	}
	var p12Path string
	tempDir := os.TempDir()

	// Track all temp files for cleanup on error.
	var tempFiles []string
	cleanup := func() {
		for _, f := range tempFiles {
			cp.removeIfExists(f)
		}
	}

	// User data in P12 format not presented — generate from PEM components.
	if userp12 == "" {
		// Write user CA cert, user cert, and user key to temp files.
		userCAPath, err := writeTempFile("user_ca.crt", userCACert)
		if err != nil {
			cp.log.Error(err, "Failed to write temp file", "File", "user_ca.crt")
			return nil, "", err
		}
		tempFiles = append(tempFiles, userCAPath)

		userCertPath, err := writeTempFile("user.crt", userCert)
		if err != nil {
			cp.log.Error(err, "Failed to write temp file", "File", "user.crt")
			cleanup()
			return nil, "", err
		}
		tempFiles = append(tempFiles, userCertPath)

		userKeyPath, err := writeTempFile("user.key", userKey)
		if err != nil {
			cp.log.Error(err, "Failed to write temp file", "File", "user.key")
			cleanup()
			return nil, "", err
		}
		tempFiles = append(tempFiles, userKeyPath)

		p12Path = filepath.Join(tempDir, "user.p12")
		// Generate p12 format bundle
		cmd := exec.Command("openssl", "pkcs12", "-export", "-in", userCertPath, "-inkey", userKeyPath, "-chain", "-CAfile",
			userCAPath, "-name", "confluent-schema-registry", "-passout", "pass:"+password, "-out", p12Path)
		out, err := cmd.Output()
		if err != nil {
			cp.log.Error(err, "Error while exec command", "cmdout", string(out))
			cleanup()
			return nil, "", err
		}

		// Remove the PEM temp files now that openssl has consumed them.
		cleanup()
		tempFiles = nil
	} else {
		// Cert in P12 format is presented — write directly to temp file.
		cp.log.V(1).Info("Using p12 cert store")
		p12Path, err = writeTempFile("user.p12", userp12)
		if err != nil {
			cp.log.Error(err, "Failed to write temp file", "File", "user.p12")
			return nil, "", err
		}
		tempFiles = append(tempFiles, p12Path)
	}
	defer cp.removeIfExists(p12Path)

	// Check if p12 format bundle exists
	if _, err := os.Stat(p12Path); os.IsNotExist(err) {
		cp.log.Error(err, "File not exist", "File", p12Path)
		return nil, "", err
	}

	// Create path to client keystore
	keystorePath := filepath.Join(tempDir, "client.keystore.jks")
	defer cp.removeIfExists(keystorePath)

	// Generate client keystore
	cp.log.V(1).Info("Generate keystore")
	cmd := exec.Command("keytool", "-importkeystore", "-deststorepass", password, "-destkeystore", keystorePath,
		"-deststoretype", "jks", "-srckeystore", p12Path, "-srcstoretype", "PKCS12", "-srcstorepass", password, "-noprompt")
	out, err := cmd.Output()
	if err != nil {
		cp.log.Error(err, "Error while exec command", "cmdout", string(out))
		return nil, "", err
	}

	// Remove p12_path now that keytool has consumed it.
	cp.removeIfExists(p12Path)

	// Check if keystore exists
	if _, err := os.Stat(keystorePath); os.IsNotExist(err) {
		cp.log.Error(err, "File not exist", "File", keystorePath)
		return nil, "", err
	}

	// Read keystore from file to save in kubernetes secret
	b, err := os.ReadFile(keystorePath)
	if err != nil {
		cp.log.Error(err, "File read fail", "File", keystorePath)
		return nil, "", err
	}

	// Remove keystore output now that we've read it into memory.
	cp.removeIfExists(keystorePath)

	return b, password, nil
}

func (cp *CertProcessor) GenerateTLSforHTTP(caCert string, caKey string, password string, cn string) ([]byte, string, error) {
	// Validate CN is not empty
	if cn == "" {
		return nil, "", fmt.Errorf("common name (CN) cannot be empty")
	}
	// Create key, create clr. Sign clr with CACert. Create keystore.
	if password == "" {
		var err error
		password, err = GeneratePassword(24, true, false)
		if err != nil {
			cp.log.Error(err, "Failed to generate cryptographically secure random number")
			return nil, "", err
		}
	}

	tempDir := os.TempDir()

	// Track all temp files for cleanup on error paths.
	var tempFiles []string
	cleanup := func() {
		for _, f := range tempFiles {
			cp.removeIfExists(f)
		}
	}

	// Write cluster CA cert to temp file.
	caCertPath, err := writeTempFile("tls-ca-cert.crt", caCert)
	if err != nil {
		cp.log.Error(err, "Failed to write temp file", "File", "tls-ca-cert.crt")
		return nil, "", err
	}
	tempFiles = append(tempFiles, caCertPath)

	// Generate server private key and CSR
	serverKey, csr, err := cp.generateCSR(cn)
	if err != nil {
		cp.log.Error(err, "Failed to generate CSR")
		cleanup()
		return nil, "", err
	}
	ca, err := StringToCertificate(caCert)
	if err != nil {
		cp.log.Error(err, "Failed to convert from string to x509 certificate")
		cleanup()
		return nil, "", err
	}
	key, err := StringToPrivateKey(caKey)
	if err != nil {
		cp.log.Error(err, "Failed to convert from string to x509 private key")
		cleanup()
		return nil, "", err
	}
	// Sign the CSR with the CA to create a server certificate
	serverCert, err := signCSR(ca, key, csr)
	if err != nil {
		cp.log.Error(err, "Failed to sign CSR")
		cleanup()
		return nil, "", err
	}
	// Save TLS cert and key to files
	keyPath, certPath, err := cp.saveFiles(serverKey, serverCert)
	if err != nil {
		cp.log.Error(err, "Failed to save files")
		cleanup()
		return nil, "", err
	}
	tempFiles = append(tempFiles, keyPath, certPath)

	p12Path := filepath.Join(tempDir, "tls.p12")
	// Generate p12 format bundle
	cmd := exec.Command("openssl", "pkcs12", "-export", "-in", certPath, "-inkey", keyPath, "-chain", "-CAfile",
		caCertPath, "-name", "confluent-schema-registry-tls", "-passout", "pass:"+password, "-out", p12Path)
	out, err := cmd.Output()
	if err != nil {
		cp.log.Error(err, "Error while exec command", "cmdout", string(out))
		cleanup()
		return nil, "", err
	}

	// Remove the PEM key/cert/ca temp files now that openssl has consumed them.
	cleanup()
	tempFiles = nil

	defer cp.removeIfExists(p12Path)

	// Check if p12 format bundle exists
	if _, err := os.Stat(p12Path); os.IsNotExist(err) {
		cp.log.Error(err, "File not exist", "File", p12Path)
		return nil, "", err
	}

	keystorePath := filepath.Join(tempDir, "tls.keystore.jks")
	defer cp.removeIfExists(keystorePath)

	// Generate keystore
	cp.log.V(1).Info("Generate tls keystore")
	cmd = exec.Command("keytool", "-importkeystore", "-deststorepass", password, "-destkeystore", keystorePath,
		"-deststoretype", "jks", "-srckeystore", p12Path, "-srcstoretype", "PKCS12", "-srcstorepass", password, "-noprompt")
	out, err = cmd.Output()
	if err != nil {
		cp.log.Error(err, "Error while exec command", "cmdout", string(out))
		return nil, "", err
	}

	// Remove p12_path now that keytool has consumed it.
	cp.removeIfExists(p12Path)

	// Check if keystore exists
	if _, err := os.Stat(keystorePath); os.IsNotExist(err) {
		cp.log.Error(err, "File not exist", "File", keystorePath)
		return nil, "", err
	}

	// Read keystore from file to save in kubernetes secret
	b, err := os.ReadFile(keystorePath)
	if err != nil {
		cp.log.Error(err, "File read fail", "File", keystorePath)
		return nil, "", err
	}

	// Remove keystore output now that we've read it into memory.
	cp.removeIfExists(keystorePath)

	return b, password, nil
}

func (cp *CertProcessor) generateCSR(cn string) (*rsa.PrivateKey, *x509.CertificateRequest, error) {
	// Generate server private key
	serverKey, err := rsa.GenerateKey(cryptorand.Reader, 4096)
	if err != nil {
		return nil, nil, err
	}
	// Create CSR template
	csrTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{
			Organization: []string{"Schema Registry"},
			CommonName:   strings.Split(cn, ".")[0],
		},
		DNSNames: []string{strings.Split(cn, ".")[0], cn, cn + ".svc", cn + ".svc.cluster", cn + ".svc.cluster.local"},
	}
	// Create CSR
	csrDER, err := x509.CreateCertificateRequest(cryptorand.Reader, csrTemplate, serverKey)
	if err != nil {
		return nil, nil, err
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, nil, err
	}
	return serverKey, csr, nil
}

func signCSR(caCert *x509.Certificate, caKey *rsa.PrivateKey, csr *x509.CertificateRequest) (*x509.Certificate, error) {
	// Generate a random serial number
	serialNumber, err := cryptorand.Int(cryptorand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	// Create server certificate template
	serverTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      csr.Subject,
		DNSNames:     csr.DNSNames,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(1, 0, 0), // 1 year
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	// Sign the server certificate with the CA
	serverCertDER, err := x509.CreateCertificate(cryptorand.Reader, serverTemplate, caCert, csr.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	serverCert, err := x509.ParseCertificate(serverCertDER)
	if err != nil {
		return nil, err
	}
	return serverCert, nil
}

func StringToCertificate(certString string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certString))
	if block == nil {
		return nil, errors.New("failed to parse PEM block containing the certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	return cert, nil
}

func StringToPrivateKey(privateKeyString string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privateKeyString))
	if block == nil {
		return nil, errors.New("failed to parse PEM block containing the private key")
	}
	// Try PKCS1 first
	priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err == nil {
		return priv, nil
	}
	// If PKCS1 fails, try PKCS8
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	switch key := key.(type) {
	case *rsa.PrivateKey:
		return key, nil
	default:
		return nil, errors.New("key type is not RSA private key")
	}
}

func (cp *CertProcessor) saveFiles(serverKey *rsa.PrivateKey, serverCert *x509.Certificate) (string, string, error) {
	// Save server private key — close immediately after writing.
	serverKeyFile, err := os.CreateTemp("", "tls-server.key")
	if err != nil {
		return "", "", err
	}
	keyPath := serverKeyFile.Name()
	if err := pem.Encode(serverKeyFile, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)}); err != nil {
		serverKeyFile.Close()
		os.Remove(keyPath)
		return "", "", err
	}
	if err := serverKeyFile.Close(); err != nil {
		os.Remove(keyPath)
		return "", "", fmt.Errorf("failed to close key file %q: %w", keyPath, err)
	}

	// Save server certificate — close immediately after writing.
	serverCertFile, err := os.CreateTemp("", "tls-server.crt")
	if err != nil {
		os.Remove(keyPath)
		return "", "", err
	}
	certPath := serverCertFile.Name()
	if err := pem.Encode(serverCertFile, &pem.Block{Type: "CERTIFICATE", Bytes: serverCert.Raw}); err != nil {
		serverCertFile.Close()
		os.Remove(certPath)
		os.Remove(keyPath)
		return "", "", err
	}
	if err := serverCertFile.Close(); err != nil {
		os.Remove(certPath)
		os.Remove(keyPath)
		return "", "", fmt.Errorf("failed to close cert file %q: %w", certPath, err)
	}

	return keyPath, certPath, nil
}
