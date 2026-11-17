// Utilities for generating the truststore and keystore for the Schema Registry
// based on the cluster's CA cert and the KafkaUser's key.
package certprocessor

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/pavlo-v-chernykh/keystore-go/v4"
	"software.sslmate.com/src/go-pkcs12"
)

// rsaKeyBits is the RSA key size used for the REST API server certificate
// key generated on every renewal (generateCSR). The default is 2048 bits:
// fast enough to generate on every certificate renewal and sufficient for a
// cluster-internal certificate (B18; the previous hardcoded 4096 made each
// renewal noticeably slower). It is a variable so deployments that want a
// larger key can raise it (e.g. 3072 or 4096) and so tests can pin it.
var rsaKeyBits = 2048

// ensureContextAlive returns the caller's context error — wrapped so
// errors.Is still matches context.Canceled / context.DeadlineExceeded — when
// ctx is already cancelled or past its deadline, and nil otherwise. The
// pure-Go certificate building paths check it around their expensive work so
// caller cancellation is honoured the same way the bounded keytool/openssl
// child processes used to be (B5).
func ensureContextAlive(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: certificate processing aborted before it completed", err)
	}
	return nil
}

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

// truststoreAlias returns the alias under which the CA certificate at index
// (of a bundle of total certificates) is imported into the truststore. A
// single-cert bundle keeps the historical "CARoot" alias so existing
// deployments see no change; a multi-cert bundle — the shape Strimzi
// produces during a zero-downtime cluster CA rotation — uses "CARoot-0",
// "CARoot-1", ... in bundle order.
func truststoreAlias(index, total int) string {
	if total == 1 {
		return "CARoot"
	}
	return fmt.Sprintf("CARoot-%d", index)
}

// jksCertificate converts an x509.Certificate into the keystore-go
// Certificate representation stored in JKS entries: the X.509 type tag and
// the DER content.
func jksCertificate(cert *x509.Certificate) keystore.Certificate {
	return keystore.Certificate{Type: "X509", Content: cert.Raw}
}

// encodeJKS serializes store into the JKS binary format, integrity-protected
// with password — the same password a consumer later opens the store with.
func encodeJKS(store keystore.KeyStore, password string) ([]byte, error) {
	var buf bytes.Buffer
	if err := store.Store(&buf, []byte(password)); err != nil {
		return nil, fmt.Errorf("failed to encode the JKS store: %w", err)
	}
	return buf.Bytes(), nil
}

// jksChain converts a certificate chain into the keystore-go representation
// stored in JKS private key entries.
func jksChain(certs []*x509.Certificate) []keystore.Certificate {
	chain := make([]keystore.Certificate, 0, len(certs))
	for _, cert := range certs {
		chain = append(chain, jksCertificate(cert))
	}
	return chain
}

