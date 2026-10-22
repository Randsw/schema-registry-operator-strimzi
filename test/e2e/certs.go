//go:build e2e

package e2e

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// clusterCA holds the Strimzi cluster CA certificate and key fetched from the
// <cluster>-cluster-ca-cert / <cluster>-cluster-ca secrets. The operator signs
// the Schema Registry REST API certificate with this CA and builds the
// client-auth truststore from it, so REST mTLS clients must present a
// certificate signed by the same CA.
type clusterCA struct {
	certPEM []byte
	cert    *x509.Certificate
	key     crypto.Signer
}

// fetchClusterCA reads the cluster CA certificate and key from the Strimzi
// secrets in the scenario namespace.
func fetchClusterCA(ctx context.Context) (*clusterCA, error) {
	certSecret := &corev1.Secret{}
	err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      scenario.ClusterName + "-cluster-ca-cert",
		Namespace: scenario.Namespace,
	}, certSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to get %s-cluster-ca-cert secret: %w", scenario.ClusterName, err)
	}

	keySecret := &corev1.Secret{}
	err = k8sClient.Get(ctx, types.NamespacedName{
		Name:      scenario.ClusterName + "-cluster-ca",
		Namespace: scenario.Namespace,
	}, keySecret)
	if err != nil {
		return nil, fmt.Errorf("failed to get %s-cluster-ca secret: %w", scenario.ClusterName, err)
	}

	certPEM := certSecret.Data["ca.crt"]
	if len(certPEM) == 0 {
		return nil, fmt.Errorf("secret %s-cluster-ca-cert has no ca.crt key", scenario.ClusterName)
	}
	keyPEM := keySecret.Data["ca.key"]
	if len(keyPEM) == 0 {
		return nil, fmt.Errorf("secret %s-cluster-ca has no ca.key key", scenario.ClusterName)
	}

	cert, err := parseCertificate(certPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse cluster CA certificate: %w", err)
	}
	key, err := parsePrivateKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse cluster CA key: %w", err)
	}

	return &clusterCA{certPEM: certPEM, cert: cert, key: key}, nil
}

// certPool returns a certificate pool containing the cluster CA, used to
// verify the Schema Registry REST API server certificate.
func (ca *clusterCA) certPool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	return pool
}

// signClientCert generates a new client certificate signed by the cluster CA
// for use in the REST API mutual TLS (clientauth) phases.
func (ca *clusterCA) signClientCert(commonName string) (tls.Certificate, error) {
	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to generate client key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to generate serial number: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &clientKey.PublicKey, ca.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to sign client certificate: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  clientKey,
	}, nil
}

func parseCertificate(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode certificate PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parsePrivateKey(keyPEM []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode private key PEM block")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if signer, ok := key.(crypto.Signer); ok {
			return signer, nil
		}
		return nil, fmt.Errorf("PKCS#8 private key of unsupported type %T", key)
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("unsupported private key format")
}
