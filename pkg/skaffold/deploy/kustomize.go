package deploy

import (
	"bytes"
	"context"
	"io"
	"os/exec"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/build"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/schema/v1alpha2"
	"github.com/pkg/errors"
)

type KustomizeDeployer struct {
	*v1alpha2.DeployConfig
	kubeContext string
}

func NewKustomizeDeployer(cfg *v1alpha2.DeployConfig, kubeContext string) *KustomizeDeployer {
	return &KustomizeDeployer{
		DeployConfig: cfg,
		kubeContext:  kubeContext,
	}
}

func (k *KustomizeDeployer) Deploy(ctx context.Context, out io.Writer, builds []build.Build) error {
	if k.KustomizeDeploy.Kustomization == "" {
		return errors.New("must specify a kustomization.yaml")
	}
	manifests, err := buildManifests(k.KustomizeDeploy.Kustomization)
	if err != nil {
		return errors.Wrap(err, "kustomize")
	}
	if err := kubectl(manifests, out, k.kubeContext, "apply", "-f", "-"); err != nil {
		return errors.Wrap(err, "running kubectl")
	}
	return nil
}

func (k *KustomizeDeployer) Cleanup(ctx context.Context, out io.Writer) error {
	manifests, err := buildManifests(k.KustomizeDeploy.Kustomization)
	if err != nil {
		return errors.Wrap(err, "kustomize")
	}
	if err := kubectl(manifests, out, k.kubeContext, "delete", "-f", "-"); err != nil {
		return errors.Wrap(err, "kubectl delete")
	}
	return nil
}

func (k *KustomizeDeployer) Dependencies() ([]string, error) {
	// TODO(r2d4): parse kustomization yaml and add base and patches as dependencies
	return []string{k.KustomizeDeploy.Kustomization}, nil
}

func buildManifests(kustomization string) (io.Reader, error) {
	var buf bytes.Buffer
	cmd := exec.Command("kustomize", "build", kustomization)
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return nil, errors.Wrap(err, "running kustomize build")
	}
	return &buf, nil
}
