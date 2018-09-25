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

package kubernetes

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/util"
	"github.com/sirupsen/logrus"

	"github.com/pkg/errors"
	"k8s.io/api/core/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func CopyFilesForImage(image string, f []string) error {
	return perform(image, f, copyFileFn)
}

func DeleteFilesForImage(image string, f []string) error {
	return perform(image, f, deleteFileFn)
}

func deleteFileFn(pod v1.Pod, container v1.Container, file string) *exec.Cmd {
	return exec.Command("kubectl", "exec", fmt.Sprintf("%s", pod.Name), "-c", container.Name, "--", "rm", "-rf", file)
}

func copyFileFn(pod v1.Pod, container v1.Container, file string) *exec.Cmd {
	return exec.Command("kubectl", "cp", file, fmt.Sprintf("%s/%s:%s", pod.Namespace, pod.Name, file), "-c", container.Name)
}

func perform(image string, files []string, cmdFn func(v1.Pod, v1.Container, string) *exec.Cmd) error {
	logrus.Info("Syncing files:", files, "type: ", cmdFn)
	client, err := Client()
	if err != nil {
		return errors.Wrap(err, "getting k8s client")
	}
	pods, err := client.CoreV1().Pods("").List(meta_v1.ListOptions{})
	if err != nil {
		return errors.Wrap(err, "getting pods")
	}
	for _, p := range pods.Items {
		for _, c := range p.Spec.Containers {
			if strings.HasPrefix(c.Image, image) {
				for _, f := range files {
					cmd := cmdFn(p, c, f)
					if err := util.RunCmd(cmd); err != nil {
						return errors.Wrapf(err, "syncing with kubectl")
					}
				}
			}
		}
	}
	return nil
}
