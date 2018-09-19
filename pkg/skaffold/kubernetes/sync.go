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
	client, err := Client()
	if err != nil {
		return errors.Wrap(err, "getting k8s client")
	}
	pods, err := client.CoreV1().Pods("").List(meta_v1.ListOptions{})
	if err != nil {
		return errors.Wrap(err, "getting all pods")
	}
	for _, p := range pods.Items {
		for _, c := range p.Spec.Containers {
			if strings.HasPrefix(c.Image, image) {
				if err := copy(p, c, f); err != nil {
					return errors.Wrap(err, "copying files into pod")
				}
			}
		}
	}
	return nil
}

func copy(pod v1.Pod, container v1.Container, files []string) error {
	for _, f := range files {
		cmd := exec.Command("kubectl", "cp", f, fmt.Sprintf("%s/%s:%s", pod.Namespace, pod.Name, f), "-c", container.Name)
		if err := util.RunCmd(cmd); err != nil {
			return errors.Wrap(err, "running kubectl cp")
		}
	}
	return nil
}

func DeleteFilesForImage(image string, f []string) error {
	return nil
}
