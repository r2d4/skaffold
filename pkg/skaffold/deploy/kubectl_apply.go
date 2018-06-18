/*
Copyright 2018 The Skaffold Authors

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

package deploy

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"io/ioutil"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/build"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/schema/v1alpha2"
	"github.com/pkg/errors"
)

type kubectlBaseDeployer struct {
	*v1alpha2.DeployConfig
	kubeContext string
	mb          manifestBuilder
}

type manifestBuilder interface {
	build() (io.Reader, error)
}

func (k *kubectlBaseDeployer) Deploy(ctx context.Context, out io.Writer, builds []build.Artifact) ([]Artifact, error) {
	manifests, err := k.mb.build()
	if err != nil {
		return nil, errors.Wrap(err, "building manifests")
	}
	deploys, err := applyManifests(manifests, out, k.kubeContext, builds)
	if err != nil {
		return nil, errors.Wrap(err, "applying manifests")
	}
	return deploys, nil
}

func applyManifests(r io.Reader, out io.Writer, kubeContext string, builds []build.Artifact) ([]Artifact, error) {
	manifestList, err := newManifestList(r)
	if err != nil {
		return nil, errors.Wrap(err, "getting manifest list")
	}
	manifestList, err = manifestList.replaceImages(builds)
	if err != nil {
		return nil, errors.Wrap(err, "replacing images")
	}
	if err := kubectl(manifestList.reader(), out, kubeContext, "apply", "-f", "-"); err != nil {
		return nil, errors.Wrap(err, "running kubectl")
	}
	return parseManifestsForDeploys(manifestList)
}

func parseManifestsForDeploys(manifests manifestList) ([]Artifact, error) {
	results := []Artifact{}
	for _, manifest := range manifests {
		b := bufio.NewReader(bytes.NewReader(manifest))
		results = append(results, parseReleaseInfo("", b)...)
	}
	return results, nil
}

func newManifestList(r io.Reader) (manifestList, error) {
	var manifests manifestList
	buf, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, errors.Wrap(err, "reading manifests")
	}

	parts := bytes.Split(buf, []byte("\n---"))
	for _, part := range parts {
		manifests = append(manifests, part)
	}

	return manifests, nil
}

func (k *kubectlBaseDeployer) Cleanup(ctx context.Context, out io.Writer) error {
	manifests, err := k.mb.build()
	if err != nil {
		return errors.Wrap(err, "kustomize")
	}
	if err := kubectl(manifests, out, k.kubeContext, "delete", "-f", "-"); err != nil {
		return errors.Wrap(err, "kubectl delete")
	}
	return nil
}
