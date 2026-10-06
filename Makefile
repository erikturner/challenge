SHELL := /bin/sh

IMAGE_NAME ?= kube-replica-server
IMAGE_TAG ?= $(shell git rev-parse --short HEAD)
IMAGE = $(IMAGE_NAME):$(IMAGE_TAG)

CLUSTER ?= kube-replica-server
NAMESPACE ?= default
RELEASE ?= kube-replica-server

CHART ?= ./charts/kube-replica-server
TLS_SECRET ?= kube-replica-server-tls
CERT_DIR ?= certs

TEST_DEPLOYMENT ?= replica-test
TEST_MANIFEST ?= deploy/replica-test.yaml

KUBE_CONTEXT = kind-$(CLUSTER)

.PHONY: help doctor test build certs docker-build kind-up \
        kind-load tls-secret helm-lint deploy integration-test \
        verify clean

help:
	@printf '%s\n' \
	  'make doctor            Check local tool dependencies' \
	  'make test              Run Go unit tests' \
	  'make build             Build the Go binary' \
	  'make certs             Generate local mTLS certificates' \
	  'make docker-build      Build the Docker image' \
	  'make kind-up           Create the KIND cluster if missing' \
	  'make kind-load         Build and load the image into KIND' \
	  'make tls-secret        Create the Kubernetes TLS Secret' \
	  'make helm-lint         Validate the Helm chart' \
	  'make deploy            Deploy the application and test fixture' \
	  'make integration-test  Run integration tests against KIND' \
	  'make verify            Run the complete local verification workflow' \
	  'make clean             Delete generated files and the KIND cluster'

doctor:
	./scripts/check-tools.sh

test:
	go test ./...

build:
	mkdir -p bin
	go build -o bin/server ./cmd/server

certs:
	rm -rf $(CERT_DIR)
	go run ./cmd/certgen -out $(CERT_DIR)

docker-build:
	docker build -t $(IMAGE) .

kind-up:
	@kind get clusters 2>/dev/null | grep -qx '$(CLUSTER)' || \
		kind create cluster --name $(CLUSTER)

kind-load: docker-build kind-up
	kind load docker-image $(IMAGE) --name $(CLUSTER)

tls-secret: certs kind-up
	kubectl --context $(KUBE_CONTEXT) \
		create namespace $(NAMESPACE) \
		--dry-run=client -o yaml | \
		kubectl --context $(KUBE_CONTEXT) apply -f -

	kubectl --context $(KUBE_CONTEXT) -n $(NAMESPACE) \
		create secret generic $(TLS_SECRET) \
		--from-file=server.crt=$(CERT_DIR)/server.crt \
		--from-file=server.key=$(CERT_DIR)/server.key \
		--from-file=ca.crt=$(CERT_DIR)/ca.crt \
		--dry-run=client -o yaml | \
		kubectl --context $(KUBE_CONTEXT) apply -f -

helm-lint:
	helm lint $(CHART)

deploy: kind-load tls-secret helm-lint
	helm upgrade --install $(RELEASE) $(CHART) \
		--kube-context $(KUBE_CONTEXT) \
		--namespace $(NAMESPACE) \
		--create-namespace \
		--set-string image.repository=$(IMAGE_NAME) \
		--set-string image.tag=$(IMAGE_TAG) \
		--set image.pullPolicy=Never \
		--set-string tls.secretName=$(TLS_SECRET) \
		--wait \
		--timeout 120s

	# Restart Pods to load the newly generated mTLS certificates.
	kubectl --context $(KUBE_CONTEXT) -n $(NAMESPACE) \
		rollout restart deployment/$(RELEASE)

	kubectl --context $(KUBE_CONTEXT) -n $(NAMESPACE) \
		rollout status deployment/$(RELEASE) \
		--timeout=120s

	kubectl --context $(KUBE_CONTEXT) -n $(NAMESPACE) \
		apply -f $(TEST_MANIFEST)

	kubectl --context $(KUBE_CONTEXT) -n $(NAMESPACE) \
		rollout status deployment/$(TEST_DEPLOYMENT) \
		--timeout=120s

integration-test:
	KUBECTL_CONTEXT=$(KUBE_CONTEXT) \
	TEST_DEPLOYMENT=$(TEST_DEPLOYMENT) \
	./test/integration/run.sh $(NAMESPACE) $(RELEASE) $(CERT_DIR)

verify: doctor test deploy integration-test

clean:
	rm -rf $(CERT_DIR) bin
	kind delete cluster --name $(CLUSTER)
