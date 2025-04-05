// Utilities for generating the truststore and keystore for the Schema Registry
// based on the cluster's CA cert and the KafkaUser's key.
package certprocessor

import (
	"encoding/base64"
	"math/rand"
	"os"
	"os/exec"
	"time"

	"github.com/go-logr/logr"
)

func GeneratePassword(length int, includeNumber bool, includeSpecial bool) string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	var password []byte
	var charSource string

	if includeNumber {
		charSource += "0123456789"
	}
	if includeSpecial {
		charSource += "!@#$%^&*()_+=-"
	}
	charSource += charset

	for i := 0; i < length; i++ {
		randNum := r.Intn(len(charSource))
		password = append(password, charSource[randNum])
	}
	return string(password)
}

///Create a JKS-formatted truststore using the cluster's CA certificate.
// Parameters
//     ----------
//     cert : `string`
//         The content of the Kafka cluster CA certificate. You can get this from
//         a Kubernetes Secret named ``<cluster>-cluster-ca-cert``, and
//         specifially the secret key named ``ca.crt``. See
//         `get_cluster_ca_cert`.

//     Returns
//     -------
//     truststore_content : `bytes`
//         The content of a JKS truststore containing the cluster CA certificate.
//     password : `str`
//         The password generated for the truststore.
// Notes
// -----
// Internally this function calls out to the ``keytool`` command-line tool.

