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
	"context"
	"os"
	"time"

	kafka "github.com/RedHatInsights/strimzi-client-go/apis/kafka.strimzi.io/v1beta2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const cert string = `-----BEGIN CERTIFICATE-----
MIIFLTCCAxWgAwIBAgIUbe6ebrD+ANcQf1kYa8e4ZJJs/QwwDQYJKoZIhvcNAQEN
BQAwLTETMBEGA1UECgwKaW8uc3RyaW16aTEWMBQGA1UEAwwNY2x1c3Rlci1jYSB2
MDAeFw0yNTAzMTQxNTUzMjZaFw0yNjAzMTQxNTUzMjZaMC0xEzARBgNVBAoMCmlv
LnN0cmltemkxFjAUBgNVBAMMDWNsdXN0ZXItY2EgdjAwggIiMA0GCSqGSIb3DQEB
AQUAA4ICDwAwggIKAoICAQDJtPr6eazRhGz28FNiJkmXQQ14ZKvfGr+2pXyQE985
izJ4HIHjqdbnL0GabuTxwJBOF5zWtb8TAzsI5cB98W3IHJHPCNX2qEI9DqWd4x1m
Qm8jV36O8OGQ/Xz5hYzlKmbXNqErzW5qkM0hFk9YK50raltHusfYnUIkRypPheY8
ZuxIUFisWz/QT/d98+K+wet7bEVae4hlyWu5hZ5Io+cgf4dlTUh8XiAoHlmnPnsv
BE1tc5Zl7Tya7R1QlbTJYTFemF9B8ZuwHbHGHOA+VlImwPn7LrIY1JoHQ1o6zZVB
2e2DwPtGTU5dMe3YU8h8vmyF5mk3m1Sk5ZphfP40JeFNmiLNXwilub06hHoy2H5+
PXudh+I/Q/pxhXyA4D0qB8BSVCry5T/lXseOIM5FUVNhK0kSYzkjwsfIHMwJC8VT
TK+3SuhAM5bkb/8+Vp2Jn+xsJqKr7i7SS2MR6pT6Twv4rcW+soEVfW5R3wEcNH6x
dVMQsYjfllwGQD6oYeNr3bTzj4QTCHqAxce6doAba6lSSwyshMh7jqZ2nhQD9FqA
VsfCxVQLRapcjOluY/htRwptCSq8aHCNOgrEfbQMO7zte0LwY8/Q818T4J19+mpy
SlOe+MS6zKan9PutHhKvP7727/G5WQOy31CGSv72wEfvN/ggrjGwYNgcekOHrvh0
rwIDAQABo0UwQzAdBgNVHQ4EFgQUxWIOTnlDuqTWTxr3/0ck49fRiuMwEgYDVR0T
AQH/BAgwBgEB/wIBADAOBgNVHQ8BAf8EBAMCAQYwDQYJKoZIhvcNAQENBQADggIB
AL+fUCf5ccVzFDxYAS6wSF0kUAs8JZDDIDwnIWb0BF5nvjaHsCJgG2Yk3s8cX15d
k/ELwH0XZP9Yc5fSyEz3XPy2pdHByS3XgLCDTIzfYmGr6FFRI1IrV0BTHeTw4nWQ
kIH+7QqabamyF9vJdSM1vZdXJLLOybU29MqpLQpJTiADyqawbcCQXAEuRwkzrHHf
nxkbxtdVcYtRvhv1jvQLLkFYqXnVIaMYptf+aBgGON25c/Svtr1tHrZ6r5a2zR3Z
xJO22DiRtXlN/HnEKZALCdmvBbUgWAZh3O14RsFSgKIsg+qVwqd5Ha1IO/kvulJy
AloIBwNiqJinjmJ/daF/cPn4r4rVP8iXlpUXmmnjEDBeN0OMm/5omXdOjm8kQ4ig
chociQrxWqNiS35iqvvyo9gxPjpFoxqxmecVMedA4lM1VyYgF5oOwekfA+cJgAib
bkSuMcjoAsHcJZRi7Frb4eM8FSMHfUoftVULz4xzAtPzxw7d50KihKtQdH/eXggB
Jt+U/PiN6LL6X4adQkbBAK1mXg/FnKyzvpw4fXCHGlcfUEXeZCYbjh9mXxo2H2RA
YOfNN2wulDZ7o+4nU8lAgzZPIQZpBPMRT/lTfyBzbzGcAAS26G5VDehNKijGu0PL
C1Su8qJSFefPuGQm0ht3N1RNpZF21NCKK4umiUrVS9VB
-----END CERTIFICATE-----
`

func clusterCACert() string {
	clusterCACert := `-----BEGIN CERTIFICATE-----
