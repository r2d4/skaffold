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
	"bytes"
	"io"
	"os/exec"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/constants"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/schema/v1alpha2"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/util"
	"github.com/pkg/errors"
)

type ComposeDeployer struct {
	*kubectlBaseDeployer
	*v1alpha2.DeployConfig
}

type komposeManifestBuilder struct{}

func (k *komposeManifestBuilder) build() (io.Reader, error) {
	cmd := exec.Command("kompose", "convert", "--stdout")
	out, err := util.DefaultExecCommand.RunCmdOut(cmd)
	if err != nil {
		return nil, errors.Wrap(err, "running kustomize build")
	}
	return bytes.NewReader(out), nil
}

func NewComposeDeployer(cfg *v1alpha2.DeployConfig, kubeContext string) *ComposeDeployer {
	return &ComposeDeployer{
		DeployConfig: cfg,
		kubectlBaseDeployer: &kubectlBaseDeployer{
			kubeContext:  kubeContext,
			DeployConfig: cfg,
			mb:           &komposeManifestBuilder{},
		},
	}
}

func (c *ComposeDeployer) Labels() map[string]string {
	return map[string]string{
		constants.Labels.Deployer: "kompose",
	}
}

func (c *ComposeDeployer) Dependencies() ([]string, error) {
	return []string{}, nil
}
