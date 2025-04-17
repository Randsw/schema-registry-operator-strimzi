/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kafka "github.com/RedHatInsights/strimzi-client-go/apis/kafka.strimzi.io/v1beta2"
	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment
var ctx context.Context
var cancel context.CancelFunc

const kafkaCRDUrl string = "https://raw.githubusercontent.com/strimzi/strimzi-kafka-operator/refs/heads/main/install/cluster-operator/040-Crd-kafka.yaml"
const kafkaUserCRDUrl string = "https://raw.githubusercontent.com/strimzi/strimzi-kafka-operator/refs/heads/main/install/cluster-operator/044-Crd-kafkauser.yaml"

// DownloadCRD downloads a CRD from a URL and converts it to *apiextensionsv1.CustomResourceDefinition
func DownloadCRD(ctx context.Context, url string) (*apiextensionsv1.CustomResourceDefinition, error) {
	// 1. Download the file from the URL
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to download CRD from %s: %v", url, err)
	}
	defer func() {
		err := resp.Body.Close()
		if err != nil {
			fmt.Printf("Failed to close respone body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code when downloading CRD: %d", resp.StatusCode)
	}

	// 2. Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read CRD content: %v", err)
	}

	// 3. Parse the YAML into CRD object
	crd := &apiextensionsv1.CustomResourceDefinition{}
	decoder := yaml.NewYAMLOrJSONDecoder(io.NopCloser(bytes.NewReader(body)), 1024)
	if err := decoder.Decode(crd); err != nil {
		return nil, fmt.Errorf("failed to decode CRD YAML: %v", err)
	}

	// 4. Validate the CRD
	if crd.Kind != "CustomResourceDefinition" {
		return nil, fmt.Errorf("downloaded file is not a CRD (kind is %s)", crd.Kind)
	}

	return crd, nil
}
func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	var err error
	ctx, cancel = context.WithCancel(context.TODO())

	kafkaCRD, err := DownloadCRD(ctx, kafkaCRDUrl)
	if err != nil {
		logf.Log.Error(err, "Failed to download KafkaCRD")
		return
	}

	kafkaUserCRD, err := DownloadCRD(ctx, kafkaUserCRDUrl)
	if err != nil {
		logf.Log.Error(err, "Failed to download KafkaUserCRD")
		return
	}

	CRDs := []*apiextensionsv1.CustomResourceDefinition{}

	CRDs = append(CRDs, kafkaCRD)

	CRDs = append(CRDs, kafkaUserCRD)

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		CRDs:                  CRDs,
		// The BinaryAssetsDirectory is only required if you want to run the tests directly
		// without call the makefile target test. If not informed it will look for the
		// default path defined in controller-runtime which is /usr/local/kubebuilder/.
		// Note that you must have the required binaries setup under the bin directory to perform
		// the tests directly. When we run make test it will be setup and used automatically.
		BinaryAssetsDirectory: filepath.Join("..", "..", "bin", "k8s",
			fmt.Sprintf("1.31.0-%s-%s", runtime.GOOS, runtime.GOARCH)),
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = strimziregistryoperatorv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = kafka.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
