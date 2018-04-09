/*
Copyright 2018 Google LLC

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

package build

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/skaffold/pkg/skaffold/build/tag"
	"github.com/GoogleCloudPlatform/skaffold/pkg/skaffold/config"
	"github.com/GoogleCloudPlatform/skaffold/pkg/skaffold/constants"
	"github.com/GoogleCloudPlatform/skaffold/pkg/skaffold/docker"
	"github.com/GoogleCloudPlatform/skaffold/pkg/skaffold/kubernetes"
	"github.com/GoogleCloudPlatform/skaffold/pkg/skaffold/util"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LocalBuilder uses the host docker daemon to build and tag the image
type LocalBuilder struct {
	*config.BuildConfig

	api          docker.DockerAPIClient
	localCluster bool
	kubeContext  string
}

// NewLocalBuilder returns an new instance of a LocalBuilder
func NewLocalBuilder(cfg *config.BuildConfig, kubeContext string) (*LocalBuilder, error) {
	api, err := docker.NewDockerAPIClient()
	if err != nil {
		return nil, errors.Wrap(err, "getting docker client")
	}

	l := &LocalBuilder{
		BuildConfig: cfg,

		kubeContext:  kubeContext,
		api:          api,
		localCluster: kubeContext == constants.DefaultMinikubeContext || kubeContext == constants.DefaultDockerForDesktopContext,
	}

	if cfg.LocalBuild.SkipPush == nil {
		logrus.Debugf("skipPush value not present. defaulting to cluster default %t (minikube=true, d4d=true, gke=false)", l.localCluster)
		cfg.LocalBuild.SkipPush = &l.localCluster
	}

	return l, nil
}

func (l *LocalBuilder) runBuildForArtifact(ctx context.Context, out io.Writer, artifact *config.Artifact) (string, error) {
	logrus.Debugf("Running build for %+v", artifact)
	if artifact.DockerArtifact != nil {
		return l.buildDocker(ctx, out, artifact)
	}
	if artifact.BazelArtifact != nil {
		return l.buildBazel(ctx, out, artifact)
	}
	if artifact.KanikoArtifact != nil {
		return l.buildKaniko(ctx, out, artifact)
	}
	return "", fmt.Errorf("undefined artifact type: %+v", artifact.ArtifactType)
}

const kanikoImage = "gcr.io/kbuild-project/executor:latest"

func (l *LocalBuilder) buildKaniko(ctx context.Context, out io.Writer, artifact *config.Artifact) (string, error) {
	tarName := fmt.Sprintf("context-%s.tar.gz", util.RandomID())
	if err := UploadTarToGCS(ctx, artifact.KanikoArtifact.DockerfilePath, artifact.Workspace, artifact.KanikoArtifact.GCSBucket, tarName); err != nil {
		return "", errors.Wrap(err, "uploading tar to gcs")
	}
	client, err := kubernetes.GetClientset()
	if err != nil {
		return "", errors.Wrap(err, "")
	}

	secretData, err := ioutil.ReadFile(artifact.KanikoArtifact.PullSecret)
	if err != nil {
		return "", errors.Wrap(err, "reading secret")
	}

	secret, err := client.CoreV1().Secrets("default").Create(&v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "kaniko-secret",
			Labels: map[string]string{"kaniko": "kaniko"},
		},
		Data: map[string][]byte{
			"kaniko-secret": secretData,
		},
	})
	if err != nil {
		return "", errors.Wrap(err, "creating secret")
	}
	defer func() {
		if err := client.CoreV1().Secrets("default").Delete(secret.Name, &metav1.DeleteOptions{}); err != nil {
			logrus.Fatalf("deleting secret")
		}
	}()

	imageList := kubernetes.NewImageList()
	imageList.AddImage(kanikoImage)

	logger := kubernetes.NewLogAggregator(out, imageList, kubernetes.NewColorPicker([]*config.Artifact{artifact}))
	if err := logger.Start(ctx, client.CoreV1()); err != nil {
		return "", errors.Wrap(err, "starting log streamer")
	}

	p, err := client.CoreV1().Pods("default").Create(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "kaniko",
			Labels: map[string]string{"kaniko": "kaniko"},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:            "kaniko",
					Image:           kanikoImage, //todo(r2d4)
					ImagePullPolicy: v1.PullIfNotPresent,
					Args: []string{
						fmt.Sprintf("--dockerfile=%s", artifact.KanikoArtifact.DockerfilePath),
						fmt.Sprintf("--bucket=%s", artifact.KanikoArtifact.GCSBucket),
						fmt.Sprintf("--remote-context-path=%s", tarName),
						fmt.Sprintf("--destination=%s:kaniko", artifact.ImageName),
						fmt.Sprintf("-v=%s", logrus.GetLevel().String()),
					},
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
							SecretName: secret.Name,
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
		imageList.RemoveImage(kanikoImage)
		if err := client.CoreV1().Pods("default").Delete(p.Name, &metav1.DeleteOptions{
			GracePeriodSeconds: new(int64),
		}); err != nil {
			logrus.Fatalf("deleting pod: %s", err)
		}
	}()

	if err := kubernetes.WaitForPodComplete(client.CoreV1().Pods("default"), p.Name); err != nil {
		req := client.CoreV1().Pods("default").GetLogs(p.Name, &v1.PodLogOptions{})
		rc, err := req.Stream()
		if err != nil {
			return "", errors.Wrap(err, "streaming logs from failed pod")
		}
		defer rc.Close()
		io.Copy(out, rc)
		return "", errors.Wrap(err, "waiting for pod to complete")
	}

	return "", nil
}

// Build runs a docker build on the host and tags the resulting image with
// its checksum. It streams build progress to the writer argument.
func (l *LocalBuilder) Build(ctx context.Context, out io.Writer, tagger tag.Tagger, artifacts []*config.Artifact) (*BuildResult, error) {
	if l.localCluster {
		if _, err := fmt.Fprintf(out, "Found [%s] context, using local docker daemon.\n", l.kubeContext); err != nil {
			return nil, errors.Wrap(err, "writing status")
		}
	}
	defer l.api.Close()

	res := &BuildResult{}
	for _, artifact := range artifacts {
		initialTag, err := l.runBuildForArtifact(ctx, out, artifact)
		if err != nil {
			return nil, errors.Wrap(err, "running build for artifact")
		}

		digest, err := docker.Digest(ctx, l.api, initialTag)
		if err != nil {
			return nil, errors.Wrapf(err, "build and tag: %s", initialTag)
		}
		if digest == "" {
			return nil, fmt.Errorf("digest not found")
		}
		tag, err := tagger.GenerateFullyQualifiedImageName(".", &tag.TagOptions{
			ImageName: artifact.ImageName,
			Digest:    digest,
		})
		if err != nil {
			return nil, errors.Wrap(err, "generating tag")
		}
		if err := l.api.ImageTag(ctx, initialTag, tag); err != nil {
			return nil, errors.Wrap(err, "tagging image")
		}
		if _, err := io.WriteString(out, fmt.Sprintf("Successfully tagged %s\n", tag)); err != nil {
			return nil, errors.Wrap(err, "writing tag status")
		}
		if !*l.LocalBuild.SkipPush {
			if err := docker.RunPush(ctx, l.api, tag, out); err != nil {
				return nil, errors.Wrap(err, "running push")
			}
		}

		res.Builds = append(res.Builds, Build{
			ImageName: artifact.ImageName,
			Tag:       tag,
			Artifact:  artifact,
		})
	}

	return res, nil
}

func (l *LocalBuilder) buildBazel(ctx context.Context, out io.Writer, a *config.Artifact) (string, error) {
	cmd := exec.Command("bazel", "build", a.BazelArtifact.BuildTarget)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return "", errors.Wrap(err, "running command")
	}
	//TODO(r2d4): strip off leading //:, bad
	tarPath := strings.TrimPrefix(a.BazelArtifact.BuildTarget, "//:")
	//TODO(r2d4): strip off trailing .tar, even worse
	imageTag := strings.TrimSuffix(tarPath, ".tar")
	imageTar, err := os.Open(filepath.Join("bazel-bin", tarPath))
	if err != nil {
		return "", errors.Wrap(err, "opening image tarball")
	}
	defer imageTar.Close()
	resp, err := l.api.ImageLoad(ctx, imageTar, false)
	if err != nil {
		return "", errors.Wrap(err, "loading image into docker daemon")
	}
	defer resp.Body.Close()
	respStr, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "reading from image load response")
	}
	out.Write(respStr)

	return fmt.Sprintf("bazel:%s", imageTag), nil
}

func (l *LocalBuilder) buildDocker(ctx context.Context, out io.Writer, a *config.Artifact) (string, error) {
	initialTag := util.RandomID()
	err := docker.RunBuild(ctx, l.api, &docker.BuildOptions{
		ImageNames:  []string{initialTag, a.ImageName},
		Dockerfile:  a.DockerArtifact.DockerfilePath,
		ContextDir:  a.Workspace,
		ProgressBuf: out,
		BuildBuf:    out,
		BuildArgs:   a.DockerArtifact.BuildArgs,
	})
	if err != nil {
		return "", errors.Wrap(err, "running build")
	}
	return fmt.Sprintf("%s:latest", initialTag), nil
}