MIIFLTCCAxWgAwIBAgIUYThZW2C1f6pLM6JbgsuQusZDpKUwDQYJKoZIhvcNAQEN
BQAwLTETMBEGA1UECgwKaW8uc3RyaW16aTEWMBQGA1UEAwwNY2x1c3Rlci1jYSB2
MDAeFw0yMTEwMTkyMDQ4NDFaFw0yMjEwMTkyMDQ4NDFaMC0xEzARBgNVBAoMCmlv
LnN0cmltemkxFjAUBgNVBAMMDWNsdXN0ZXItY2EgdjAwggIiMA0GCSqGSIb3DQEB
AQUAA4ICDwAwggIKAoICAQDPEjmS0/K6o+o9zLwYNo7WMeHLCsHnbw8aljDBUXKR
duftlQlIUQGJC22AH/kmxaHarIAZGhvZtSSpBM662oI9DGL/5Vt7ASvlzch2U5Rx
NHk3R8+Whsnc6UzZsKxvpronoG638UXi7g8nbCIJUUzTjwp/71T1jugfG6cbZPfs
Mj955pM3RNT8JNlLSU5LUj/DEU+HZfsAVYULUl70CYhHFo9yVA4cag2/wBXr5ejQ
oVi5TpFFcnA6Qi0kcECbdX7Tt99MBXcMd2Hc8Rw/nZZYF5oiRYGp0mlMqS/ev1zJ
OTN5Wg8qxPKaz3XocaEMwT+3hLehNEVy8KMsr2fsgiREUYrXjMppnkBLELq6p6uM
kmzvJ8IPbOUA/crnfVGWGNtsOVUCGVOdJA0KHEIlqiZbk5LWWBGwniBEs7rHA/Pm
6eCFmwhkyX/A3IvluxWksyTKlUg/RLLY88H3RWEnf+PMScFO58wnA0jfZ9+e5fRw
b9ozOk+V5vc9AZB0PdJzC0wRzJN3pERC77zrJrGOGFRvgz6Bg86LoWhzH3D2TGpF
49eNJS9NPgdOC7u2y7WvgkR2Q1TvHndDrm+ZPw+gcBuJB234d+zBY8KQSKOE4qo1
zveJCuaaTe9yT/bTEcrL4gipbJ3i2t9tCUkED8AJepXAzT9jXcesGH9KQ9v4f0Sk
awIDAQABo0UwQzAdBgNVHQ4EFgQU1wq9E8qWO93Ln1brxd0/AxmsPt0wEgYDVR0T
AQH/BAgwBgEB/wIBADAOBgNVHQ8BAf8EBAMCAQYwDQYJKoZIhvcNAQENBQADggIB
AM5/4tYT4X9f29HlYhBNooH394cKUfrodObFHYIywh20Mah7iyw2Os12OxyzKNVp
YjTTgPFHl0KmExOUglJvSorY2TVfiUiq7QzT5XwRnosmSryqMR8B/vMyiQEn1of+
vEID4k+eB8bdFlQCV6xmyR3swcwM0aOpne29D6x5p/ilOknXNA2xlxUzF470Awvm
NbLHW1ODtyDlvHS3eS6ofN3Sf2nR4r8i8fF/NceL+VDnVOl0sLAj+awgNBeEuUQl
zLCmcgqmugFT2haxOmr9IDlXNXSI8YFqEfE2nHgLo17lq1/rDThi78PXlgZpprT4
1hpEocD/t+6nJePQVzzV1CyTihcCRwVYbSI+T4Z2c+FYvc5vdiQJ5hoS7QcI1ns4
vbgyxXpw4Wz+/3IAga6qjsYUb6DN70JmLy3pwB2aVDN8lvmxlOvhtkJcDBkgFCLc
9LGEh/CHonDIYG6st43Te+fzJk2B8IO8nllOZMh1aAX0/9yNXco/2E7R6DKuhnj4
8YxwugJcZ7iydK+giatSlF059AD5nqk0wp4CQxw6nhQcJsGxMHfmSl6tlZwhUJrV
SnJ2auomP4fc6elwr9LCBOl1HpBZ5XkijO7WLL1zCGBDYIu45Ruy16BYN1EWqnjJ
sr7xIs5yJ9mzWcjYQ/9XJkIh5j0whhVFB17W9bzEnTjs
-----END CERTIFICATE-----
`
	return clusterCACert
}

func userCACert() string {
	userCACert := `-----BEGIN CERTIFICATE-----
