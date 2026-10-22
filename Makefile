KIND ?= kind
KIND_CLUSTER ?= swiss-test-e2e
# Empty uses the node image pinned in test/e2e/kind-config.yaml.
# Set to pass kind create --image and override that pin.
KIND_NODE_IMAGE ?=
IMG ?= swissd:e2e
DOCKER ?= docker
# Extra arguments to go test, for example -ginkgo.label-filter=helm.
E2E_ARGS ?=
E2E_TIMEOUT ?= 45m

.PHONY: help
help: ## List targets.
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

.PHONY: docker-build
docker-build: ## Build the swissd image ($(IMG)).
	@test -f web/console/web/package.json || { \
		echo "web/console is empty; run: git submodule update --init" >&2; \
		exit 1; \
	}
	$(DOCKER) build -t $(IMG) .

.PHONY: setup-test-e2e
setup-test-e2e: ## Create kind cluster $(KIND_CLUSTER) if it does not exist.
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "kind is not installed. Install kind and retry." >&2; \
		exit 1; \
	}
	@case "$$($(KIND) get clusters 2>/dev/null)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation." ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)'..."; \
			$(KIND) create cluster --name $(KIND_CLUSTER) \
				--config test/e2e/kind-config.yaml $(if $(KIND_NODE_IMAGE),--image $(KIND_NODE_IMAGE),) \
				--wait 120s ;; \
	esac

.PHONY: test-e2e
test-e2e: setup-test-e2e ## Run the e2e suite. Deletes the cluster on success unless KEEP_CLUSTER=1. A failure leaves the cluster up.
	KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) IMG=$(IMG) \
		go test -tags=e2e ./test/e2e/ -v -ginkgo.v -timeout $(E2E_TIMEOUT) $(E2E_ARGS)
ifeq ($(KEEP_CLUSTER),1)
	@echo "KEEP_CLUSTER=1: leaving $(KIND_CLUSTER) up (kubectl --context kind-$(KIND_CLUSTER))"
else
	$(MAKE) cleanup-test-e2e
endif

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Delete kind cluster $(KIND_CLUSTER).
	$(KIND) delete cluster --name $(KIND_CLUSTER)

.PHONY: test
test: ## Run unit tests.
	go test ./...

.PHONY: vet
vet: ## Vet all packages, including the e2e build tag.
	go vet ./...
	go vet -tags=e2e ./test/...
