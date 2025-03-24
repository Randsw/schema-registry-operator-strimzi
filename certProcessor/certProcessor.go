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

func Create_truststore(cert string, password string) ([]byte, string, error) {
	if password == "" {
		password = GeneratePassword(24, true, false)
	}
	file, err := os.CreateTemp("", "ca_cert_")
	if err != nil {
		fmt.Println(err.Error())
	}
	defer os.Remove(file.Name())
	tempDir := os.TempDir()
	output_path := tempDir + "/" + "client.truststore.jks"

	_, err = file.WriteString(cert)
	if err != nil {
		fmt.Println(err.Error())
	}

	cmd := exec.Command("keytool", "-importcert", "-keystore", output_path, "-alias", "CARoot", "-file",
		file.Name(), "-storepass", password, "-storetype", "jks", "-trustcacerts", "-noprompt")

	_, err = cmd.Output()

	if err != nil {
		fmt.Println(err.Error())
	}

	if _, err := os.Stat(output_path); os.IsNotExist(err) {
		fmt.Println("File not exist")
		return nil, "", err
	}

	b, err := os.ReadFile(output_path) // just pass the file name
	if err != nil {
		fmt.Println(err.Error())
	}

	return b, password, nil
}
