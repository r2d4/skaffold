package build

import (
	"context"
	"path/filepath"

	cstorage "cloud.google.com/go/storage"
	"github.com/GoogleCloudPlatform/skaffold/pkg/skaffold/docker"
	"github.com/pkg/errors"
)

func UploadTarToGCS(ctx context.Context, dockerfilePath, dockerCtx, bucket, objectName string) error {
	c, err := cstorage.NewClient(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	relDockerfilePath := filepath.Join(dockerCtx, dockerfilePath)
	w := c.Bucket(bucket).Object(objectName).NewWriter(ctx)
	if err := docker.CreateDockerTarGzContext(w, relDockerfilePath, dockerCtx); err != nil {
		return errors.Wrap(err, "uploading targz to google storage")
	}
	return w.Close()
}