// encodeJKSKeystore encodes a JKS keystore holding a single private key
// entry under alias: the PKCS#8 DER private key (encrypted with password, as
// the JKS format requires) and its certificate chain.
func encodeJKSKeystore(alias string, keyDER []byte, chain []keystore.Certificate, password string) ([]byte, error) {
	store := keystore.New()
	if err := store.SetPrivateKeyEntry(alias, keystore.PrivateKeyEntry{
		CreationTime:     time.Now(),
		PrivateKey:       keyDER,
		CertificateChain: chain,
	}, []byte(password)); err != nil {
		return nil, fmt.Errorf("failed to add the private key entry to the keystore: %w", err)
	}
	return encodeJKS(store, password)
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
// The truststore is encoded entirely in Go with the keystore-go library — no
// keytool/openssl subprocesses and no temporary files are involved, and the
// aliases are stored lowercased exactly as keytool itself writes them.
//
// The cluster CA secret may hold a multi-PEM bundle (Strimzi appends the new
// CA to the old one during a zero-downtime CA rotation). Every CERTIFICATE
// block becomes its own trusted entry — "CARoot" for a single-cert bundle,
// "CARoot-0", "CARoot-1", ... for a multi-cert bundle — because a single
// keytool -importcert call silently kept only the first certificate of a
// multi-PEM file. An empty or unparseable ca.crt is rejected with an
// actionable error before anything is encoded.
//
// ctx is honoured with an explicit cancellation check before the store is
// encoded: a cancelled or expired context aborts the call with the wrapped
// context error (B5).
func (cp *CertProcessor) CreateTruststore(ctx context.Context, cert string, password string) ([]byte, string, error) {
	if password == "" {
		var err error
		password, err = GeneratePassword(24, true, false)
		if err != nil {
			cp.log.Error(err, "Failed to generate cryptographically secure random number")
			return nil, "", err
		}
	}

	// An empty ca.crt would otherwise surface as a cryptic tool failure;
	// reject it up front with an actionable error naming the offending input.
	if strings.TrimSpace(cert) == "" {
		err := errors.New("cluster CA certificate (ca.crt) is empty; the <cluster>-cluster-ca-cert secret must contain a CA certificate to build the truststore")
		cp.log.Error(err, "Empty cluster CA certificate")
		return nil, "", err
	}

	// During a Strimzi zero-downtime CA rotation ca.crt holds the old and the
	// new cluster CA. Parse every CERTIFICATE block (this also rejects
	// unparseable input before anything is encoded) and store each one under
	// its own alias (see truststoreAlias).
	certs, err := StringToCertificates(cert)
	if err != nil {
		err = fmt.Errorf("invalid cluster CA certificate (ca.crt): %w", err)
		cp.log.Error(err, "Failed to parse cluster CA certificate bundle")
		return nil, "", err
	}

	// Honour caller cancellation before the JKS encoding runs (B5).
	if err := ensureContextAlive(ctx); err != nil {
		cp.log.Error(err, "Context cancelled while building the truststore")
		return nil, "", err
	}

	store := keystore.New()
	now := time.Now()
	for i, caCert := range certs {
		if err := store.SetTrustedCertificateEntry(truststoreAlias(i, len(certs)), keystore.TrustedCertificateEntry{
			CreationTime: now,
			Certificate:  jksCertificate(caCert),
		}); err != nil {
			err = fmt.Errorf("failed to add CA certificate #%d to the truststore: %w", i, err)
			cp.log.Error(err, "Failed to build the JKS truststore")
			return nil, "", err
		}
	}

	// Encode the truststore to save in kubernetes secret
	b, err := encodeJKS(store, password)
	if err != nil {
		cp.log.Error(err, "Failed to encode the JKS truststore")
		return nil, "", err
	}

	return b, password, nil
}

// isSelfSigned reports whether cert was issued by itself.
func isSelfSigned(cert *x509.Certificate) bool {
	return bytes.Equal(cert.RawIssuer, cert.RawSubject) && cert.CheckSignatureFrom(cert) == nil
}

// buildCertChain returns the certificate chain that starts at leaf and
// follows the issuers found in pool — matched by subject and signature, so
// two CAs sharing a subject are told apart — up to a self-signed
// certificate. It replicates what openssl pkcs12 -export -chain used to do
// for the keystore pipelines: an incomplete chain (no issuer of the current
// certificate in the pool, or no self-signed root) is an error, so a broken
// ca.crt fails loudly instead of producing a keystore whose chain clients
// cannot validate.
func buildCertChain(leaf *x509.Certificate, pool []*x509.Certificate) ([]*x509.Certificate, error) {
	chain := []*x509.Certificate{leaf}
	current := leaf
	remaining := make([]*x509.Certificate, len(pool))
	copy(remaining, pool)
	for !isSelfSigned(current) {
		issuerIndex := -1
		for i, candidate := range remaining {
			if bytes.Equal(candidate.RawSubject, current.RawIssuer) && current.CheckSignatureFrom(candidate) == nil {
				issuerIndex = i
				break
			}
		}
		if issuerIndex < 0 {
			return nil, fmt.Errorf("unable to build the certificate chain: no issuer certificate for %q found in the provided CA certificate (ca.crt); ensure ca.crt contains the complete issuing chain up to a self-signed root", current.Subject.String())
		}
		current = remaining[issuerIndex]
		remaining = append(remaining[:issuerIndex], remaining[issuerIndex+1:]...)
		chain = append(chain, current)
	}
	return chain, nil
}

// selectLeafForKey returns the certificate of certs whose public key matches
// key — the end-entity certificate openssl pkcs12 -export used to pair with
// the private key — together with the remaining certificates of the bundle.
// When no certificate matches the key it returns the same error the openssl
// pipeline produced, so a mismatched key and certificate still fails the
// call instead of producing a broken keystore.
func selectLeafForKey(certs []*x509.Certificate, key crypto.Signer) (*x509.Certificate, []*x509.Certificate, error) {
	for i, cert := range certs {
		if publicKeyMatches(cert.PublicKey, key.Public()) {
			extras := make([]*x509.Certificate, 0, len(certs)-1)
			extras = append(extras, certs[:i]...)
			extras = append(extras, certs[i+1:]...)
			return cert, extras, nil
		}
	}
	return nil, nil, errors.New("no certificate in the provided certificate bundle matches the provided private key")
}

// keystoreFromPEM builds the JKS bytes for a single private key entry from a
// PEM-encoded certificate bundle and private key, optionally chaining the
// entry with the issuers found in caCerts (nil keeps the entry leaf-only).
// It mirrors the openssl pkcs12 -export pipeline it replaces: the private
// key must match one of the provided certificates, and the issuers must
// form a complete chain up to a self-signed root, so a broken input fails
// instead of producing a keystore whose chain clients cannot validate.
func (cp *CertProcessor) keystoreFromPEM(ctx context.Context, certPEM string, keyPEM string, caCerts []*x509.Certificate, alias string, password string) ([]byte, error) {
	certs, err := StringToCertificates(certPEM)
	if err != nil {
		err = fmt.Errorf("invalid certificate PEM data: %w", err)
		cp.log.Error(err, "Failed to parse the certificate bundle")
		return nil, err
	}
	key, err := StringToPrivateKey(keyPEM)
	if err != nil {
		err = fmt.Errorf("invalid private key PEM data: %w", err)
		cp.log.Error(err, "Failed to parse the private key")
		return nil, err
	}
	leaf, extras, err := selectLeafForKey(certs, key)
	if err != nil {
		cp.log.Error(err, "The private key does not match any provided certificate")
		return nil, err
	}

	chain := append([]*x509.Certificate{leaf}, extras...)
	if len(caCerts) > 0 {
		issuers, err := buildCertChain(leaf, caCerts)
		if err != nil {
			cp.log.Error(err, "Failed to build the certificate chain from the issuing CA certificate")
			return nil, err
		}
		chain = append(chain, issuers[1:]...)
	}

	// Honour caller cancellation before the JKS encoding runs (B5).
	if err := ensureContextAlive(ctx); err != nil {
		cp.log.Error(err, "Context cancelled while building the keystore")
		return nil, err
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		err = fmt.Errorf("failed to encode the private key: %w", err)
		cp.log.Error(err, "Failed to encode the private key")
		return nil, err
	}
	return encodeJKSKeystore(alias, keyDER, jksChain(chain), password)
}

// keystoreFromPKCS12 decodes a PKCS12 bundle — the Strimzi KafkaUser
// user.p12, encrypted with password — and re-encodes it as a JKS keystore
// holding the same private key and certificate chain, mirroring the
// keytool -importkeystore pipeline it replaces.
func (cp *CertProcessor) keystoreFromPKCS12(ctx context.Context, p12 []byte, password string, alias string) ([]byte, error) {
	// Honour caller cancellation before the PKCS12 decoding runs (B5).
	if err := ensureContextAlive(ctx); err != nil {
		cp.log.Error(err, "Context cancelled while building the keystore")
		return nil, err
	}

	key, cert, caCerts, err := pkcs12.DecodeChain(p12, password)
	if err != nil {
		err = fmt.Errorf("failed to decode the PKCS12 keystore (user.p12): %w", err)
		cp.log.Error(err, "Failed to decode the PKCS12 keystore")
		return nil, err
	}

	chain := append([]*x509.Certificate{cert}, caCerts...)

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		err = fmt.Errorf("failed to encode the private key: %w", err)
		cp.log.Error(err, "Failed to encode the private key")
		return nil, err
	}
	return encodeJKSKeystore(alias, keyDER, jksChain(chain), password)
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
// The keystore is encoded entirely in Go: a provided user.p12 is decoded
// with go-pkcs12 and re-encoded as JKS, otherwise the PEM components are
// parsed and encoded directly — no openssl/keytool subprocesses and no
// temporary files are involved.
//
// The PEM path keeps the validations the openssl pipeline used to enforce:
// the private key must match one of the provided certificates, and the user
// CA certificate must contain the complete issuing chain up to a self-signed
// root, so a broken input still fails the reconcile instead of producing a
// keystore whose chain clients cannot validate.
//
// ctx is honoured with explicit cancellation checks before the PKCS12
// decoding and the JKS encoding run (B5).
func (cp *CertProcessor) CreateKeystore(ctx context.Context, userCACert string, userCert string, userKey string, userp12 string, password string) ([]byte, string, error) {
	var err error
	if password == "" {
		password, err = GeneratePassword(24, true, false)
		if err != nil {
			cp.log.Error(err, "Failed to generate cryptographically secure random number")
			return nil, "", err
		}
	}

	var b []byte
	if userp12 == "" {
		// User data in P12 format not presented — build the keystore from the
		// PEM components. The user CA certificate is mandatory on this path:
		// the openssl pipeline always exported with -chain -CAfile, so an
		// empty or unparseable ca.crt failed the call.
		caCerts, parseErr := StringToCertificates(userCACert)
		if parseErr != nil {
			parseErr = fmt.Errorf("invalid user CA certificate (ca.crt): %w", parseErr)
			cp.log.Error(parseErr, "Failed to parse the user CA certificate")
			return nil, "", parseErr
		}
		b, err = cp.keystoreFromPEM(ctx, userCert, userKey, caCerts, "confluent-schema-registry", password)
	} else {
		// Cert in P12 format is presented — decode it and re-encode as JKS.
		cp.log.V(1).Info("Using p12 cert store")
		b, err = cp.keystoreFromPKCS12(ctx, []byte(userp12), password, "confluent-schema-registry")
	}
	if err != nil {
		return nil, "", err
	}

	return b, password, nil
}

// GenerateTLSforHTTP builds a JKS keystore holding a freshly generated
// server certificate (key + CSR signed by the cluster CA) for the Schema
// Registry REST API TLS endpoint.
//
// The cluster CA secret may hold a multi-PEM bundle (Strimzi appends the new
// CA to the old one during a zero-downtime CA rotation). The certificate
// whose public key matches the provided CA private key (ca.key) is used for
// signing, so the issued server certificate always has an issuer clients
// trust. An empty or unparseable ca.crt/ca.key, or a bundle in which no
// certificate matches the key, is rejected with an actionable error.
//
// ctx is honoured with explicit cancellation checks around the RSA key
// generation and the JKS encoding (B5).
func (cp *CertProcessor) GenerateTLSforHTTP(ctx context.Context, caCert string, caKey string, password string, name string, namespace string) ([]byte, string, error) {
	// Validate the DNS identity inputs. name is the CR name — which may
	// itself contain dots (e.g. my.registry) — and namespace the CR
	// namespace; the certificate SANs are built from <name>.<namespace>, so
	// either being empty would produce invalid names.
	if name == "" {
		return nil, "", fmt.Errorf("common name (CN) cannot be empty")
	}
	if namespace == "" {
		return nil, "", fmt.Errorf("namespace cannot be empty; the REST API certificate DNS SANs are built from <name>.<namespace>")
	}
	// Empty CA material would otherwise surface as a cryptic tool failure or
	// an invalid signed certificate; reject it up front with an actionable
	// error naming the offending input.
	if strings.TrimSpace(caCert) == "" {
		err := errors.New("cluster CA certificate (ca.crt) is empty; the <cluster>-cluster-ca-cert secret must contain a CA certificate to sign the REST API certificate")
		cp.log.Error(err, "Empty cluster CA certificate")
		return nil, "", err
	}
	if strings.TrimSpace(caKey) == "" {
		err := errors.New("cluster CA private key (ca.key) is empty; the <cluster>-cluster-ca-key secret must contain the CA private key to sign the REST API certificate")
		cp.log.Error(err, "Empty cluster CA private key")
		return nil, "", err
	}
	// Create key, create csr. Sign csr with CACert. Create keystore.
	if password == "" {
		var err error
		password, err = GeneratePassword(24, true, false)
		if err != nil {
			cp.log.Error(err, "Failed to generate cryptographically secure random number")
			return nil, "", err
		}
	}

	// Honour caller cancellation around the expensive RSA key generation (B5).
	if err := ensureContextAlive(ctx); err != nil {
		cp.log.Error(err, "Context cancelled while generating the REST API certificate")
		return nil, "", err
	}

	// Generate server private key and CSR
	serverKey, csr, err := cp.generateCSR(name, namespace)
	if err != nil {
		cp.log.Error(err, "Failed to generate CSR")
		return nil, "", err
	}
	if err := ensureContextAlive(ctx); err != nil {
		cp.log.Error(err, "Context cancelled while generating the REST API certificate")
		return nil, "", err
	}
	caCerts, err := StringToCertificates(caCert)
	if err != nil {
		err = fmt.Errorf("invalid cluster CA certificate (ca.crt): %w", err)
		cp.log.Error(err, "Failed to parse cluster CA certificate bundle")
		return nil, "", err
	}
	key, err := StringToPrivateKey(caKey)
	if err != nil {
		err = fmt.Errorf("invalid cluster CA private key (ca.key): %w", err)
		cp.log.Error(err, "Failed to convert from string to x509 private key")
		return nil, "", err
	}
	// During a Strimzi CA rotation ca.crt holds the old and the new CA
	// certificate and the active one is not necessarily first; sign with the
	// certificate whose public key matches the provided ca.key, otherwise the
	// issued server certificate would have an issuer clients do not trust.
	ca, err := selectCACertForKey(caCerts, key)
	if err != nil {
		cp.log.Error(err, "Failed to select the CA certificate matching the CA private key")
		return nil, "", err
	}
	// Sign the CSR with the CA to create a server certificate
	serverCert, err := signCSR(ca, key, csr)
	if err != nil {
		cp.log.Error(err, "Failed to sign CSR")
		return nil, "", err
	}

	// The JKS chain mirrors what openssl pkcs12 -export -chain used to
	// produce: the server certificate followed by its issuers from the CA
	// bundle, up to a self-signed root.
	chain, err := buildCertChain(serverCert, caCerts)
	if err != nil {
		cp.log.Error(err, "Failed to build the server certificate chain")
		return nil, "", err
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		err = fmt.Errorf("failed to encode the server private key: %w", err)
		cp.log.Error(err, "Failed to encode the server private key")
		return nil, "", err
	}

	// Encode the keystore to save in kubernetes secret
	cp.log.V(1).Info("Generate tls keystore")
	b, err := encodeJKSKeystore("confluent-schema-registry-tls", keyDER, jksChain(chain), password)
	if err != nil {
		cp.log.Error(err, "Failed to encode the JKS keystore")
		return nil, "", err
	}

	return b, password, nil
}

// ConvertPEMToJKS builds a JKS keystore from a PEM-encoded certificate and
// private key — the shape of a standard kubernetes.io/tls secret (tls.crt /
// tls.key, as issued by cert-manager). The issuing CA certificate (the
// secret's optional ca.crt key) is optional: when provided it must parse and
// its chain is embedded into the keystore, so TLS clients validating against
// that CA succeed even when tls.crt holds only the leaf certificate; when
// absent the keystore holds the leaf certificate alone, exactly as before.
//
// Returns
// -------
// keystore_content : `bytes`
//
//	The content of a JKS keystore holding the certificate and key.
//
// password : `str`
//
//	Password protecting the output keystore (generated when empty).
//
// Notes
// -----
// The keystore is encoded entirely in Go (PEM parsing plus JKS encoding via
// the keystore-go library), mirroring the CreateKeystore pipeline without
// the openssl/keytool subprocesses and temporary files. The validations the
// openssl pipeline used to enforce are kept: the private key must match the
// certificate, and a provided ca.crt must contain the complete issuing
// chain up to a self-signed root.
//
// ctx is honoured with an explicit cancellation check before the JKS
// encoding runs (B5).
func (cp *CertProcessor) ConvertPEMToJKS(ctx context.Context, certPEM string, keyPEM string, caPEM string, password string) ([]byte, string, error) {
	if password == "" {
		var err error
		password, err = GeneratePassword(24, true, false)
		if err != nil {
			cp.log.Error(err, "Failed to generate cryptographically secure random number")
			return nil, "", err
		}
	}

	// The issuing CA is optional: an empty (or whitespace-only) ca.crt keeps
	// the leaf-only keystore of a leaf-only secret. A provided ca.crt must
	// parse before anything is encoded, so a broken bundle fails with an
	// actionable error instead of a cryptic tool failure.
	var caCerts []*x509.Certificate
	if strings.TrimSpace(caPEM) != "" {
		var err error
		caCerts, err = StringToCertificates(caPEM)
		if err != nil {
			err = fmt.Errorf("invalid issuing CA certificate (ca.crt): %w", err)
			cp.log.Error(err, "Failed to parse the issuing CA certificate of the user TLS secret")
			return nil, "", err
		}
	}

	b, err := cp.keystoreFromPEM(ctx, certPEM, keyPEM, caCerts, "confluent-schema-registry", password)
	if err != nil {
		return nil, "", err
	}

	return b, password, nil
}

// KeystoreCertExpiry returns the NotAfter timestamp of the first certificate
// in the given JKS keystore. The keystore is decoded in Go with the same
// keystore-go library that encodes it — no keytool subprocess and no
// temporary file is involved. This works for keystore secrets produced by any
// operator version, so no extra data key is needed in the secret.
//
// The certificates are collected entry by entry in alphabetical alias order:
// a private key entry contributes its certificate chain in chain order (the
// end-entity certificate first), a trusted certificate entry its single
// certificate. The keystores the operator produces hold a single private key
// entry whose chain starts with the REST API server certificate, so the
// returned NotAfter is the server certificate's expiry — the same first PEM
// block the keytool -list -rfc pipeline used to parse.
//
// ctx is honoured with an explicit cancellation check before the keystore is
// decoded (B5).
func (cp *CertProcessor) KeystoreCertExpiry(ctx context.Context, keystoreBytes []byte, password string) (time.Time, error) {
	// Honour caller cancellation before the JKS decoding runs (B5).
	if err := ensureContextAlive(ctx); err != nil {
		cp.log.Error(err, "Context cancelled while reading the keystore certificate expiry")
		return time.Time{}, err
	}

	if len(keystoreBytes) == 0 {
		return time.Time{}, fmt.Errorf("keystore is empty")
	}

	store := keystore.New(keystore.WithOrderedAliases())
	if err := store.Load(bytes.NewReader(keystoreBytes), []byte(password)); err != nil {
		err = fmt.Errorf("failed to decode the JKS keystore: %w", err)
		cp.log.Error(err, "Failed to decode the JKS keystore")
		return time.Time{}, err
	}

	for _, alias := range store.Aliases() {
		var certs []keystore.Certificate
		if store.IsPrivateKeyEntry(alias) {
			chain, err := store.GetPrivateKeyEntryCertificateChain(alias)
			if err != nil {
				err = fmt.Errorf("failed to read the certificate chain of the keystore entry %q: %w", alias, err)
				cp.log.Error(err, "Failed to read the keystore certificate chain")
				return time.Time{}, err
			}
			certs = chain
		} else if store.IsTrustedCertificateEntry(alias) {
			entry, err := store.GetTrustedCertificateEntry(alias)
			if err != nil {
				err = fmt.Errorf("failed to read the keystore entry %q: %w", alias, err)
				cp.log.Error(err, "Failed to read the keystore entry")
				return time.Time{}, err
			}
			certs = []keystore.Certificate{entry.Certificate}
		} else {
			continue
		}
		// The first collected certificate decides: the chain order keeps the
		// end-entity certificate first, so this is the certificate whose
		// expiry drives the controller's renewal window.
		for _, c := range certs {
			cert, err := x509.ParseCertificate(c.Content)
			if err != nil {
				return time.Time{}, fmt.Errorf("failed to parse certificate from keystore: %w", err)
			}
			return cert.NotAfter, nil
		}
	}
	return time.Time{}, fmt.Errorf("no certificate found in the keystore")
}

// maxCommonNameLen is the PKI length limit for the certificate Subject
// CommonName. CR names longer than this are truncated in the CN while the
// DNS SANs keep the full name.
const maxCommonNameLen = 64

// clampCommonName truncates the CSR subject CommonName to the 64-character
// PKI limit; the DNS SANs always keep the full, untruncated name.
func clampCommonName(name string) string {
	if len(name) > maxCommonNameLen {
		return name[:maxCommonNameLen]
	}
	return name
}

// generateCSR creates the REST API server private key and the CSR for it.
// name is the CR name — which may itself contain dots (e.g. my.registry) —
// and namespace the CR namespace; both are passed separately so a dotted CR
// name is never truncated at the first dot. The DNS SANs cover the standard
// Kubernetes service DNS forms built from the full <name>.<namespace>
// service name; the non-standard <name>.<namespace>.svc.cluster entry is not
// included (B22) — it is not a resolvable name and only added noise. The
// Subject CommonName carries the full CR name clamped to the 64-character
// PKI limit.
func (cp *CertProcessor) generateCSR(name, namespace string) (*rsa.PrivateKey, *x509.CertificateRequest, error) {
	// Generate server private key. The key size is the package-level
	// rsaKeyBits (2048 by default, B18) instead of a hardcoded 4096.
	serverKey, err := rsa.GenerateKey(cryptorand.Reader, rsaKeyBits)
	if err != nil {
		return nil, nil, err
	}
	serviceName := name + "." + namespace
	// Create CSR template. The DNS names cover the standard Kubernetes
	// service DNS forms; the non-standard <name>.<namespace>.svc.cluster
	// entry is not included (B22) — it is not a resolvable name and only
	// added noise.
	csrTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{
			Organization: []string{"Schema Registry"},
			CommonName:   clampCommonName(name),
		},
		DNSNames: []string{name, serviceName, serviceName + ".svc", serviceName + ".svc.cluster.local"},
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

// signCSR issues the REST API server certificate from the CSR, signed by the
// provided cluster CA. caKey is the CA private key as a crypto.Signer, so
// both RSA and ECDSA cluster CA keys are supported (x509.CreateCertificate
// picks the signature algorithm from the signer); the CSR's public key may
// be of any algorithm the CA can sign for.
func signCSR(caCert *x509.Certificate, caKey crypto.Signer, csr *x509.CertificateRequest) (*x509.Certificate, error) {
	// Generate a random serial number
	serialNumber, err := cryptorand.Int(cryptorand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	// Create server certificate template. NotBefore carries a 5 minute
	// back-dated skew margin (B9): clients and kubelets with slightly fast
	// clocks would otherwise reject the fresh certificate as not yet valid.
	serverTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      csr.Subject,
		DNSNames:     csr.DNSNames,
		NotBefore:    time.Now().Add(-5 * time.Minute),
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

// StringToCertificates decodes every CERTIFICATE PEM block in certString and
// parses each into an x509.Certificate, preserving bundle order. PEM blocks
// of any other type and any data between blocks are skipped, matching
// pem.Decode's lenient leading-garbage behavior. An error is returned when no
// CERTIFICATE block is present or a certificate block fails to parse; the
// message names the 0-based position of the offending certificate.
func StringToCertificates(certString string) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := []byte(certString)
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
			return nil, fmt.Errorf("failed to parse certificate #%d of the PEM bundle: %w", len(certs), err)
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return nil, errors.New("no CERTIFICATE PEM block found in the provided data")
	}
	return certs, nil
}

// StringToCertificate parses the first CERTIFICATE PEM block of certString.
// For multi-PEM CA bundles use StringToCertificates instead: during a Strimzi
// CA rotation the certificate matching the CA private key is not necessarily
// the first one.
func StringToCertificate(certString string) (*x509.Certificate, error) {
	certs, err := StringToCertificates(certString)
	if err != nil {
		return nil, err
	}
	return certs[0], nil
}

// StringToPrivateKey parses a PEM-encoded private key. RSA keys (PKCS#1
// "RSA PRIVATE KEY" and PKCS#8 "PRIVATE KEY") and ECDSA keys (SEC1
// "EC PRIVATE KEY" and PKCS#8) are supported, so a Strimzi cluster CA
// generated with an ECDSA key can sign the REST API certificate. The
// returned key always implements crypto.Signer.
func StringToPrivateKey(privateKeyString string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(privateKeyString))
	if block == nil {
		return nil, errors.New("failed to parse PEM block containing the private key")
	}
	// Try PKCS1 first (the traditional "RSA PRIVATE KEY" encoding)
	priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err == nil {
		return priv, nil
	}
	// SEC1 "EC PRIVATE KEY" (the traditional OpenSSL EC encoding)
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	// If PKCS1 and SEC1 fail, try PKCS8 (covers RSA and ECDSA keys)
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	switch key := key.(type) {
	case *rsa.PrivateKey:
		return key, nil
	case *ecdsa.PrivateKey:
		return key, nil
	default:
		return nil, errors.New("key type is not RSA or ECDSA private key")
	}
}

// publicKeyMatches reports whether the two public keys are the same key,
// compared via their PKIX DER encodings so every public key algorithm found
// in a CA certificate (RSA and ECDSA) is handled uniformly.
func publicKeyMatches(a, b crypto.PublicKey) bool {
	aDER, err := x509.MarshalPKIXPublicKey(a)
	if err != nil {
		return false
	}
	bDER, err := x509.MarshalPKIXPublicKey(b)
	if err != nil {
		return false
	}
	return bytes.Equal(aDER, bDER)
}

// selectCACertForKey returns the certificate from the ca.crt bundle whose
// public key matches the provided ca.key private key (RSA or ECDSA). During
// a Strimzi CA rotation the bundle holds the old and the new CA certificate
// and the one matching the active private key is not necessarily first;
// signing with a non-matching certificate would issue server certificates
// whose issuer clients do not trust.
func selectCACertForKey(caCerts []*x509.Certificate, caKey crypto.Signer) (*x509.Certificate, error) {
	for _, cert := range caCerts {
		if publicKeyMatches(cert.PublicKey, caKey.Public()) {
			return cert, nil
		}
	}
	return nil, fmt.Errorf("no certificate in the CA bundle (ca.crt) matches the provided CA private key (ca.key); ensure ca.crt and ca.key come from the same CA generation so the REST API certificate is signed by a CA clients trust")
}
