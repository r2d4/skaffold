package kubernetes

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/util"
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
	return exec.Command("kubectl", "exec", fmt.Sprintf("%s/%s", pod.Namespace, pod.Name), "-c", container.Name, "rm", "-rf", file)
}

func copyFileFn(pod v1.Pod, container v1.Container, file string) *exec.Cmd {
	return exec.Command("kubectl", "cp", file, fmt.Sprintf("%s/%s:%s", pod.Namespace, pod.Name, file), "-c", container.Name)
}

func perform(image string, files []string, cmdFn func(v1.Pod, v1.Container, string) *exec.Cmd) error {
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
						return errors.Wrap(err, "running kubectl cp")
					}
				}
			}
		}
	}
	return nil
}
