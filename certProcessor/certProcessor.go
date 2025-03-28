// Utilities for generating the truststore and keystore for the Schema Registry
// based on the cluster's CA cert and the KafkaUser's key.
package certprocessor

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"time"
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

func CreateTruststore(cert string, password string) ([]byte, string, error) {
	if password == "" {
		password = GeneratePassword(24, true, false)
	}
	// Create temporary file
	file, err := os.CreateTemp("", "ca_cert")
	if err != nil {
		fmt.Println(err.Error())
	}
	defer os.Remove(file.Name())

	// Save certificate to temporary file
	_, err = file.WriteString(cert)
	if err != nil {
		fmt.Println(err.Error())
	}
	// Set trustore output file
	tempDir := os.TempDir()
	output_path := tempDir + "/" + "client.truststore.jks"
	defer os.Remove(output_path)

	// Generate trustore
	cmd := exec.Command("keytool", "-importcert", "-keystore", output_path, "-alias", "CARoot", "-file",
		file.Name(), "-storepass", password, "-storetype", "jks", "-trustcacerts", "-noprompt")

	_, err = cmd.Output()

	if err != nil {
		fmt.Println(err.Error())
	}
	// Check if trustore exist
	if _, err := os.Stat(output_path); os.IsNotExist(err) {
		fmt.Println("File not exist")
		return nil, "", err
	}
	// Read trustore from file to save in kubernetes secret
	b, err := os.ReadFile(output_path) // just pass the file name
	if err != nil {
		fmt.Println(err.Error())
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

func CreateKeystore(userCACert string, userCert string, userKey string, userp12 string, password string) ([]byte, string, error) {

	if password == "" {
		password = GeneratePassword(24, true, false)
	}
	var p12_path string

	tempDir := os.TempDir()

	if userp12 == "" {
		// Create temporary file
		userCAFile, err := os.CreateTemp("", "user_ca.crt")
		if err != nil {
			fmt.Println(err.Error())
		}
		defer os.Remove(userCAFile.Name())

		// Save ca certificate to temporary file
		_, err = userCAFile.WriteString(userCACert)
		if err != nil {
			fmt.Println(err.Error())
		}

		userCertFile, err := os.CreateTemp("", "user.crt")
		if err != nil {
			fmt.Println(err.Error())
		}
		defer os.Remove(userCertFile.Name())

		// Save user certificate to temporary file
		_, err = userCertFile.WriteString(userCert)
		if err != nil {
			fmt.Println(err.Error())
		}

		userKeyFile, err := os.CreateTemp("", "user.key")
		if err != nil {
			fmt.Println(err.Error())
		}
		defer os.Remove(userKeyFile.Name())

		// Save user key to temporary file
		_, err = userKeyFile.WriteString(userKey)
		if err != nil {
			fmt.Println(err.Error())
		}

		p12_path = tempDir + "/" + "user.p12"

		// Generate trustore
		cmd := exec.Command("openssl", "pkcs12", "-export", "-in", userCertFile.Name(), "-inkey", userKeyFile.Name(), "-chain", "-CAfile",
			userCAFile.Name(), "-name", "confluent-schema-registry", "-passout", "pass:"+password, "-out", p12_path)

		_, err = cmd.Output()

		if err != nil {
			fmt.Println(err.Error())
		}
		// Check if trustore exist
		if _, err := os.Stat(p12_path); os.IsNotExist(err) {
			fmt.Println("File not exist")
			return nil, "", err
		}
	} else {
		userKeyFilep12, err := os.CreateTemp("", "user.p12")
		if err != nil {
			fmt.Println(err.Error())
		}
		defer os.Remove(userKeyFilep12.Name())
		// Save p12 certificate to temporary file
		_, err = userKeyFilep12.WriteString(userp12)
		if err != nil {
			fmt.Println(err.Error())
		}
		p12_path = userKeyFilep12.Name()

	}

	keystore_path := tempDir + "/" + "client.keystore.jks"
	defer os.Remove(keystore_path)
	// Generate trustore
	cmd := exec.Command("keytool", "-importkeystore", "-deststorepass", password, "-destkeystore", keystore_path,
		"-deststoretype", "jks", "-srckeystore", p12_path, "-srcstoretype", "PKCS12", "-srcstorepass", password, "-noprompt")
	_, err := cmd.Output()

	if err != nil {
		fmt.Println(err.Error())
	}
	// Check if trustore exist
	if _, err := os.Stat(keystore_path); os.IsNotExist(err) {
		fmt.Println("File not exist")
		return nil, "", err
	}
	// Read trustore from file to save in kubernetes secret
	b, err := os.ReadFile(keystore_path) // just pass the file name
	if err != nil {
		fmt.Println(err.Error())
	}

	return b, password, nil
}
