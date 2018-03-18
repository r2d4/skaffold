# Copyright 2018 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Bump these on release
VERSION_MAJOR ?= 0
VERSION_MINOR ?= 2
VERSION_BUILD ?= 0

VERSION ?= v$(VERSION_MAJOR).$(VERSION_MINOR).$(VERSION_BUILD)

GOOS ?= $(shell go env GOOS)
GOARCH = amd64
BUILD_DIR ?= ./out
ORG := github.com/GoogleCloudPlatform
PROJECT := skaffold
REPOPATH ?= $(ORG)/$(PROJECT)
RELEASE_BUCKET ?= $(PROJECT)

INTEGRATION_CLOUDSDK_COMPUTE_ZONE ?= us-central1-a
INTEGRATION_CONTAINER_CLUSTER ?= gke-dev

SUPPORTED_PLATFORMS := linux-$(GOARCH) darwin-$(GOARCH) windows-$(GOARCH).exe
BUILD_PACKAGE = $(REPOPATH)/cmd/skaffold

VERSION_PACKAGE = $(REPOPATH)/pkg/skaffold/version

GO_LDFLAGS :="
GO_LDFLAGS += -X $(VERSION_PACKAGE).version=$(VERSION)
GO_LDFLAGS += -X $(VERSION_PACKAGE).buildDate=$(shell date +'%Y-%m-%dT%H:%M:%SZ')
GO_LDFLAGS += -X $(VERSION_PACKAGE).gitCommit=$(shell git rev-parse HEAD)
GO_LDFLAGS += -X $(VERSION_PACKAGE).gitTreeState=$(if $(shell git status --porcelain),dirty,clean)
GO_LDFLAGS +="

GO_FILES := $(shell find . -type f -name '*.go' -not -path "./vendor/*")
GO_BUILD_TAGS := "kqueue container_image_ostree_stub containers_image_openpgp"

$(BUILD_DIR)/$(PROJECT): $(BUILD_DIR)/$(PROJECT)-$(GOOS)-$(GOARCH)
	cp $(BUILD_DIR)/$(PROJECT)-$(GOOS)-$(GOARCH) $@

$(BUILD_DIR)/$(PROJECT)-%-$(GOARCH): $(GO_FILES) $(BUILD_DIR)
	GOOS=$* GOARCH=$(GOARCH) CGO_ENABLED=0 go build -ldflags $(GO_LDFLAGS) -tags $(GO_BUILD_TAGS) -o $@ $(BUILD_PACKAGE)

%.sha256: %
	shasum -a 256 $< &> $@

%.exe: %
	mv $< $@

$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

.PRECIOUS: $(foreach platform, $(SUPPORTED_PLATFORMS), $(BUILD_DIR)/$(PROJECT)-$(platform))

.PHONY: cross
cross: $(foreach platform, $(SUPPORTED_PLATFORMS), $(BUILD_DIR)/$(PROJECT)-$(platform).sha256)

.PHONY: test
test:
	@ ./test.sh

.PHONY: install
install: $(GO_FILES) $(BUILD_DIR)
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 go install -ldflags $(GO_LDFLAGS) -tags $(GO_BUILD_TAGS) $(BUILD_PACKAGE)

.PHONY: integration
integration: $(BUILD_DIR)/$(PROJECT)
	go test -v -tags integration $(REPOPATH)/integration -timeout 10m

$(BUILD_DIR)/integration-test-context.tar.gz: install $(skaffold docker deps -f deploy/skaffold/Dockerfile.integration)
	skaffold docker context -c deploy/skaffold -f deploy/skaffold/Dockerfile.integration -o $@

.PHONY: docker
docker: $(BUILD_DIR)/integration-test-context.tar.gz
	docker build -f Dockerfile.integration -t skaffold-integration - < $(BUILD_DIR)/integration-test-context.tar.gz 
	docker run \
        -v $(PWD):/go/src/$(REPOPATH) \
        -v /var/run/docker.sock:/var/run/docker.sock \
        -v $(HOME)/.config/gcloud:/root/.config/gcloud \
        -it \
        -e CLOUDSDK_COMPUTE_ZONE=$(INTEGRATION_CLOUDSDK_COMPUTE_ZONE) \
        -e CLOUDSDK_CONTAINER_CLUSTER=$(INTEGRATION_CONTAINER_CLUSTER) \
        skaffold-integration /bin/bash

.PHONY: release
release: cross
	gsutil cp $(BUILD_DIR)/$(PROJECT)-* gs://$(RELEASE_BUCKET)/$(VERSION)/
	gsutil cp $(BUILD_DIR)/$(PROJECT)-* gs://$(RELEASE_BUCKET)/latest/

.PHONY: clean
clean:
	rm -rf $(BUILD_DIR)
