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

package kaniko

import (
	"context"
	"fmt"
	"io"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/constants"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/docker"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/kubernetes"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/schema/v1alpha2"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/util"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func RunKanikoBuild(ctx context.Context, out io.Writer, artifact *v1alpha2.Artifact, cfg *v1alpha2.KanikoBuild) (string, error) {
	dockerfilePath := artifact.DockerArtifact.DockerfilePath

	initialTag := util.RandomID()
	tarName := "context.tar.gz" // TODO(r2d4): until this is configurable upstream
	if err := docker.UploadContextToGCS(ctx, dockerfilePath, artifact.Workspace, cfg.GCSBucket, tarName); err != nil {
		return "", errors.Wrap(err, "uploading tar to gcs")
	}

	client, err := kubernetes.GetClientset()
	if err != nil {
		return "", errors.Wrap(err, "")
	}

	imageList := kubernetes.NewImageList()
	imageList.Add(constants.DefaultKanikoImage)

	logger := kubernetes.NewLogAggregator(out, imageList, kubernetes.NewColorPicker([]*v1alpha2.Artifact{artifact}))
	if err := logger.Start(ctx); err != nil {
		return "", errors.Wrap(err, "starting log streamer")
	}
	imageDst := fmt.Sprintf("%s:%s", artifact.ImageName, initialTag)

	args := []string{
		fmt.Sprintf("--dockerfile=%s", dockerfilePath),
		fmt.Sprintf("--bucket=%s", cfg.GCSBucket),
		fmt.Sprintf("--destination=%s:%s", artifact.ImageName, initialTag),
		fmt.Sprintf("-v=%s", logrus.GetLevel().String()),
	}
	if cfg.InsecureSkipTLSVerify {
		args = append(args, fmt.Sprintf("--insecure-skip-tls-verify=true"))
	}
	if cfg.TarPath != "" {
		args = append(args, fmt.Sprintf("--tarPath=%s", cfg.TarPath))
	}
	kanikoSecretName := constants.DefaultKanikoSecretName
	if cfg.SecretVolumeSource != "" {
		kanikoSecretName = cfg.SecretVolumeSource
	}
	kanikoImageName := constants.DefaultKanikoImage
	if 
	p, err := client.CoreV1().Pods("default").Create(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "kaniko",
			Labels: map[string]string{"kaniko": "kaniko"},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:            "kaniko",
					Image:           constants.DefaultKanikoImage,
					ImagePullPolicy: v1.PullIfNotPresent,
					Args:            args,
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "kaniko-secret",
							MountPath: "/secret",
						},
					},
					Env: []v1.EnvVar{
						{
							Name:  "GOOGLE_APPLICATION_CREDENTIALS",
							Value: "/secret/kaniko-secret",
						},
					},
				},
			},
			Volumes: []v1.Volume{
				{
					Name: "kaniko-secret",
					VolumeSource: v1.VolumeSource{
						Secret: &v1.SecretVolumeSource{
							SecretName: kanikoSecretName,
						},
					},
				},
			},
			RestartPolicy: v1.RestartPolicyNever,
		},
	})

	if err != nil {
		return "", errors.Wrap(err, "creating kaniko pod")
	}

	defer func() {
		imageList.Remove(constants.DefaultKanikoImage)
		if err := client.CoreV1().Pods("default").Delete(p.Name, &metav1.DeleteOptions{
			GracePeriodSeconds: new(int64),
		}); err != nil {
			logrus.Fatalf("deleting pod: %s", err)
		}
	}()

	if err := kubernetes.WaitForPodComplete(client.CoreV1().Pods("default"), p.Name); err != nil {
		return "", errors.Wrap(err, "waiting for pod to complete")
	}

	return imageDst, nil
}
