//go:build e2e

package e2e

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// registryClient talks to the Schema Registry REST API through the
// port-forwarded service endpoint. The server certificate is signed by the
// Strimzi cluster CA and carries the <registry>.<namespace>.svc DNS SANs, so
// ServerName is set to the in-cluster service DNS name for hostname
// verification while connecting to 127.0.0.1.
//
// The request Host header is set to the same service DNS name: the Schema
// Registry's Jetty server rejects requests whose Host does not match the TLS
// SNI value with "400 Host does not match SNI".
type registryClient struct {
	baseURL string
	host    string
	http    *http.Client
}

func newRegistryClient(caPool *x509.CertPool, clientCert *tls.Certificate, port int) *registryClient {
	host := fmt.Sprintf("%s.%s.svc", scenario.RegistryName, scenario.Namespace)
	tlsConfig := &tls.Config{
		RootCAs:    caPool,
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	}
	if clientCert != nil {
		tlsConfig.Certificates = []tls.Certificate{*clientCert}
	}
	return &registryClient{
		baseURL: fmt.Sprintf("https://127.0.0.1:%d", port),
		host:    host,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
		},
	}
}

// do performs an HTTP request against the Schema Registry REST API and
// returns the status code and the fully read response body.
func (c *registryClient) do(method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Host = c.host
	req.Header.Set("Content-Type", "application/vnd.schemaregistry.v1+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

// listSubjects returns all registered subjects.
func (c *registryClient) listSubjects() ([]string, error) {
	status, data, err := c.do(http.MethodGet, "/subjects", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("GET /subjects returned %d: %s", status, data)
	}
	var subjects []string
	if err := json.Unmarshal(data, &subjects); err != nil {
		return nil, err
	}
	return subjects, nil
}

// registerSchema registers a JSON schema under the given subject and returns
// the assigned schema ID.
func (c *registryClient) registerSchema(subject, schema string) (int, error) {
	status, data, err := c.do(http.MethodPost, fmt.Sprintf("/subjects/%s/versions", subject),
		map[string]string{"schemaType": "JSON", "schema": schema})
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, fmt.Errorf("POST /subjects/%s/versions returned %d: %s", subject, status, data)
	}
	var result struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return 0, err
	}
	return result.ID, nil
}

// getSchemaByID fetches the schema string for the given ID.
func (c *registryClient) getSchemaByID(id int) (string, error) {
	status, data, err := c.do(http.MethodGet, fmt.Sprintf("/schemas/ids/%d", id), nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("GET /schemas/ids/%d returned %d: %s", id, status, data)
	}
	var result struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	return result.Schema, nil
}

// getGlobalConfig returns the global compatibility level (e.g. "FORWARD").
func (c *registryClient) getGlobalConfig() (string, error) {
	status, data, err := c.do(http.MethodGet, "/config", nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("GET /config returned %d: %s", status, data)
	}
	var result struct {
		CompatibilityLevel string `json:"compatibilityLevel"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	return result.CompatibilityLevel, nil
}
