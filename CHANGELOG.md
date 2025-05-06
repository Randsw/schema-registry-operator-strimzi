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