func (cp *CertProcessor) CreateTruststore(cert string, password string) ([]byte, string, error) {
	if password == "" {
		password = GeneratePassword(24, true, false)
	}
	// Create temporary file
	file, err := os.CreateTemp("", "ca_cert")
	if err != nil {
		cp.log.Error(err, "Failed to create temp file", "File", "ca_cert")
	}
	defer func() {
		err = os.Remove(file.Name())
		if err != nil {
			cp.log.Error(err, "Failed to delete file", "File", file.Name())
		}
	}()

	// Save certificate to temporary file
	_, err = file.WriteString(cert)
	if err != nil {
		cp.log.Error(err, "Failed to write cert to temp file", "File", "ca_cert")
	}
	// Set trustore output file
	tempDir := os.TempDir()
	output_path := tempDir + "/" + "client.truststore.jks"
	defer func() {
		err = os.Remove(output_path)
		if err != nil {
			cp.log.Error(err, "Failed to delete file", "File", output_path)
		}
	}()
	// Generate trustore
	cmd := exec.Command("keytool", "-importcert", "-keystore", output_path, "-alias", "CARoot", "-file",
		file.Name(), "-storepass", password, "-storetype", "jks", "-trustcacerts", "-noprompt")

	out, err := cmd.Output()

	if err != nil {
		cp.log.Error(err, "Error while exec command", "cmdout", out)
	}
	// Check if trustore exist
	if _, err := os.Stat(output_path); os.IsNotExist(err) {

		cp.log.Error(err, "File not exist", "File", output_path)
		return nil, "", err
	}
	// Read trustore from file to save in kubernetes secret
	b, err := os.ReadFile(output_path) // just pass the file name
	if err != nil {
		cp.log.Error(err, "File read fail", "File", output_path)
		return nil, "", err
	}

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
// 	with.

// Raises
// ------
// subprocess.CalledProcessError
// 	Raised if the calls to :command:`keystore` or :command:`openssl` result
// 	in a non-zero exit status.
// RuntimeError
// 	Raised if the truststore is not generated.

// Notes
// -----
// Internally this function calls out to the ``openssl`` and ``keytool``
// command-line tool.

type CertProcessor struct {
	log *logr.Logger
}

func (cp *CertProcessor) CreateKeystore(userCACert string, userCert string, userKey string, userp12 string, password string) ([]byte, string, error) {

	if password == "" {
		password = GeneratePassword(24, true, false)
	}
	var p12_path string

	tempDir := os.TempDir()

	if userp12 == "" {

		// Create temporary file
		userCAFile, err := os.CreateTemp("", "user_ca.crt")
		if err != nil {
			cp.log.Error(err, "Failed to create temp file", "File", "user_ca.crt")
		}
		defer func() {
			err = os.Remove(userCAFile.Name())
			if err != nil {
				cp.log.Error(err, "Failed to delete file", "File", userCAFile.Name())
			}
		}()

		// Save ca certificate to temporary file
		_, err = userCAFile.WriteString(userCACert)
		if err != nil {
			cp.log.Error(err, "Error writing to temp file", "File", "user_ca.crt")
		}

		userCertFile, err := os.CreateTemp("", "user.crt")
		if err != nil {
			cp.log.Error(err, "Failed to create temp file", "File", "user.crt")
		}
		defer func() {
			err = os.Remove(userCertFile.Name())
			if err != nil {
				cp.log.Error(err, "Failed to delete file", "File", userCertFile.Name())
			}
		}()

		// Save user certificate to temporary file
		_, err = userCertFile.WriteString(userCert)
		if err != nil {
			cp.log.Error(err, "Error writing to temp file", "File", "user.crt")
		}

		userKeyFile, err := os.CreateTemp("", "user.key")
		if err != nil {
			cp.log.Error(err, "Failed to create temp file", "File", "user.key")
		}
		defer func() {
			err = os.Remove(userKeyFile.Name())
			if err != nil {
				cp.log.Error(err, "Failed to delete file", "File", userKeyFile.Name())
			}
		}()

		// Save user key to temporary file
		_, err = userKeyFile.WriteString(userKey)
		if err != nil {
			cp.log.Error(err, "Error writing to temp file", "File", "user.key")
		}

		p12_path = tempDir + "/" + "user.p12"

		// Generate p12 format bundle
		cmd := exec.Command("openssl", "pkcs12", "-export", "-in", userCertFile.Name(), "-inkey", userKeyFile.Name(), "-chain", "-CAfile",
			userCAFile.Name(), "-name", "confluent-schema-registry", "-passout", "pass:"+password, "-out", p12_path)

		out, err := cmd.Output()

		if err != nil {
			cp.log.Error(err, "Error while exec command", "cmdout", out)
			return nil, "", err
		}
		// Check if p12 format bundle exist
		if _, err := os.Stat(p12_path); os.IsNotExist(err) {
			cp.log.Error(err, "File not exist", "File", p12_path)
			return nil, "", err
		}
	} else {
		cp.log.Info("Using p12 cert store")
		userKeyFilep12, err := os.CreateTemp("", "user.p12")
		if err != nil {
			cp.log.Error(err, "Failed to create temp file", "File", "user.p12")
			return nil, "", err
		}

		// Save p12 certificate to temporary file
		p12Data := userp12
		_, err = userKeyFilep12.Write([]byte(p12Data))
		if err != nil {
			cp.log.Error(err, "Error writing to temp file", "File", userKeyFilep12.Name())
			return nil, "", err
		}
		p12_path = userKeyFilep12.Name()
	}
	defer func() {
		err := os.Remove(p12_path)
		if err != nil {
			cp.log.Error(err, "Failed to delete file", "File", p12_path)
		}
	}()

	keystore_path := tempDir + "/" + "client.keystore.jks"
	defer func() {
		err := os.Remove(keystore_path)
		if err != nil {
			cp.log.Error(err, "Failed to delete file", "File", keystore_path)
		}
	}()
	// Generate keystore
	cp.log.Info("Generate keystore")
	cmd := exec.Command("keytool", "-importkeystore", "-deststorepass", password, "-destkeystore", keystore_path,
		"-deststoretype", "jks", "-srckeystore", p12_path, "-srcstoretype", "PKCS12", "-srcstorepass", password, "-noprompt")
	out, err := cmd.Output()
	if err != nil {
		cp.log.Error(err, "Error while exec command", "cmdout", out)
		return nil, "", err
	}
	// Check if trustore exist
	if _, err := os.Stat(keystore_path); os.IsNotExist(err) {
		cp.log.Error(err, "File not exist", "File", keystore_path)
		return nil, "", err
	}
	// Read trustore from file to save in kubernetes secret
	b, err := os.ReadFile(keystore_path) // just pass the file name
	if err != nil {
		cp.log.Error(err, "File read fail", "File", keystore_path)
		return nil, "", err
	}

	return b, password, nil
}

func NewCertProcessor(logger *logr.Logger) *CertProcessor {
	return &CertProcessor{logger}
}

func Decode_secret_field(str string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(str)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