MIIFFzCCAv+gAwIBAgIUJOLkz7zytAO1u4rGiLsg+dyiWcIwDQYJKoZIhvcNAQEL
BQAwGjEYMBYGA1UEAwwPU1RSSU1aSS1TUi1URVNUMCAXDTI1MDMyODEyMzgyNFoY
DzIwNTIwODEzMTIzODI0WjAaMRgwFgYDVQQDDA9TVFJJTVpJLVNSLVRFU1QwggIi
MA0GCSqGSIb3DQEBAQUAA4ICDwAwggIKAoICAQC67S6hAyAGLAtWPPeAsIFWZf+5
LK16A2cd4QlUgOhzvTPm7x6MQVhQi0fNpReKytZ4B9yDn7/sjeu6Au3oqszIw2sF
lAjrP5U5aAb93WjfZnvjU0xP9pyeb7REiBkR6iwhL+fj4CieAtc6b/fx9Qi6Mg18
Si9vKIF193jHAcvEBfvPDGtZCq1MTtIlqHN37buHGkjLTmxjzWM6MwQHU11RvuP8
r7xRyVPPkTqAo2bW+rxRlo0J/hbBZbkcSZ0OxrH9P7JsqdlCHITksE2mh2A6MpB6
NyTV/LiEZjIC4YMIK+/X1UO/lS7OxLq2kRbIj2UoIxJnoCTxP1Zzr123uP65cNt4
vkXgrQgprc4ijBVB3fyicwIG8CJWXLqkjuZ3XYgeTdufMUfFnICUKMkjXC/24QyX
6VFY3qtjc/iI31rFak5w4GLbH4LA0GjzMChNDKX4RAhL+D268YTr8eVIbG9OhYtJ
zBA6NAter9RVGpoCJ+jpLrVWfnabm3qlIUOHhdTg+a2ruRyZZM4HNuFLTSCq3Wa1
JNN/lrub55ZXfg35pgplz8vKhAmuwf91Tfh/gVyNyDp6p+F9xSdk2wteMYQG9ufm
j6QQq/9KVq9p4ua+SblVm+uaiv0cu2C/JLm4EwUC2fk1aJaZF2OwNlVwf1eDuMVp
tmvcqXQ4GzR8ZncctwIDAQABo1MwUTAdBgNVHQ4EFgQUkj0W4Z8/3ehvi8ZoIJqg
XoHxALkwHwYDVR0jBBgwFoAUkj0W4Z8/3ehvi8ZoIJqgXoHxALkwDwYDVR0TAQH/
BAUwAwEB/zANBgkqhkiG9w0BAQsFAAOCAgEACU7Q3lK64I0S/rfkPinKk43RXdAa
dh1ecs+FSgw5fkYGq6hH5WnSwG5ftBgrfqtKccFL++0kXFlssRHe0XwgZOU6mq6Y
Ibwug7PVhliJWPUAmkImBxx+2nEcbKe8/aQhJdYU42F/iKM2eu0lpuxpCGTxjnWX
MmbqlYo6+sZdyv3d51lqR6H2vj/kWx3UE5hFqL+9f+yq1j9bzizZPHUz/AG9wsds
ZDkIOH9Rt/+qRrKebSMVVK2R0AwMVfq6FLP6jjLcxsemAtLnRGDkKLBnq8RMZSKj
jSr/DvkdnmVmM+Q9OoliSfqrXq4bReKr9D1sjIFXhW+snfRbWP/x5+lkBUzOGGln
5lUo8i4P6cck5dyW0X16aDTQXuUd91AR00ZzTUNAq3YrPrtflPi6NcmJVfab2Ogh
hAf+PebfSSv2rZ5RtQo28P1YaegerzdHLaIpofGP2RwQ5AWNNB3zKpvXZ5ysRN52
2jGal+H4mnpWqznb8ynCNki6zIanWofbKulplj0a8TooT1OBJap+U9l4QLXNDiVN
aR9Jf0P9e3xtBnNeIVUON2aOQ5PTw5VdLVlo3umBWGQ7odSbOmvTnJkqA7psmVOR
DUagf2U5O1GIuJmTgz2yrD1kpZis79k6VTvc+QIQdUFa15Pgk4QfnPszT0PJlfg0
mmY6wv33AVS6Ok0=
-----END CERTIFICATE-----
`
	return userCACert
}

func userCert() string {
	userCert := `-----BEGIN CERTIFICATE-----
