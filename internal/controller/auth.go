/*
Copyright 2024 Randsw.

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
	"context"
	"fmt"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type kafkaAuthMode int

const (
	authMTLS          kafkaAuthMode = iota // SSL: TLS transport + client cert
	authSASLSSL                            // SASL_SSL: TLS transport + SCRAM-SHA-512
	authSASLPlaintext                      // SASL_PLAINTEXT: SCRAM-SHA-512 only
	authPlaintext                          // PLAINTEXT: no auth
)

type kafkaAuth struct {
	Protocol        string
	Mode            kafkaAuthMode
	NeedsTruststore bool
	NeedsKeystore   bool
	NeedsSASL       bool
	SASLMechanism   string
}

const saslMechanismSCRAMSHA512 = "SCRAM-SHA-512"

// resolveKafkaAuth determines the effective Kafka authentication configuration
// from the StrimziSchemaRegistry security protocol and the referenced KafkaUser.
func resolveKafkaAuth(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, kafkaUser *kafka.KafkaUser) (kafkaAuth, error) {
	protocol := instance.Spec.SecurityProtocol
	if protocol == "" {
		protocol = "SSL"
	}

	switch protocol {
	case "SSL":
		if kafkaUser == nil || kafkaUser.Spec.Authentication == nil || kafkaUser.Spec.Authentication.Type != kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE {
			return kafkaAuth{}, fmt.Errorf("security protocol %s requires a KafkaUser with tls authentication", protocol)
		}
		return kafkaAuth{
			Protocol:        protocol,
			Mode:            authMTLS,
			NeedsTruststore: true,
			NeedsKeystore:   true,
			NeedsSASL:       false,
		}, nil
	case "SASL_SSL":
		if kafkaUser == nil || kafkaUser.Spec.Authentication == nil || kafkaUser.Spec.Authentication.Type != kafka.SCRAM_SHA_512_KAFKAUSERAUTHENTICATIONTYPE {
			return kafkaAuth{}, fmt.Errorf("security protocol %s requires a KafkaUser with scram-sha-512 authentication", protocol)
		}
		return kafkaAuth{
			Protocol:        protocol,
			Mode:            authSASLSSL,
			NeedsTruststore: true,
			NeedsKeystore:   false,
			NeedsSASL:       true,
			SASLMechanism:   saslMechanismSCRAMSHA512,
		}, nil
	case "SASL_PLAINTEXT":
		if kafkaUser == nil || kafkaUser.Spec.Authentication == nil || kafkaUser.Spec.Authentication.Type != kafka.SCRAM_SHA_512_KAFKAUSERAUTHENTICATIONTYPE {
			return kafkaAuth{}, fmt.Errorf("security protocol %s requires a KafkaUser with scram-sha-512 authentication", protocol)
		}
		return kafkaAuth{
			Protocol:        protocol,
			Mode:            authSASLPlaintext,
			NeedsTruststore: false,
			NeedsKeystore:   false,
			NeedsSASL:       true,
			SASLMechanism:   saslMechanismSCRAMSHA512,
		}, nil
	case "PLAINTEXT":
		return kafkaAuth{
			Protocol:        protocol,
			Mode:            authPlaintext,
			NeedsTruststore: false,
			NeedsKeystore:   false,
			NeedsSASL:       false,
		}, nil
	default:
		return kafkaAuth{}, fmt.Errorf("unsupported security protocol: %s", protocol)
	}
}

// getKafkaUser returns the KafkaUser CR whose name matches the SchemaRegistry
// instance, or nil when no such KafkaUser exists.
func (r *StrimziSchemaRegistryReconciler) getKafkaUser(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, ctx context.Context) (*kafka.KafkaUser, error) {
	kafkaUsers := &kafka.KafkaUserList{}
	err := r.List(ctx, kafkaUsers, client.InNamespace(instance.Namespace))
	if err != nil {
		return nil, err
	}
	for i := range kafkaUsers.Items {
		if kafkaUsers.Items[i].Name == instance.Name {
			return &kafkaUsers.Items[i], nil
		}
	}
	return nil, nil
}
