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
	"io/ioutil"
	"os/exec"
	"strings"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/build"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/constants"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/docker"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/schema/v1alpha2"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/util"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

// KubectlDeployer deploys workflows using kubectl CLI.
type KubectlDeployer struct {
	*v1alpha2.DeployConfig
	*kubectlBaseDeployer

	workingDir string
}

type noOpManifestBuilder struct {
	manifests       []string
	remoteManifests []string
	context         string
	workingDir      string
}

func (n *noOpManifestBuilder) build() (io.Reader, error) {
	manifests, err := readManifests(n.workingDir, n.context, n.manifests, n.remoteManifests)
	if err != nil {
		return nil, errors.Wrap(err, "reading manifests")
	}

	return strings.NewReader(manifests.String()), nil
}

// NewKubectlDeployer returns a new KubectlDeployer for a DeployConfig filled
// with the needed configuration for `kubectl apply`
func NewKubectlDeployer(workingDir string, cfg *v1alpha2.DeployConfig, kubeContext string) *KubectlDeployer {
	return &KubectlDeployer{
		DeployConfig: cfg,
		workingDir:   workingDir,
		kubectlBaseDeployer: &kubectlBaseDeployer{
			DeployConfig: cfg,
			kubeContext:  kubeContext,
			mb: &noOpManifestBuilder{
				manifests:       cfg.KubectlDeploy.Manifests,
				remoteManifests: cfg.KubectlDeploy.RemoteManifests,
				context:         kubeContext,
				workingDir:      workingDir,
			},
		},
	}
}

// readManifests reads the manifests to deploy/delete.
func readManifests(workingDir, context string, manifestPaths, remoteManifests []string) (manifestList, error) {
	files, err := manifestFiles(workingDir, manifestPaths)
	if err != nil {
		return nil, errors.Wrap(err, "expanding user manifest list")
	}
	var manifests manifestList

	for _, manifest := range files {
		buf, err := ioutil.ReadFile(manifest)
		if err != nil {
			return nil, errors.Wrap(err, "reading manifest")
		}

		parts := bytes.Split(buf, []byte("\n---"))
		for _, part := range parts {
			manifests = append(manifests, part)
		}
	}

	for _, m := range remoteManifests {
		manifest, err := readRemoteManifest(m, context)
		if err != nil {
			return nil, errors.Wrap(err, "get remote manifests")
		}

		manifests = append(manifests, manifest)
	}

	logrus.Debugln("manifests", manifests.String())

	return manifests, nil
}

func readRemoteManifest(name, kubeContext string) ([]byte, error) {
	var args []string
	if parts := strings.Split(name, ":"); len(parts) > 1 {
		args = append(args, "--namespace", parts[0])
		name = parts[1]
	}
	args = append(args, "get", name, "-o", "yaml")

	var manifest bytes.Buffer
	err := kubectl(nil, &manifest, kubeContext, args...)
	if err != nil {
		return nil, errors.Wrap(err, "getting manifest")
	}

	return manifest.Bytes(), nil
}

func (k *KubectlDeployer) Labels() map[string]string {
	return map[string]string{
		constants.Labels.Deployer: "kubectl",
	}
}

func (k *KubectlDeployer) Dependencies() ([]string, error) {
	return manifestFiles(k.workingDir, k.KubectlDeploy.Manifests)
}

func kubectl(in io.Reader, out io.Writer, kubeContext string, arg ...string) error {
	args := append([]string{"--context", kubeContext}, arg...)

	cmd := exec.Command("kubectl", args...)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = out

	return util.RunCmd(cmd)
}

func manifestFiles(workingDir string, manifests []string) ([]string, error) {
	list, err := util.ExpandPathsGlob(workingDir, manifests)
	if err != nil {
		return nil, errors.Wrap(err, "expanding kubectl manifest paths")
	}

	var filteredManifests []string
	for _, f := range list {
		if !util.IsSupportedKubernetesFormat(f) {
			if !util.StrSliceContains(manifests, f) {
				logrus.Infof("refusing to deploy/delete non {json, yaml} file %s", f)
				logrus.Info("If you still wish to deploy this file, please specify it directly, outside a glob pattern.")
				continue
			}
		}
		filteredManifests = append(filteredManifests, f)
	}

	return filteredManifests, nil
}

type replacement struct {
	tag   string
	found bool
}

type manifestList [][]byte

func (l *manifestList) String() string {
	var str string
	for i, manifest := range *l {
		if i != 0 {
			str += "\n---\n"
		}
		str += string(bytes.TrimSpace(manifest))
	}
	return str
}

func (l *manifestList) reader() io.Reader {
	return strings.NewReader(l.String())
}

func (l *manifestList) replaceImages(builds []build.Artifact) (manifestList, error) {
	replacements := map[string]*replacement{}
	for _, build := range builds {
		replacements[build.ImageName] = &replacement{
			tag: build.Tag,
		}
	}

	var updatedManifests manifestList

	for _, manifest := range *l {
		m := make(map[interface{}]interface{})
		if err := yaml.Unmarshal(manifest, &m); err != nil {
			return nil, errors.Wrap(err, "reading kubernetes YAML")
		}

		if len(m) == 0 {
			continue
		}

		recursiveReplaceImage(m, replacements)

		updatedManifest, err := yaml.Marshal(m)
		if err != nil {
			return nil, errors.Wrap(err, "marshalling yaml")
		}

		updatedManifests = append(updatedManifests, updatedManifest)
	}

	for name, replacement := range replacements {
		if !replacement.found {
			logrus.Warnf("image [%s] is not used by the deployment", name)
		}
	}

	logrus.Debugln("manifests with tagged images", updatedManifests.String())

	return updatedManifests, nil
}

func recursiveReplaceImage(i interface{}, replacements map[string]*replacement) {
	switch t := i.(type) {
	case []interface{}:
		for _, v := range t {
			recursiveReplaceImage(v, replacements)
		}
	case map[interface{}]interface{}:
		for k, v := range t {
			if k.(string) != "image" {
				recursiveReplaceImage(v, replacements)
				continue
			}

			image := v.(string)
			parsed, err := docker.ParseReference(image)
			if err != nil {
				logrus.Warnf("Couldn't parse image: %s", v)
				continue
			}

			if parsed.FullyQualified {
				// TODO(1.0.0): Remove this warning.
				logrus.Infof("Not replacing fully qualified image: %s (see #565)", v)
				continue
			}

			if img, present := replacements[parsed.BaseName]; present {
				t[k] = img.tag
				img.found = true
			}
		}
	}
}
