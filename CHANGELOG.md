## [1.11.4](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.11.3...1.11.4) (2026-10-26)


### 🛠 Fixes

* **helm:** repair the operator metrics Service ([a2c49da](https://github.com/Randsw/schema-registry-operator-strimzi/commit/a2c49da19020eb1b84e256022574a94811813496))

## [1.11.3](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.11.2...1.11.3) (2026-10-26)


### 🛠 Fixes

* **rbac:** add kafka.strimzi.io kafkas/kafkausers get;list;watch to manager role ([e521071](https://github.com/Randsw/schema-registry-operator-strimzi/commit/e5210716fdc1182c95c4606fd1ae4a45d7c724c3))

## [1.11.2](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.11.1...1.11.2) (2026-10-26)


### 🦊 CI/CD

* **deps:** Bump actions/upload-artifact from 4 to 7 ([182a704](https://github.com/Randsw/schema-registry-operator-strimzi/commit/182a7044055404d9aa7bca36b7eb3f5b49a51f97))
* **deps:** Bump azure/setup-helm from 4 to 5 ([cd8505c](https://github.com/Randsw/schema-registry-operator-strimzi/commit/cd8505c8b6b89933ae3b259017b89aca7219f5d8))
* run e2e only on pull requests and main ([26e6178](https://github.com/Randsw/schema-registry-operator-strimzi/commit/26e617880146f14f87ad514601dbe6b1b5acd836))


### 🛠 Fixes

* **certProcessor:** use per-call temp directories to prevent concurrent-reconcile races ([45abc2b](https://github.com/Randsw/schema-registry-operator-strimzi/commit/45abc2ba216023980516523a2b6d0d0b3ace2a45))

## [1.11.1](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.11.0...1.11.1) (2026-10-22)


### 🧪 Tests

* add kind-based e2e tests for all security protocols with and without client auth ([2d7480d](https://github.com/Randsw/schema-registry-operator-strimzi/commit/2d7480dc50c9da081e07f2a19c3d7d1d2a69868b))


### 🛠 Fixes

* move entityOperator to spec level in e2e Kafka manifests ([7a0b6bc](https://github.com/Randsw/schema-registry-operator-strimzi/commit/7a0b6bcae5610141ba32fd5d6d2c4b56f44d9ce4))
* register the e2e test schema as Avro instead of JSON ([f5fa9f1](https://github.com/Randsw/schema-registry-operator-strimzi/commit/f5fa9f1995432cf2e7805e6278ee251e54cd00d5))
* send service DNS name in the Host header of e2e REST requests ([df28e54](https://github.com/Randsw/schema-registry-operator-strimzi/commit/df28e54db27a2568ec25a8d29149296d0d90cf3f))

## [1.11.0](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.10.1...1.11.0) (2026-10-21)


### 📔 Docs

* document arbitrary configuration field ([7af6e05](https://github.com/Randsw/schema-registry-operator-strimzi/commit/7af6e05beeaf66004a2367b3d6eaec24b0625d0f))
* document clientauth and REST BASIC/RBAC auth ([ee4750e](https://github.com/Randsw/schema-registry-operator-strimzi/commit/ee4750ef1eb0b01b172bb7fbe1475d8f6d012b30))
* document storage and identity parameters ([6233eec](https://github.com/Randsw/schema-registry-operator-strimzi/commit/6233eecc6db9b0cebab4fda511e24f11d5fb31d3))
* observability and single-replica notes ([6ebc4bc](https://github.com/Randsw/schema-registry-operator-strimzi/commit/6ebc4bcdfc20cd025988c3ba0937cd8d7455c778))


### 🦊 CI/CD

* **deps:** Bump github.com/scholzj/strimzi-go from 0.11.0 to 0.12.0 ([bb52453](https://github.com/Randsw/schema-registry-operator-strimzi/commit/bb52453c481be4357a0ac0661a7ffc039dd89618))
* **deps:** Bump software.sslmate.com/src/go-pkcs12 from 0.7.3 to 0.7.4 ([6a2b8d2](https://github.com/Randsw/schema-registry-operator-strimzi/commit/6a2b8d2313341d0c191f8d8625694fcba9f1f2ed))


### 🧪 Tests

* cover clientauth rendering, validation and truststore ([9aa30d4](https://github.com/Randsw/schema-registry-operator-strimzi/commit/9aa30d440aa948d513bf9c25cc8efe7fb1b123a6))
* cover configuration rendering and hashing ([6ddb83a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/6ddb83ac94765ec5cec80f86f04f09d849148a24))
* cover phase 4 observability and polish ([973edf9](https://github.com/Randsw/schema-registry-operator-strimzi/commit/973edf99bc25521e8e2709ee78675187ec4261eb))
* cover storage and identity parameter wiring ([43100cb](https://github.com/Randsw/schema-registry-operator-strimzi/commit/43100cbca222f09070b7b9ef3b1c0c62b37111ce))


### 🚀 Features

* regenerate CRD manifests and sync helm chart ([0d7e44f](https://github.com/Randsw/schema-registry-operator-strimzi/commit/0d7e44f7706ef2e5d390c65ef5455e75a0603959))
* regenerate CRD manifests and sync helm chart ([c07dbce](https://github.com/Randsw/schema-registry-operator-strimzi/commit/c07dbce9bbb501f6a029c744106c78c0c800a30b))
* regenerate CRD manifests and sync helm chart ([604ae6a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/604ae6aff734eeab0366eec3a776bd4ff9603322))
* regenerate CRD manifests and sync helm chart ([1899f90](https://github.com/Randsw/schema-registry-operator-strimzi/commit/1899f90a2713d12b00263b7811ebf8ae3a89b2f3))
* **api:** add clientauth field ([2db70c2](https://github.com/Randsw/schema-registry-operator-strimzi/commit/2db70c29cf70a5b07a921c2be1eb3142749cc82b))
* **api:** add configuration map field ([7ba7b30](https://github.com/Randsw/schema-registry-operator-strimzi/commit/7ba7b301df3c823408ecf5bdc90e33adb2ac7376))
* **api:** add service passthrough and networkpolicy spec fields ([6c6dacc](https://github.com/Randsw/schema-registry-operator-strimzi/commit/6c6dacca509eb7dd7daedb37d179ea5fe5fdedd8))
* **api:** add storage and identity spec fields ([72dce6b](https://github.com/Randsw/schema-registry-operator-strimzi/commit/72dce6bb8ffb0529441dc6f7f25f3d1fecbdf4f0))
* **controller:** validate clientauth and rotate REST truststore ([7f1057b](https://github.com/Randsw/schema-registry-operator-strimzi/commit/7f1057bb4e28eacea9bb79c997e1a1022c954144))
* **k8s:** render arbitrary configuration with reserved-key protection ([778046a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/778046a140e630d2994e21b269c1b7eb02870ac2))
* **k8s:** render REST mTLS with clientauth ([a5d9857](https://github.com/Randsw/schema-registry-operator-strimzi/commit/a5d9857e950012afa21488f28283f2d3cb72f0e0))
* **k8s:** render service annotations/labels and networkpolicy ([eee2ce9](https://github.com/Randsw/schema-registry-operator-strimzi/commit/eee2ce99c5541e187c912ad99d8ce6d2a4bf8652))
* **k8s:** wire storage/identity params into pod env and spec hash ([0fcc916](https://github.com/Randsw/schema-registry-operator-strimzi/commit/0fcc916fae2ebba4c635c7ef35710123e22aadb6))


### 🛠 Fixes

* bump app and helm chart versions in a single semantic-release commit ([0159184](https://github.com/Randsw/schema-registry-operator-strimzi/commit/015918430c141439c7317c91932ec230b986e00c))
* trigger app and helm chart version bump verification ([7ce029f](https://github.com/Randsw/schema-registry-operator-strimzi/commit/7ce029fbf399216662b04ac49ad1ebdb490e0a52))
* **controller:** rename finalizer with migration ([2424a98](https://github.com/Randsw/schema-registry-operator-strimzi/commit/2424a98beb644434052c8765f07209d15d027cbd))

## [1.10.1](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.10.0...1.10.1) (2026-10-19)


### 🦊 CI/CD

* fix golangci-lint v2 install path ([640c68d](https://github.com/Randsw/schema-registry-operator-strimzi/commit/640c68dc302a0237a79a6ef32356e0f868c2374e))
* **deps:** Bump github.com/prometheus/client_golang ([1bf8e91](https://github.com/Randsw/schema-registry-operator-strimzi/commit/1bf8e91bd356f29ff58cf296d2dfb38003b648e5))
* **deps:** Bump helm/kind-action from 1.15.0 to 1.15.1 ([21fa5a1](https://github.com/Randsw/schema-registry-operator-strimzi/commit/21fa5a1d4ec0d84f06676ee1fc12ee5a0c53e3b7))


### 🧪 Tests

* cover phase 0 correctness fixes ([1afa594](https://github.com/Randsw/schema-registry-operator-strimzi/commit/1afa5948c6723d35b1716e2e27ba8662f8feb117))


### 🛠 Fixes

* **controller:** correct condition reason and message formatting ([359bdf4](https://github.com/Randsw/schema-registry-operator-strimzi/commit/359bdf4d0a28a02c942b240caa31a6dbb8f197ed))
* **k8s:** apply resource defaults per-field ([42c75fd](https://github.com/Randsw/schema-registry-operator-strimzi/commit/42c75fdf4365259f201c53ea6c5520770e835b17))
* **k8s:** remove dead probe initial delays ([5bff905](https://github.com/Randsw/schema-registry-operator-strimzi/commit/5bff9054cf7d65237ed9b7a2d6da5145b424aadc))
* **status:** avoid unconditional status update reconcile hot loop ([866adf4](https://github.com/Randsw/schema-registry-operator-strimzi/commit/866adf4497d0fb31e24727cbc62472ff1eda2dcd))

## [1.10.0](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.9.3...1.10.0) (2026-10-13)


### 📔 Docs

* document SASL security protocols and add sample CRs (Step 7) ([fe1f9cb](https://github.com/Randsw/schema-registry-operator-strimzi/commit/fe1f9cb353bcdab3c622be9aa1c4bdaa72cb9a7b))
* update README to Strimzi Kafka API v1 ([0f1265e](https://github.com/Randsw/schema-registry-operator-strimzi/commit/0f1265e512c107e753655d54fb2692e2d1fb65ea))


### 🦊 CI/CD

* **deps:** Bump docker/login-action from 4 to 4.5.2 ([877ad25](https://github.com/Randsw/schema-registry-operator-strimzi/commit/877ad2508f2873019673a469f1280e58261eab7b))
* **deps:** Bump docker/login-action from 4.5.2 to 4.6.0 ([9ab2752](https://github.com/Randsw/schema-registry-operator-strimzi/commit/9ab2752ffd69df6f5c8f65752fc23445e2418605))
* **deps:** Bump github.com/go-logr/logr from 1.4.3 to 1.4.4 ([750cece](https://github.com/Randsw/schema-registry-operator-strimzi/commit/750cece2cfd777ee6f47c18b78dd5c711dc3a291))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.32.0 to 2.32.1 ([7677a61](https://github.com/Randsw/schema-registry-operator-strimzi/commit/7677a61efc1faf1db2d6f3d7ac1caf470a06b2c3))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.32.1 to 2.32.2 ([cebe3e4](https://github.com/Randsw/schema-registry-operator-strimzi/commit/cebe3e4debcacc5f67e18ae6591ac851ef41fd6b))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.32.2 to 2.33.0 ([54fe845](https://github.com/Randsw/schema-registry-operator-strimzi/commit/54fe8453d77ec0f9e27ce3c9c9e3ccd4b4f79237))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.33.0 to 2.33.1 ([d8d05a6](https://github.com/Randsw/schema-registry-operator-strimzi/commit/d8d05a6699ad9c3d540b1b812585caa73477a37a))
* **deps:** Bump github.com/onsi/gomega from 1.42.1 to 1.43.0 ([d910d9a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/d910d9a8d625dfdeda59ae20899c0f7a42252cc5))
* **deps:** Bump github.com/onsi/gomega from 1.43.0 to 1.43.1 ([eaaf3c5](https://github.com/Randsw/schema-registry-operator-strimzi/commit/eaaf3c58ef6299c8b8b325a682541925c8a24a1f))
* **deps:** Bump github.com/onsi/gomega from 1.43.1 to 1.44.0 ([e5aa984](https://github.com/Randsw/schema-registry-operator-strimzi/commit/e5aa9845200ceaa69a4f2523f93a1927a9f634dc))
* **deps:** Bump github.com/prometheus/client_golang ([84c96c0](https://github.com/Randsw/schema-registry-operator-strimzi/commit/84c96c05ffd8d111f625b6db90a2c5e9a696f866))
* **deps:** Bump github.com/prometheus/client_golang ([1f6a5c6](https://github.com/Randsw/schema-registry-operator-strimzi/commit/1f6a5c655a94f3c4377b39e2a142380fa8824a07))
* **deps:** Bump github.com/scholzj/strimzi-go from 0.10.0 to 0.11.0 ([3ba0789](https://github.com/Randsw/schema-registry-operator-strimzi/commit/3ba07890a4bc2fcc01460fddc2aac9f8d7d87fcf))
* **deps:** Bump github.com/scholzj/strimzi-go from 0.10.0 to 0.11.0 ([0104b7d](https://github.com/Randsw/schema-registry-operator-strimzi/commit/0104b7dc23a625572a2b54441cc537f540350216))
* **deps:** Bump golang from 1.26 to 1.27 ([9c2ea9c](https://github.com/Randsw/schema-registry-operator-strimzi/commit/9c2ea9c5e4f0f3d27b4b58b56e36108b8fe6987d))
* **deps:** Bump helm/chart-testing-action from 2.8.0 to 2.9.0 ([cd0cb48](https://github.com/Randsw/schema-registry-operator-strimzi/commit/cd0cb48f855732ae9ea8419db2157474e7128ed3))
* **deps:** Bump helm/kind-action from 1.14.0 to 1.15.0 ([ccff982](https://github.com/Randsw/schema-registry-operator-strimzi/commit/ccff9827c539c5a577bc63333e5e843346064a42))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.36.2 to 0.36.3 ([ac42489](https://github.com/Randsw/schema-registry-operator-strimzi/commit/ac42489835b3d5f9ca733f9b82c65af22b393289))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.36.3 to 0.36.4 ([997287e](https://github.com/Randsw/schema-registry-operator-strimzi/commit/997287e6796af552b1a9085053b63c6af0d84970))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.36.4 to 0.37.0 ([938c985](https://github.com/Randsw/schema-registry-operator-strimzi/commit/938c98509207527dbad5a5353b3d690f80097d17))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.37.0 to 0.37.1 ([f4fdfde](https://github.com/Randsw/schema-registry-operator-strimzi/commit/f4fdfdebf068a7e4917e7b5903ada2ab6236e18e))
* **deps:** Bump k8s.io/client-go from 0.36.4 to 0.37.0 ([1e6d56d](https://github.com/Randsw/schema-registry-operator-strimzi/commit/1e6d56da0a81a28fb5750f5f6b3b66c73bf98fa5))
* **deps:** Bump sigs.k8s.io/controller-runtime from 0.24.1 to 0.25.0 ([cf6ab4a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/cf6ab4ad8b773e5385c65c6c244c2862450e1a8c))
* **deps:** Bump sigs.k8s.io/controller-runtime from 0.25.0 to 0.25.1 ([b36e738](https://github.com/Randsw/schema-registry-operator-strimzi/commit/b36e738024925009765c3d371e2cc77e0d770f72))
* **deps:** Bump sigs.k8s.io/controller-runtime from 0.25.1 to 0.25.2 ([e2d06ad](https://github.com/Randsw/schema-registry-operator-strimzi/commit/e2d06ad7f7f34759d28da80f17925a7a01881cb9))
* **docker:** improve Dockerfile for multi-arch, size and reproducibility ([c8c6de9](https://github.com/Randsw/schema-registry-operator-strimzi/commit/c8c6de98117dc83419bddedd07608389a8cccbac))
* **docker:** keep trailing newline in .dockerignore ([e6aea5a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/e6aea5a8fdb160b1f22651921f8c952ed34cd33f))


### 🧪 Tests

* **controller:** add SASL/SCRAM auth unit and envtest coverage (Step 6) ([4c79152](https://github.com/Randsw/schema-registry-operator-strimzi/commit/4c79152274d35f42a11dc6282f15f78aed1a89d8))


### 🚀 Features

* add SASL auth support with PKCS8 cert keys and envtest e2e ([3bd3824](https://github.com/Randsw/schema-registry-operator-strimzi/commit/3bd3824767f9421ffadb349d57676450bd304501))
* **controller:** add auth resolver and KafkaUser lookup for SASL/SCRAM (Step 2) ([7f042e7](https://github.com/Randsw/schema-registry-operator-strimzi/commit/7f042e7ea78f4b1736a6cb794b8b4516d5a23fb2))
* **controller:** branch deployment pod spec for SASL/SSL security protocols (Step 4) ([3b78449](https://github.com/Randsw/schema-registry-operator-strimzi/commit/3b78449d4e1878ddbe52a71d4ee65f9437294271))
* **controller:** branch secret creation for SASL/SSL security protocols (Step 3) ([fcf244e](https://github.com/Randsw/schema-registry-operator-strimzi/commit/fcf244eb4b1e4808e176ac04afdf944e1218f56a))
* **controller:** rework secret rotation for SASL/SSL modes (Step 5) ([44f54af](https://github.com/Randsw/schema-registry-operator-strimzi/commit/44f54af75d704406b2efa470e83e4eede9057ab6))
* **crd:** add SASL security protocol values to SecurityProtocol enum (Step 1) ([1638e79](https://github.com/Randsw/schema-registry-operator-strimzi/commit/1638e792c1e9bf2c404f4d352ebdceb15e22beea))

## [1.9.3](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.9.2...1.9.3) (2026-07-21)


### 🛠 Fixes

* Fix lint issue ([ac983ac](https://github.com/Randsw/schema-registry-operator-strimzi/commit/ac983acb46ff91353e09d01440c63470e1c0cf4b))
* Improve security ([0d361e6](https://github.com/Randsw/schema-registry-operator-strimzi/commit/0d361e611da21c17c4d9171adf5633c196225359))

## [1.9.2](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.9.1...1.9.2) (2026-07-21)


### 🛠 Fixes

* Improve code ([9dd23a5](https://github.com/Randsw/schema-registry-operator-strimzi/commit/9dd23a5a1b4c0eedeb91aa8925ce873a3c498f80))

## [1.9.1](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.9.0...1.9.1) (2026-07-21)


### 🛠 Fixes

* fix minor bugs ([8a3ee02](https://github.com/Randsw/schema-registry-operator-strimzi/commit/8a3ee024bf02fbdadd9a8604b1c1e7f746f29e4d))

## [1.9.0](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.8.0...1.9.0) (2026-07-20)


### 🦊 CI/CD

* **deps:** Bump actions/setup-python from 6 to 7 ([10033bf](https://github.com/Randsw/schema-registry-operator-strimzi/commit/10033bf60a25156029e7c49d46b7d66cc3919e43))


### 🚀 Features

* **helm:** Use metav1.Conditions in Status instead of string ([535b28f](https://github.com/Randsw/schema-registry-operator-strimzi/commit/535b28f3ab59a4460757520ab9162d893493329f))


### 🛠 Fixes

* Fix critical bugs ([63598e6](https://github.com/Randsw/schema-registry-operator-strimzi/commit/63598e6ca48e4157d9a5a8229f1ee4a23a9224ed))

## [1.8.0](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.7.1...1.8.0) (2026-07-18)


### 🚀 Features

* Use metav1.Conditions in Status instead of string ([126827e](https://github.com/Randsw/schema-registry-operator-strimzi/commit/126827e165293876a35ab78d99e78a76437a2f9c))

## [1.7.1](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.7.0...1.7.1) (2026-07-18)


### 💈 Style

* Rename another var in Go style ([c43bd8a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/c43bd8a435dca92e46e6ecdc8b44c10e0be68b7d))
* Rename var in Go style ([53273ee](https://github.com/Randsw/schema-registry-operator-strimzi/commit/53273ee5a9472819b549380f02cc972cd8365312))


### 🛠 Fixes

* Fix metrics var name ([19510c3](https://github.com/Randsw/schema-registry-operator-strimzi/commit/19510c31473e46c20162ae274f936204634a2687))
* Fix more typos ([37db20e](https://github.com/Randsw/schema-registry-operator-strimzi/commit/37db20e7b86be89f745cbc5afdef06d5007defaa))
* Fix typos ([168c45b](https://github.com/Randsw/schema-registry-operator-strimzi/commit/168c45b98299ea8b1ce16701fa1333e24761fcf6))
* Remove old func ([88498e0](https://github.com/Randsw/schema-registry-operator-strimzi/commit/88498e0f74ece69b4c1da8ebeccf89038c84a0d6))

## [1.7.0](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.6.1...1.7.0) (2026-07-18)


### 🧪 Tests

* **certprocessor:** Improve tests ([34bc7fc](https://github.com/Randsw/schema-registry-operator-strimzi/commit/34bc7fc9cb74902932681160d4a99132c5b50316))
* **controller:** Add more tests ([a6eb87b](https://github.com/Randsw/schema-registry-operator-strimzi/commit/a6eb87be993b03b53f4df1736f03d7a271fe3d12))
* **k8s:** Improve tests ([2b2697c](https://github.com/Randsw/schema-registry-operator-strimzi/commit/2b2697ce76cdd60022198242e738b154275858ea))


### 🚀 Features

* Update CRD with default fields values ([907a508](https://github.com/Randsw/schema-registry-operator-strimzi/commit/907a508eb7649d0090f91a8d8766c8702b04bfbc))


### 🛠 Fixes

* Allow TLSSecretName be empty ([4ea194a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/4ea194ae7f32b508654b91cd75b5bc893ba0613a))

## [1.6.1](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.6.0...1.6.1) (2026-07-17)


### 🛠 Fixes

* Add default values to spec field ([d03fdbc](https://github.com/Randsw/schema-registry-operator-strimzi/commit/d03fdbc9281b1af0f191e53c8aadd2e2885a1d67))
* Add spec field validation ([c49e806](https://github.com/Randsw/schema-registry-operator-strimzi/commit/c49e8060883220decfda9e472130a12c6e788aa7))

## [1.6.0](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.5.0...1.6.0) (2026-07-17)


### 🦊 CI/CD

* **deps:** Bump actions/setup-go from 6 to 7 ([6fafe33](https://github.com/Randsw/schema-registry-operator-strimzi/commit/6fafe33af73c286dd26aabc25e336bf18376fe59))


### 🚀 Features

* Upgrade deployment and service after CRD spec update ([fdd7529](https://github.com/Randsw/schema-registry-operator-strimzi/commit/fdd75295ac8852fbc3991624cf9867fab9f78e5d))
* **helm-chart:** Upgrade CRD in chart ([89ff562](https://github.com/Randsw/schema-registry-operator-strimzi/commit/89ff5628ec93273ba29b9f47c55f112a13938873))


### 🛠 Fixes

* Check if annotation is nil to avoid panic ([64b15b0](https://github.com/Randsw/schema-registry-operator-strimzi/commit/64b15b06b2ff08745f5ff0e33fdf9b51e6aa3298))

## [1.5.0](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.4.5...1.5.0) (2026-07-17)


### 🚀 Features

* Add probes to generated deployment. Java HeapOpt now configured from CRD ([f8c26e8](https://github.com/Randsw/schema-registry-operator-strimzi/commit/f8c26e85c59d085d66dbb636f1b41a1310e0e405))


### 🛠 Fixes

* Add scheme to probe. Fix typo in json field naming ([27f54d6](https://github.com/Randsw/schema-registry-operator-strimzi/commit/27f54d69753addf431152545c88ccd79e4ddd8ed))

## [1.4.5](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.4.4...1.4.5) (2026-07-14)


### 🦊 CI/CD

* **deps:** Bump actions/setup-node from 6 to 7 ([fc49537](https://github.com/Randsw/schema-registry-operator-strimzi/commit/fc49537893023390e47fdc6036d424a09e24705f))


### 🛠 Fixes

* Use Enentually insead of sleep ([dde6934](https://github.com/Randsw/schema-registry-operator-strimzi/commit/dde6934b730988c634c00bfe8c3e81e4febfcd4b))

## [1.4.4](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.4.3...1.4.4) (2026-07-14)


### 🛠 Fixes

* Add check if jksSecret is nil ([0f8cc19](https://github.com/Randsw/schema-registry-operator-strimzi/commit/0f8cc19ca52f0876c18d80507a54004d8c281cb7))
* Controller-runtime in-place mutate object in create function. Read secret to get resourceVersion unnessesary ([c570b5d](https://github.com/Randsw/schema-registry-operator-strimzi/commit/c570b5d679b8008ce2317d946bebcab009424f4b))

## [1.4.3](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.4.2...1.4.3) (2026-07-14)


### 🛠 Fixes

* Add password presence check in secret ([f26f4fb](https://github.com/Randsw/schema-registry-operator-strimzi/commit/f26f4fbb8e6de2d82845f167d111b6c314cd3cec))
* Fix registry version parsing ([37e5940](https://github.com/Randsw/schema-registry-operator-strimzi/commit/37e59408c208e9cdb76c7c87390ff60d0dd3bf45))

## [1.4.2](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.4.1...1.4.2) (2026-07-13)


### 🛠 Fixes

* Return defer func for cert and key files ([c6eaab1](https://github.com/Randsw/schema-registry-operator-strimzi/commit/c6eaab1b459e35a895bffddbbcce9b7933c9f74f))
* Use crypto/rand to generate random bytes ([8f4c797](https://github.com/Randsw/schema-registry-operator-strimzi/commit/8f4c7972d85d733bd3819cb3fe2e921e307c3639))

## [1.4.1](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.4.0...1.4.1) (2026-07-13)


### 🦊 CI/CD

* **deps:** Bump actions/checkout from 6 to 7 ([67bb637](https://github.com/Randsw/schema-registry-operator-strimzi/commit/67bb63785b2ae2eeb087284a977aa182afe519b9))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.28.2 to 2.28.3 ([73e3924](https://github.com/Randsw/schema-registry-operator-strimzi/commit/73e3924dc22627a1af3ea33c14ec212928b7d03c))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.28.3 to 2.29.0 ([2f86687](https://github.com/Randsw/schema-registry-operator-strimzi/commit/2f8668722c93ee2abe73581b86fd23b0317ff1c7))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.29.0 to 2.30.0 ([0544e09](https://github.com/Randsw/schema-registry-operator-strimzi/commit/0544e094b561e227420bda15c5fbc0fa461b74fe))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.30.0 to 2.31.0 ([53880aa](https://github.com/Randsw/schema-registry-operator-strimzi/commit/53880aaebb86270b8693ca94f09d7133c3c876b4))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.31.0 to 2.32.0 ([1300c13](https://github.com/Randsw/schema-registry-operator-strimzi/commit/1300c130b5b93729c2dacabefd133d9c337f0dd2))
* **deps:** Bump github.com/onsi/gomega from 1.40.0 to 1.41.0 ([564b67c](https://github.com/Randsw/schema-registry-operator-strimzi/commit/564b67caadb66b87c2f6032f4d19ebb3b8bdbfa5))
* **deps:** Bump github.com/onsi/gomega from 1.41.0 to 1.42.0 ([1c0f3d7](https://github.com/Randsw/schema-registry-operator-strimzi/commit/1c0f3d7465ec595e9a03f90c654d3de0562cf0ef))
* **deps:** Bump github.com/onsi/gomega from 1.42.0 to 1.42.1 ([4cc969d](https://github.com/Randsw/schema-registry-operator-strimzi/commit/4cc969dbd73d8843c0887a62e1bfa8864b430b64))
* **deps:** Bump github.com/scholzj/strimzi-go from 0.9.0 to 0.10.0 ([38b96dd](https://github.com/Randsw/schema-registry-operator-strimzi/commit/38b96dd491a885c405e9ae92ef758920507f1a46))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.36.0 to 0.36.1 ([edbf68a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/edbf68a785b1bf251c94a87b5f109d05149afcf6))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.36.1 to 0.36.2 ([664eda7](https://github.com/Randsw/schema-registry-operator-strimzi/commit/664eda7f5e28c9bace44efa7fbc3177d28e0399a))
* **deps:** Bump sigs.k8s.io/controller-runtime from 0.24.0 to 0.24.1 ([edf28cf](https://github.com/Randsw/schema-registry-operator-strimzi/commit/edf28cf199c124dd78147618d888e6f5a12ac185))
* **deps:** Bump ubuntu from 24.04 to 26.04 ([60fec0f](https://github.com/Randsw/schema-registry-operator-strimzi/commit/60fec0f71f6b008def9731c0214fd35b81013138))


### 🛠 Fixes

* Register monitoring ([32e23e9](https://github.com/Randsw/schema-registry-operator-strimzi/commit/32e23e958c4e31cabd952ad4ad544e9a2e654698))

## [1.4.0](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.3.0...1.4.0) (2026-05-06)


### 🦊 CI/CD

* **deps:** Bump actions/checkout from 4 to 5 ([ed8ecc8](https://github.com/Randsw/schema-registry-operator-strimzi/commit/ed8ecc819b2478f807e0673c76f7a179fa38787c))
* **deps:** Bump actions/checkout from 5 to 6 ([99d1ad5](https://github.com/Randsw/schema-registry-operator-strimzi/commit/99d1ad50b930b3c62fc9a15c06da3b1209ee059a))
* **deps:** Bump actions/setup-go from 5 to 6 ([3886f88](https://github.com/Randsw/schema-registry-operator-strimzi/commit/3886f889112509a016aa775cdf672b18fabf5a8b))
* **deps:** Bump actions/setup-node from 4 to 5 ([7b7fda7](https://github.com/Randsw/schema-registry-operator-strimzi/commit/7b7fda719d19955a154828f0bc0feade40c2c1a4))
* **deps:** Bump actions/setup-node from 5 to 6 ([573b4cf](https://github.com/Randsw/schema-registry-operator-strimzi/commit/573b4cf70c8d2f70947d9b52e53c9d001373da90))
* **deps:** Bump actions/setup-python from 5 to 6 ([6ec4921](https://github.com/Randsw/schema-registry-operator-strimzi/commit/6ec49211e124df0967c79dea909cd5fcd9890d8b))
* **deps:** Bump azure/setup-helm from 4 to 5 ([2094d01](https://github.com/Randsw/schema-registry-operator-strimzi/commit/2094d01bfef0e69747a8a41f56288decb031f5ce))
* **deps:** Bump docker/build-push-action from 6 to 7 ([5771034](https://github.com/Randsw/schema-registry-operator-strimzi/commit/5771034745839da38acbff20720ab3b171ba2568))
* **deps:** Bump docker/login-action from 3 to 4 ([4c71636](https://github.com/Randsw/schema-registry-operator-strimzi/commit/4c7163676e3b9aef7546d9ccd460ddebc3952d13))
* **deps:** Bump docker/metadata-action from 5 to 6 ([a5bb3dd](https://github.com/Randsw/schema-registry-operator-strimzi/commit/a5bb3ddb91beddb3d595e56f5ed8083fb9e0f1af))
* **deps:** Bump docker/setup-buildx-action from 3 to 4 ([6493bbc](https://github.com/Randsw/schema-registry-operator-strimzi/commit/6493bbc83619b75b8e7e8c326d8a63c6e83b340e))
* **deps:** Bump github.com/go-logr/logr from 1.4.2 to 1.4.3 ([5f1c520](https://github.com/Randsw/schema-registry-operator-strimzi/commit/5f1c520335aa9696d6718de8e97cca4b53deca1b))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.23.4 to 2.24.0 ([0906b13](https://github.com/Randsw/schema-registry-operator-strimzi/commit/0906b1361efc03f6ec7198d4bdf49fc6a35b90b5))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.24.0 to 2.25.0 ([b3119ab](https://github.com/Randsw/schema-registry-operator-strimzi/commit/b3119ab0fa534575c876ebbefaad2bd1c35028f8))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.25.1 to 2.25.2 ([7eac428](https://github.com/Randsw/schema-registry-operator-strimzi/commit/7eac428f0128c7d4462a06b880e467ab75ec905d))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.25.2 to 2.25.3 ([ccedc92](https://github.com/Randsw/schema-registry-operator-strimzi/commit/ccedc92774133f81db5bc4eaf78ed1bf99cf75e4))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.25.3 to 2.26.0 ([79401a5](https://github.com/Randsw/schema-registry-operator-strimzi/commit/79401a557aaa7fec29327c77c020f327fea2cec9))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.26.0 to 2.27.1 ([ae2a0eb](https://github.com/Randsw/schema-registry-operator-strimzi/commit/ae2a0eba4d3db78b2dd41a9828c74455a866cde5))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.27.1 to 2.27.2 ([663145d](https://github.com/Randsw/schema-registry-operator-strimzi/commit/663145d82b0b4879f367ce92bdcde4b4150de392))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.27.2 to 2.27.3 ([a6a0e1c](https://github.com/Randsw/schema-registry-operator-strimzi/commit/a6a0e1c8c0f7ac4d5e3ff5b9104c93aa79878773))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.27.3 to 2.27.4 ([06c11c0](https://github.com/Randsw/schema-registry-operator-strimzi/commit/06c11c0aaf131d822944391c9f282a19575d934b))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.27.4 to 2.27.5 ([74de87f](https://github.com/Randsw/schema-registry-operator-strimzi/commit/74de87f0bae745480586f1a692a67d6cc4a0abe4))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.27.5 to 2.28.1 ([dece8e7](https://github.com/Randsw/schema-registry-operator-strimzi/commit/dece8e75fc9f19ff5a22660b420541277df29ae4))
* **deps:** Bump github.com/onsi/ginkgo/v2 from 2.28.1 to 2.28.2 ([68f31f1](https://github.com/Randsw/schema-registry-operator-strimzi/commit/68f31f15337a97e4e68bc7cfc9e6e5c27015a73e))
* **deps:** Bump github.com/onsi/gomega from 1.37.0 to 1.38.0 ([52a7318](https://github.com/Randsw/schema-registry-operator-strimzi/commit/52a73182e874e59fc20690ed2e53e66d8d100ba4))
* **deps:** Bump github.com/onsi/gomega from 1.38.0 to 1.38.1 ([8fe8d1e](https://github.com/Randsw/schema-registry-operator-strimzi/commit/8fe8d1eb58526de0919444314804dd8eb9d5133e))
* **deps:** Bump github.com/onsi/gomega from 1.38.1 to 1.38.2 ([1622670](https://github.com/Randsw/schema-registry-operator-strimzi/commit/16226708bfe89d4b6538620f1e9c3d4f8fa27d89))
* **deps:** Bump github.com/onsi/gomega from 1.38.2 to 1.38.3 ([cb0fb4f](https://github.com/Randsw/schema-registry-operator-strimzi/commit/cb0fb4f35bdf81dcb547d0d307d4a8aa556d2144))
* **deps:** Bump github.com/onsi/gomega from 1.38.3 to 1.39.0 ([5fa914a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/5fa914aff5e00caa066083090aae3d26a08e79fa))
* **deps:** Bump github.com/onsi/gomega from 1.39.0 to 1.39.1 ([472c2f9](https://github.com/Randsw/schema-registry-operator-strimzi/commit/472c2f9cbe556ca74240171aac6bd7cc1d423561))
* **deps:** Bump github.com/onsi/gomega from 1.39.1 to 1.40.0 ([48e2e56](https://github.com/Randsw/schema-registry-operator-strimzi/commit/48e2e563b0c65887bfb99b2ecee7bb590960381a))
* **deps:** Bump github.com/prometheus/client_golang ([fd2d7ec](https://github.com/Randsw/schema-registry-operator-strimzi/commit/fd2d7ec6b2a4be013eec603ff99e1fb3fb37656e))
* **deps:** Bump github.com/prometheus/client_golang ([fb9db73](https://github.com/Randsw/schema-registry-operator-strimzi/commit/fb9db736e30607e800c50bfdde53cbd1c26938bd))
* **deps:** Bump go.uber.org/zap from 1.27.0 to 1.27.1 ([2a01d05](https://github.com/Randsw/schema-registry-operator-strimzi/commit/2a01d05bba4cb984700fd46a8a799b46ad4c1cb1))
* **deps:** Bump go.uber.org/zap from 1.27.1 to 1.28.0 ([b7868b4](https://github.com/Randsw/schema-registry-operator-strimzi/commit/b7868b4a1b9c2249805a609c7cb0036908c309d5))
* **deps:** Bump golang from 1.24 to 1.25 ([244c439](https://github.com/Randsw/schema-registry-operator-strimzi/commit/244c4392b26cc7b3822f9a0dc24530d51cb672c3))
* **deps:** Bump golang from 1.25 to 1.26 ([1316533](https://github.com/Randsw/schema-registry-operator-strimzi/commit/13165332c24b6312b7e203d555197c7a13fad543))
* **deps:** Bump golangci/golangci-lint-action from 8 to 9 ([bef3d20](https://github.com/Randsw/schema-registry-operator-strimzi/commit/bef3d20d44bb2883a22ef749d98a577e27fa8ead))
* **deps:** Bump helm/chart-testing-action from 2.7.0 to 2.8.0 ([7080cfb](https://github.com/Randsw/schema-registry-operator-strimzi/commit/7080cfbbcdd4b306ca51797df8cc94c00fc41056))
* **deps:** Bump helm/kind-action from 1.12.0 to 1.13.0 ([3092921](https://github.com/Randsw/schema-registry-operator-strimzi/commit/3092921906217d84be93441b7992dbbb228e5753))
* **deps:** Bump helm/kind-action from 1.13.0 to 1.14.0 ([c6d834c](https://github.com/Randsw/schema-registry-operator-strimzi/commit/c6d834c6965242e9a1c03c583bde73f3696f093e))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.33.0 to 0.33.1 ([ce2a229](https://github.com/Randsw/schema-registry-operator-strimzi/commit/ce2a229f095ad2deb9c5f33af9d8440801fc0b97))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.33.1 to 0.33.2 ([5da6fca](https://github.com/Randsw/schema-registry-operator-strimzi/commit/5da6fca650cc1c8fb2b84829827f85c258206d70))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.33.2 to 0.33.3 ([2d66237](https://github.com/Randsw/schema-registry-operator-strimzi/commit/2d66237ff9745d61595fe1b57aab46817277ddaa))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.33.3 to 0.33.4 ([3b3681b](https://github.com/Randsw/schema-registry-operator-strimzi/commit/3b3681b3dfe7109d5832dafa8783a0147a691e04))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.33.4 to 0.34.0 ([71e92d4](https://github.com/Randsw/schema-registry-operator-strimzi/commit/71e92d441a7bb9a23fe715b8910f1bf36efc9678))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.34.0 to 0.34.1 ([8ce32ab](https://github.com/Randsw/schema-registry-operator-strimzi/commit/8ce32ab143a4d75fe2f754f42beaccb6dab774ee))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.34.1 to 0.34.2 ([e96506c](https://github.com/Randsw/schema-registry-operator-strimzi/commit/e96506c90053223e49a18870ab839bb26470a572))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.34.2 to 0.34.3 ([aebc615](https://github.com/Randsw/schema-registry-operator-strimzi/commit/aebc6154dc2baeb4794bb03d400081116b347a76))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.34.3 to 0.35.0 ([47df7cf](https://github.com/Randsw/schema-registry-operator-strimzi/commit/47df7cf411df4b3126d947783c97c66bf22268e8))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.35.0 to 0.35.1 ([3e58e65](https://github.com/Randsw/schema-registry-operator-strimzi/commit/3e58e6513f9fec35dccef9e3c2bbf9c936e373d1))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.35.1 to 0.35.2 ([cc8b0de](https://github.com/Randsw/schema-registry-operator-strimzi/commit/cc8b0de11008f0783e5a8e61361006cdae705b01))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.35.2 to 0.35.3 ([496d7a0](https://github.com/Randsw/schema-registry-operator-strimzi/commit/496d7a07ac013547a5d7b76cd0c40f9239b158d4))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.35.3 to 0.35.4 ([6237fdd](https://github.com/Randsw/schema-registry-operator-strimzi/commit/6237fdd85418de8f019b3a5b1c3f99fa7d2248df))
* **deps:** Bump sigs.k8s.io/controller-runtime from 0.20.4 to 0.21.0 ([d4f1370](https://github.com/Randsw/schema-registry-operator-strimzi/commit/d4f13702469ae18fe2f76c7d207c8640af440eac))
* **deps:** Bump sigs.k8s.io/controller-runtime from 0.21.0 to 0.22.0 ([a52f445](https://github.com/Randsw/schema-registry-operator-strimzi/commit/a52f4457a73d79c71450d7af0ac81d5987d55761))
* **deps:** Bump sigs.k8s.io/controller-runtime from 0.22.0 to 0.22.1 ([18f8a3d](https://github.com/Randsw/schema-registry-operator-strimzi/commit/18f8a3d701b2c8ef6c3a1a489dd6c5af71cbf212))
* **deps:** Bump sigs.k8s.io/controller-runtime from 0.22.1 to 0.22.2 ([28dc257](https://github.com/Randsw/schema-registry-operator-strimzi/commit/28dc2571d829570cbb7491556ac9c484d37d9721))
* **deps:** Bump sigs.k8s.io/controller-runtime from 0.22.2 to 0.22.3 ([8bbbab5](https://github.com/Randsw/schema-registry-operator-strimzi/commit/8bbbab556662c2fffb44e81ef3649531b8be35ad))
* **deps:** Bump sigs.k8s.io/controller-runtime from 0.22.3 to 0.22.4 ([822c138](https://github.com/Randsw/schema-registry-operator-strimzi/commit/822c138212df2bfe03f966943382fa296f35ef0a))
* **deps:** Bump sigs.k8s.io/controller-runtime from 0.22.4 to 0.23.0 ([e713782](https://github.com/Randsw/schema-registry-operator-strimzi/commit/e713782335cdbcb497733eb80803c954b91d8962))
* **deps:** Bump sigs.k8s.io/controller-runtime from 0.23.0 to 0.23.1 ([872461d](https://github.com/Randsw/schema-registry-operator-strimzi/commit/872461d935be9a21ff13706d604ac1a794589f3b))
* **deps:** Bump sigs.k8s.io/controller-runtime from 0.23.1 to 0.23.2 ([2029924](https://github.com/Randsw/schema-registry-operator-strimzi/commit/20299247c0b2634edebf679a64b92e3ac55061b6))
* **deps:** Bump sigs.k8s.io/controller-runtime from 0.23.2 to 0.23.3 ([6135ce5](https://github.com/Randsw/schema-registry-operator-strimzi/commit/6135ce5c3e5ce5374e7b63b3d9bcb0ee1f5b2cde))
* **deps:** Bump softprops/action-gh-release from 2 to 3 ([9e5c396](https://github.com/Randsw/schema-registry-operator-strimzi/commit/9e5c396318c47d47e6a33e1bba3e717f4f377c9a))
* **deps:** Bump tj-actions/changed-files from 46 to 47 ([3a13289](https://github.com/Randsw/schema-registry-operator-strimzi/commit/3a13289afc7e2891f28de87d06837c01aa8f8118))
* **helm:** Add emptydir volume to operator deployment ([1aac4e7](https://github.com/Randsw/schema-registry-operator-strimzi/commit/1aac4e7c4752be12ed784fc444e9c861a174f29e))


### 🚀 Features

* **controller-gen:** Remove deprecated scheme.Builder ([ea81a55](https://github.com/Randsw/schema-registry-operator-strimzi/commit/ea81a5599b8517646e358ed0020089692ec33217))


### 🛠 Fixes

* **strimzi-kafka:** Update strimzi kafka api spec to v1 ([01a028d](https://github.com/Randsw/schema-registry-operator-strimzi/commit/01a028dac078925f050a2e424cd757003dbbef68))


### Other

* Bump controller-toll version to 0.16.5 ([841b303](https://github.com/Randsw/schema-registry-operator-strimzi/commit/841b303ee5266cc9438833f8219382dd8e8489e6))
* **deps:** Bump golang to 1.26 ([90a0989](https://github.com/Randsw/schema-registry-operator-strimzi/commit/90a0989be93b685a2cf7f2a52da65d0e9f8901b9))
* **deps:** Bump golang version to 1.25 ([8a2fa81](https://github.com/Randsw/schema-registry-operator-strimzi/commit/8a2fa81b60beac9f3197f99caea2432591379de2))

## [1.3.0](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.2.0...1.3.0) (2025-05-06)


### :scissors: Refactor

* Rename key password in TLS secret ([4a5c72c](https://github.com/Randsw/schema-registry-operator-strimzi/commit/4a5c72c07ab56a127b4edee79a19de69a27cef3b))


### 🦊 CI/CD

* **deps:** Bump golangci/golangci-lint-action from 7 to 8 ([a1d33e6](https://github.com/Randsw/schema-registry-operator-strimzi/commit/a1d33e67351b1b1b86cddc965073146e177ff1f6))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.32.4 to 0.33.0 ([9e27b55](https://github.com/Randsw/schema-registry-operator-strimzi/commit/9e27b55cd5b0035176db3b2f2ef8e0f1346d24e7))


### 🧪 Tests

* Add test - Renew TLS secret after kafka cluster CA is updated ([360141f](https://github.com/Randsw/schema-registry-operator-strimzi/commit/360141ff26c260976e284c3cea8928e89e9a47e7))


### 🚀 Features

* Add status field to CRD. Set to Ok if schema registry pod is running. Not ready otherwise ([1e98728](https://github.com/Randsw/schema-registry-operator-strimzi/commit/1e9872823c1f145c2019fa3b4570a7c280530329))


### 🛠 Fixes

* Print status in get request ([3e2cd81](https://github.com/Randsw/schema-registry-operator-strimzi/commit/3e2cd8111b60b3bb75f21b3bcf06b2b2cd7bcf3d))


### Other

* **deps:** Bump golang version ([68b2727](https://github.com/Randsw/schema-registry-operator-strimzi/commit/68b2727a98a422329cc57e7afeb0cba9724ef40d))

## [1.2.0](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.1.4...1.2.0) (2025-04-24)


### 🚀 Features

* Renew REST API TLS secret if cluster CA cert is changed ([a90bff4](https://github.com/Randsw/schema-registry-operator-strimzi/commit/a90bff437af2f905818ca7762366680dad3a3bef))

## [1.1.4](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.1.3...1.1.4) (2025-04-23)


### :scissors: Refactor

* Move all CRUD kubernetes resource operation in separate file ([38bc187](https://github.com/Randsw/schema-registry-operator-strimzi/commit/38bc1879d5d806fd58bdc49f8b55a493fe588663))
* Refactor certProccessor ([2215823](https://github.com/Randsw/schema-registry-operator-strimzi/commit/22158233d62d5c11ad1ddda10535954e6f8ea3db))
* Refactor some function ([fbd05d8](https://github.com/Randsw/schema-registry-operator-strimzi/commit/fbd05d8125a597b4b0ac3faf76431bc1342585e7))
* Refactoring the Reconcile function ([48bbec7](https://github.com/Randsw/schema-registry-operator-strimzi/commit/48bbec7b239f80819946deac185b8b8f38b8731c))


### 🦊 CI/CD

* **deps:** Bump k8s.io/apiextensions-apiserver from 0.32.3 to 0.32.4 ([794ac8e](https://github.com/Randsw/schema-registry-operator-strimzi/commit/794ac8ef2bd5e133f05602687a1d6948b47fc97f))


### 🧪 Tests

* Add test to check if JKS secret is updated after update on Cluster CA ca cert secret or User secret ([62a2b9a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/62a2b9a5f519e5d6d63edcb0395776622034cacc))


### 🛠 Fixes

* Add verbosity level to logs ([ac58e51](https://github.com/Randsw/schema-registry-operator-strimzi/commit/ac58e51317302d1af5792b7def17a920fc532e47))
* Move some message to debug level. Expand logger config ([9ced352](https://github.com/Randsw/schema-registry-operator-strimzi/commit/9ced3523439d9f4123d2bee13369ae02b7e0751a))
* Remove level colorization in logger ([b857211](https://github.com/Randsw/schema-registry-operator-strimzi/commit/b857211f40210f01c0d1ff45e0a0b44db70fc481))
* Set default level to info. Add more fields to log message ([936c412](https://github.com/Randsw/schema-registry-operator-strimzi/commit/936c4123d8d3b907f78982903881b050310ac810))

## [1.1.3](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.1.2...1.1.3) (2025-04-17)


### 🛠 Fixes

* Add timeout after write secret and before read. Add return block if error is occured ([6d982eb](https://github.com/Randsw/schema-registry-operator-strimzi/commit/6d982ebe6301b58259a1c1d9a5c77a6d08f075ac))
* Improve log message ([0aee7ea](https://github.com/Randsw/schema-registry-operator-strimzi/commit/0aee7eabb7b706ed6dffa274a80c2e938e8282e8))
* Remove logs from setupManager. Add logs about secret creation success. ([ab228a1](https://github.com/Randsw/schema-registry-operator-strimzi/commit/ab228a1ae93c220aac7cf1797f697ba8cc5f7749))

## [1.1.2](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.1.1...1.1.2) (2025-04-17)


### 📔 Docs

* Add badges ([f0e8e5c](https://github.com/Randsw/schema-registry-operator-strimzi/commit/f0e8e5c485a3aa58a95d97f37a1762394aa026e2))
* Add description of TLS secret format ([817f09e](https://github.com/Randsw/schema-registry-operator-strimzi/commit/817f09e7ffd39da52747c00ec377cd7f58587d60))
* Fix badges url ([c624063](https://github.com/Randsw/schema-registry-operator-strimzi/commit/c6240637b63536888f6193e6e82f1c56b9bd5d33))
* Update header ([7b559cd](https://github.com/Randsw/schema-registry-operator-strimzi/commit/7b559cd086b8cf0c034fa4c76ad62daa21b33f2e))


### 🦊 CI/CD

* Fix chart release conditions ([675d49d](https://github.com/Randsw/schema-registry-operator-strimzi/commit/675d49d117a5f9c7afa09b996db11914a94c147d))
* Trigger action when tpl file is changed ([c7f0924](https://github.com/Randsw/schema-registry-operator-strimzi/commit/c7f0924a7f1db510053e9fec6f652818efae0bcc))


### 🧪 Tests

* Fix linter error ([0ce45e8](https://github.com/Randsw/schema-registry-operator-strimzi/commit/0ce45e8c45556e61c56e59164c4b104bece3bca3))


### 🛠 Fixes

* Update CRD in helm chart ([05f6fff](https://github.com/Randsw/schema-registry-operator-strimzi/commit/05f6fff076290e627418c5a45391a979e3893d03))

## [1.1.1](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.1.0...1.1.1) (2025-04-16)


### 📔 Docs

* add overview and motivation part ([bc93a9e](https://github.com/Randsw/schema-registry-operator-strimzi/commit/bc93a9e0fe5d39e03065d36b632032167b4fa7e5))
* Finish documentation ([33aff41](https://github.com/Randsw/schema-registry-operator-strimzi/commit/33aff414e8485f1965420adb25184d6493a2bacd))
* Start motivation part ([5fac66a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/5fac66a573ad1145d754955fd786094ec372bd9e))


### 🛠 Fixes

* Remove unsued CRD field ([4c9b35f](https://github.com/Randsw/schema-registry-operator-strimzi/commit/4c9b35f106cbcbd67967f3183fa9b95679efa30f))

## [1.1.0](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.0.3...1.1.0) (2025-04-15)


### 🦊 CI/CD

* **deps:** Bump golang from 1.23 to 1.24 ([6f7fbe8](https://github.com/Randsw/schema-registry-operator-strimzi/commit/6f7fbe8f4f45a341f5fa1b50002bd388d0c17b53))
* **deps:** Bump k8s.io/apiextensions-apiserver from 0.32.1 to 0.32.3 ([448ca19](https://github.com/Randsw/schema-registry-operator-strimzi/commit/448ca19131a15bb4fef36b86fe095dbe53ff0ba7))
* Build commit not trigger release ([bca60a9](https://github.com/Randsw/schema-registry-operator-strimzi/commit/bca60a9931848c44d709865d636c149289454d78))
* Use go 1.24 in github actions ([962b57e](https://github.com/Randsw/schema-registry-operator-strimzi/commit/962b57e32c3381b3a94f83a7fc51732941dafe68))


### 🚀 Features

* Generate keyystore for schema registry TLS ([5150c93](https://github.com/Randsw/schema-registry-operator-strimzi/commit/5150c93c962c9057cdd7383540e1de6a322524dd))
* TLS secret create for schema-registry ([b61d1a3](https://github.com/Randsw/schema-registry-operator-strimzi/commit/b61d1a306e474ad40c2c68570b4615c8a198a039))


### 🛠 Fixes

* Add file close ([4047667](https://github.com/Randsw/schema-registry-operator-strimzi/commit/40476674c37efc35cba283d0516c82b6ee6cde70))
* Add service name without namespace to TLS cert SAN ([71055cf](https://github.com/Randsw/schema-registry-operator-strimzi/commit/71055cfbebf095fd73b5c410309fbeafafef8434))
* Check if secret not null before creation. Fix service ([c94a354](https://github.com/Randsw/schema-registry-operator-strimzi/commit/c94a35413ca9adfb9d49c978cffa5b30f73288d4))


### Other

* Update package ([c32a3bd](https://github.com/Randsw/schema-registry-operator-strimzi/commit/c32a3bdedf8348d8b358b6be8e0bc35a95ddc374))

## [1.0.3](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.0.2...1.0.3) (2025-04-14)


### 🦊 CI/CD

* **deps:** Bump github.com/onsi/gomega from 1.36.1 to 1.37.0 ([0ba99ab](https://github.com/Randsw/schema-registry-operator-strimzi/commit/0ba99ab28fc9f14c9e0cf1cbfa4d0cdba43ec51b))


### Other

* **deps:** bump github.com/onsi/ginkgo/v2 from 2.22.0 to 2.23.4 ([f8e92cc](https://github.com/Randsw/schema-registry-operator-strimzi/commit/f8e92cc4f638b4d046358ed418844ac65914c833))
* **deps:** bump github.com/prometheus/client_golang ([50f640d](https://github.com/Randsw/schema-registry-operator-strimzi/commit/50f640d915aac962f9b345fbd617ce47e3046273))

## [1.0.2](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.0.1...1.0.2) (2025-04-14)


### 🦊 CI/CD

* Check actor ([256ca27](https://github.com/Randsw/schema-registry-operator-strimzi/commit/256ca2753d9c995c73dc8627d25e4a9c31f47154))
* Rewrite if expression ([302056a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/302056af0001663b9ab3a143e1e6973017b4be97))
* Skip test deploy if dependabot ([2afb017](https://github.com/Randsw/schema-registry-operator-strimzi/commit/2afb01708d810b48535cb00609b5fe03c51b8853))


### 🛠 Fixes

* remove option in secret listing ([24f965f](https://github.com/Randsw/schema-registry-operator-strimzi/commit/24f965f0bbff9d609dc795a160f15d84ece3fac5))
* Rewrite code for secret change ([762e592](https://github.com/Randsw/schema-registry-operator-strimzi/commit/762e592e630cfeb7133149a2fcad6650838903c1))

## [1.0.1](https://github.com/Randsw/schema-registry-operator-strimzi/compare/1.0.0...1.0.1) (2025-04-05)


### 📔 Docs

* Add TODO's ([b91be51](https://github.com/Randsw/schema-registry-operator-strimzi/commit/b91be51e7e410dc32f233135afcba953bc43cbec))


### 🦊 CI/CD

* Build and push test image with SHA tag ([4b11601](https://github.com/Randsw/schema-registry-operator-strimzi/commit/4b11601da8abe49e25089d3092ac6db7ce2f8a1f))
* Fix error while release if develop branch not exist ([080c05b](https://github.com/Randsw/schema-registry-operator-strimzi/commit/080c05b009a07ed753cc32c574526307fcbe7104))
* Fix helm linter config ([a55c713](https://github.com/Randsw/schema-registry-operator-strimzi/commit/a55c713577c583168d78d49293446e32bbbe0d60))
* Fix helm linter errors ([ce86705](https://github.com/Randsw/schema-registry-operator-strimzi/commit/ce867057ca029c8da16526bff003f8660b486942))
* Testing helm linter action ([223bd79](https://github.com/Randsw/schema-registry-operator-strimzi/commit/223bd7982e7f58ea10d803f8049ab23fce5bca54))
* Use test for tag ([55d8f0c](https://github.com/Randsw/schema-registry-operator-strimzi/commit/55d8f0c9470e2767b75df93af21b0a7e53a7501c))


### 🛠 Fixes

* Add kafka scheme to controller ([eeed848](https://github.com/Randsw/schema-registry-operator-strimzi/commit/eeed848c22043b2497b827ef0992db3d59e339c1))
* Reconcile start only if secret is updated ([6ec6f5e](https://github.com/Randsw/schema-registry-operator-strimzi/commit/6ec6f5ea6f667f9af1953645de6fbb16256756d2))
* Rename label function ([9b614a4](https://github.com/Randsw/schema-registry-operator-strimzi/commit/9b614a4234594b7187617b7b7f4594b1f9fd80d3))

## [1.0.0](https://github.com/Randsw/schema-registry-operator-strimzi/compare/...1.0.0) (2025-04-02)


### 🦊 CI/CD

* Add pre-commit config ([f3aee0a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/f3aee0af3c67e262371c0c50a8042085aecf464a))
* Fix branch regexp in GH action ([1890654](https://github.com/Randsw/schema-registry-operator-strimzi/commit/1890654f7c2df44c04fe8aa886c449da6995a8df))


### 🚀 Features

* Add create truststore function ([95c2017](https://github.com/Randsw/schema-registry-operator-strimzi/commit/95c201718141877a1abffb80ffcff3f6dc57f520))
* Add GH action to build image ([2ee632a](https://github.com/Randsw/schema-registry-operator-strimzi/commit/2ee632a0566790517f858cc589f2bdcf9c3f83cb))
* Add Truststore and Keystore creation. Add tests ([83284f0](https://github.com/Randsw/schema-registry-operator-strimzi/commit/83284f0f08babd6e4e48351e7154a21daad689f6))
* **test:** Add test to password generator ([ac18634](https://github.com/Randsw/schema-registry-operator-strimzi/commit/ac18634549d1455e321475e04e02e3e0b447e067))


### 🛠 Fixes

* Fix linter errors ([4d86543](https://github.com/Randsw/schema-registry-operator-strimzi/commit/4d865439429d03c84f1c2ea7061836bc3e2deb4e))
* Fix more linter errors ([c913506](https://github.com/Randsw/schema-registry-operator-strimzi/commit/c9135067239982095a69087bfdd9ccb456c4a789))