MIIExzCCAq8CFC0mSjQoIueh2CgTnAseeZLzWwUDMA0GCSqGSIb3DQEBCwUAMBox
GDAWBgNVBAMMD1NUUklNWkktU1ItVEVTVDAgFw0yNTAzMjgxMjQzMTBaGA8yMDUy
MDgxMzEyNDMxMFowJDEiMCAGA1UEAwwZY29uZmx1ZW50LXNjaGVtYS1yZWdpc3Ry
eTCCAiIwDQYJKoZIhvcNAQEBBQADggIPADCCAgoCggIBAK/ZwIHy3hzKI9f7mHjZ
ABLAJgV/qu0tmKP/BScu/rG5iNpnYRxcrBcnAuRqN1A5juJ+InDkJ7T8hbeFeO8Z
T6eD+QS6OZ9TwvVC0/RfsH3XugrsI87DgdOmmaEB9ucA5puQzOSNPBUxSENlPRBY
4Dvedp5CsADwl/uAr/P/OtrWdiFjul4eYWLw+gblU37fm3RvadGHdZqaKbUxV2Jd
X8S52mBwoVwU9zPqeeywm5JknzNFnE3FYchLERPs4vMHnFaoJRUiKnczBQoxx+82
Y0rEu5RqXTQ1hGs/FJ3Die+d90dEIEpsysJNqzTHCHqlfhKB+iBWbNzXeFPc6cKU
C6hzcosvtGlSKRUKN9QvlhU30gAfvgl4MVgXQodt5QkB0d/WkYceUbPm5LUbNsLi
8yN6b5sK/dzMPQ4WaJYd2CJsR/Ld7KGbwYt+sNa+/15d4YwRREbAJJdBNcSpdHGO
BnPEwRGHo+lyMfdxBxzsGAevZGwClCOzXPUgQ/NETr8WNgTX4kaaAKxWI6olIynG
fWrsluD3Rn8TzTrNO7lc9xdy72zHlzutERhkstDM2wG6KvDMLs9pW7MJaWNpNrvN
/kMwem0phTedmUnpK/FjW6rS+5RemFWb8QKQgaiZvawtSGQwEZJclQGHeoP4K9E/
DML3H8S3/r2iZ3w02al5OIoFAgMBAAEwDQYJKoZIhvcNAQELBQADggIBAFRIdTI7
erMwNUM1D6FlOgGzLcvsv8EiEl8y/nFvzjyB5Bh29o5j3x0nuBkHmmncyduCtzKh
CsBhfZFm4Wj4HZ5CzII7yHv9xhxcrsj3KTChFAWL0gIyyxqcS1BlcKvmtovp6rna
O3LpmNhcqpaI8uIQUiJvglZMqvnoSj7xJGHMcxz+7F5klQ8GTuNR0SB/Ix3K4+xO
Jeg4wkjGyajGLGgOxrr6TkB2C6W3EfVsuMB6ZeD00EA4j/x+x86FWDm5fhx1fS95
4kzD50EZhpCX5qZy7psOB3gDw/Gpv3frX2BcvA3+1VAtOvkI2r8NfJRwJ5iEQOz4
I+9Z5ecK2y5elCVYFkc6b5UOLgSsBdVoUGbB8qGDNLSzV0g+8VvvGSVy7G8Np6EI
HA4vwnR9TqDCoPaGmSkKMrhplAuvz/dStiDQ/21CDmA5lPIMFH3Bz4PjyBJDuzPI
bxvCWY3ka5CYCaqxF4Ddf7iryaqD2yLHUhjMQ3Dq8iGd97KWAexztPIqfnvgTAxb
nkMzjF2wwBjPttrtdrtmOBF/h5YtyS7JrYxSRsuuJVl+lpovWgCbtrfO8uPW+Zs+
Y0epjjmxKnzTAYCNq2NftqmMShZBy3oF6J4DmyATPPX+p7LfokfkjPc1y6qdZ+8e
31YQPTwkZsk9uReD6gzHB4IjNv3tUKuddP7v
-----END CERTIFICATE-----
`
	return userCert
}

func userKey() string {
	userKey := `-----BEGIN PRIVATE KEY-----
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
1Rs/ntEbB2/Xb9wi5ez3s3thUz/n
-----END PRIVATE KEY-----
`
	return userKey
}

func userp12() string {
	userp12 := `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
s5W0eD3LBAgC8mjMvbRSSAICCAA=
`
	return userp12
}

var jsonString string = `{"apple":5,"lettuce":7}`

var _ = Describe("StrimziSchemaRegistry Controller", func() {
	Context("When reconciling a resource", func() {
		const SchemaRegistryName = "test-resource"

		ctx := context.Background()

		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SchemaRegistryName,
				Namespace: SchemaRegistryName,
			},
		}

		typeNamespacedName := types.NamespacedName{
			Name:      SchemaRegistryName,
			Namespace: SchemaRegistryName,
		}
		strimzischemaregistry := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}

		BeforeEach(func() {
			By("Creating the Namespace to perform the tests")
			err := k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			// Create Kafka Cluster

			By("Creating strimzi kafka cluster")
			Cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kafka-cluster",
					Namespace: namespace.Name,
				},
				Spec: &kafka.KafkaSpec{
					EntityOperator: &kafka.KafkaSpecEntityOperator{
						TopicOperator: &kafka.KafkaSpecEntityOperatorTopicOperator{},
						UserOperator:  &kafka.KafkaSpecEntityOperatorUserOperator{},
					},
					Kafka: kafka.KafkaSpecKafka{
						Authorization: &kafka.KafkaSpecKafkaAuthorization{
							SuperUsers: []string{"CN=root"},
							Type:       kafka.KafkaSpecKafkaAuthorizationTypeSimple,
						},
						Config: &v1.JSON{
							Raw: []byte(jsonString),
						},
						Listeners: []kafka.KafkaSpecKafkaListenersElem{
							{
								Name: "plain",
								Port: 9092,
								Tls:  false,
								Type: kafka.KafkaSpecKafkaListenersElemTypeInternal,
							},
							{
								Name: "tls",
								Port: 9093,
								Tls:  true,
								Type: kafka.KafkaSpecKafkaListenersElemTypeInternal,
								Authentication: &kafka.KafkaSpecKafkaListenersElemAuthentication{
									Type: kafka.KafkaSpecKafkaListenersElemAuthenticationTypeTls,
								},
							},
						},
						Version: ptr.To("3.9.0"),
					},
				},
			}
			status := &kafka.KafkaStatus{
				ClusterId: ptr.To("Ypb68J1GSu-jqq0N0ama0w"),
				Listeners: []kafka.KafkaStatusListenersElem{
					{
						Addresses: []kafka.KafkaStatusListenersElemAddressesElem{
							{
								Host: ptr.To("kafka-cluster-kafka-bootstrap.kafka.svc"),
								Port: ptr.To(int32(9092)),
							},
						},
						BootstrapServers: ptr.To("kafka-cluster-kafka-bootstrap.kafka.svc:9092"),
						Name:             ptr.To("plain"),
					},
					{
						Addresses: []kafka.KafkaStatusListenersElemAddressesElem{
							{
								Host: ptr.To("kafka-cluster-kafka-bootstrap.kafka.svc"),
								Port: ptr.To(int32(9093)),
							},
						},
						BootstrapServers: ptr.To("kafka-cluster-kafka-bootstrap.kafka.svc:9093"),
						Name:             ptr.To("TLS"),
						Certificates:     []string{cert},
					},
				},
			}
			Expect(k8sClient.Create(ctx, Cluster)).To(Succeed())
			Expect(err).To(Not(HaveOccurred()))
			// Update Status of Kafka CR
			UpdateCluser := &kafka.Kafka{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, UpdateCluser)
			Expect(err).To(Not(HaveOccurred()))

			UpdateCluser.Status = status
			By("Updating Kafka Cluster")
			Expect(k8sClient.Status().Update(ctx, UpdateCluser)).To(Succeed())

			// Create Kafka User
			By("creating Kafka User")
			User := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{
					Name:      SchemaRegistryName,
					Namespace: namespace.Name,
					Labels: map[string]string{
						"strimzi.io/cluster": "kafka-cluster",
					},
				},
				Spec: &kafka.KafkaUserSpec{
					Authentication: &kafka.KafkaUserSpecAuthentication{
						Type: kafka.KafkaUserSpecAuthenticationTypeTls,
					},
					Authorization: &kafka.KafkaUserSpecAuthorization{
						Acls: []kafka.KafkaUserSpecAuthorizationAclsElem{
							{
								Host: ptr.To("*"),
								Resource: kafka.KafkaUserSpecAuthorizationAclsElemResource{
									Name:        ptr.To("registry-schemas"),
									PatternType: ptr.To(kafka.KafkaUserSpecAuthorizationAclsElemResourcePatternTypeLiteral),
									Type:        kafka.KafkaUserSpecAuthorizationAclsElemResourceTypeTopic,
								},
								Operations: []kafka.KafkaUserSpecAuthorizationAclsElemOperationsElem{kafka.KafkaUserSpecAuthorizationAclsElemOperationsElemAll},
							},
						},
						Type: kafka.KafkaUserSpecAuthorizationTypeSimple,
					},
				},
			}
			Expect(k8sClient.Create(ctx, User)).To(Succeed())
			Expect(err).To(Not(HaveOccurred()))

			//Create Cluster Secret
			By("Creating kafka cluster CA cert secret")
			clusterSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kafka-cluster-cluster-ca-cert",
					Namespace: namespace.Name,
				},
				Data: map[string][]byte{
					"ca.crt": []byte(clusterCACert()),
				},
			}
			Expect(k8sClient.Create(ctx, clusterSecret)).To(Succeed())
			Expect(err).To(Not(HaveOccurred()))
			p12, err := certprocessor.Decode_secret_field(userp12())
			Expect(err).To(Not(HaveOccurred()))
			//Create Kafka client secret
			By("Creating kafka user secret")
			clientSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      SchemaRegistryName,
					Namespace: namespace.Name,
				},
				Data: map[string][]byte{
					"ca.crt":        []byte(userCACert()),
					"user.crt":      []byte(userCert()),
					"user.key":      []byte(userKey()),
					"user.p12":      []byte(p12),
					"user.password": []byte("test1234"),
				},
			}
			Expect(k8sClient.Create(ctx, clientSecret)).To(Succeed())
			Expect(err).To(Not(HaveOccurred()))

			By("Setting the Image ENV VAR which stores the Operand image")
			err = os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")
			Expect(err).To(Not(HaveOccurred()))

			By("creating the custom resource for the Kind StrimziSchemaRegistry")
			err = k8sClient.Get(ctx, typeNamespacedName, strimzischemaregistry)
			if err != nil && errors.IsNotFound(err) {
				resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      SchemaRegistryName,
						Namespace: namespace.Name,
						Labels: map[string]string{
							"strimzi.io/cluster": "kafka-cluster",
						},
					},
					Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
						StrimziVersion:     "v1beta2",
						SecureHTTP:         false,
						Listener:           "TLS",
						CompatibilityLevel: "forward",
						SecurityProtocol:   "SSL",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "test",
										Image: "confluentinc/cp-schema-registry:7.6.5",
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			err := k8sClient.Get(ctx, typeNamespacedName, found)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance StrimziSchemaRegistry")
			Expect(k8sClient.Delete(ctx, found)).To(Succeed())
			Eventually(func() error {
				return k8sClient.Delete(context.TODO(), found)
			}, 2*time.Minute, time.Second).Should(Succeed())

			// TODO(user): Attention if you improve this code by adding other context test you MUST
			// be aware of the current delete namespace limitations.
			// More info: https://book.kubebuilder.io/reference/envtest.html#testing-considerations
			By("Deleting the Namespace to perform the tests")
			_ = k8sClient.Delete(ctx, namespace)

			By("Removing the Image ENV VAR which stores the Operand image")
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should successfully reconcile the resource", func() {
			By("Checking if the custom resource was successfully created")
			Eventually(func() error {
				found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
				return k8sClient.Get(ctx, typeNamespacedName, found)
			}, time.Minute, time.Second).Should(Succeed())

			By("Reconciling the created resource")
			controllerReconciler := &StrimziSchemaRegistryReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking if Deployment was successfully created in the reconciliation")
			Eventually(func() error {
				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, found)
			}, time.Minute, time.Second).Should(Succeed())

			By("Checking if Secret was successfully created in the reconciliation")
			Eventually(func() error {
				found := &corev1.Secret{}
				typeNamespaceName := types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}
				return k8sClient.Get(ctx, typeNamespaceName, found)
			}, time.Minute*2, time.Second).Should(Succeed())

			By("Reconciling the created resource")
			controllerReconciler = &StrimziSchemaRegistryReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking if service was successfully created in the reconciliation")
			Eventually(func() error {
				found := &corev1.Service{}
				typeNamespaceName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}
				return k8sClient.Get(ctx, typeNamespaceName, found)
			}, time.Minute*2, time.Second).Should(Succeed())
		})
	})
})
