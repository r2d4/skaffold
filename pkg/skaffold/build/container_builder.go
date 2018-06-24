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

package build

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	cstorage "cloud.google.com/go/storage"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/build/tag"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/constants"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/docker"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/schema/v1alpha2"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/util"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/version"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2/google"
	cloudbuild "google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
)

const (
	// StatusUnknown "STATUS_UNKNOWN" - Status of the build is unknown.
	StatusUnknown = "STATUS_UNKNOWN"

	// StatusQueued "QUEUED" - Build is queued; work has not yet begun.
	StatusQueued = "QUEUED"

	// StatusWorking "WORKING" - Build is being executed.
	StatusWorking = "WORKING"

	// StatusSuccess  "SUCCESS" - Build finished successfully.
	StatusSuccess = "SUCCESS"

	// StatusFailure  "FAILURE" - Build failed to complete successfully.
	StatusFailure = "FAILURE"

	// StatusInternalError  "INTERNAL_ERROR" - Build failed due to an internal cause.
	StatusInternalError = "INTERNAL_ERROR"

	// StatusTimeout  "TIMEOUT" - Build took longer than was allowed.
	StatusTimeout = "TIMEOUT"

	// StatusCancelled  "CANCELLED" - Build was canceled by a user.
	StatusCancelled = "CANCELLED"

	// RetryDelay is the time to wait in between polling the status of the cloud build
	RetryDelay = 1 * time.Second
)

type GoogleCloudBuilder struct {
	*v1alpha2.BuildConfig
}

func NewGoogleCloudBuilder(cfg *v1alpha2.BuildConfig) (*GoogleCloudBuilder, error) {
	return &GoogleCloudBuilder{BuildConfig: cfg}, nil
}

func (cb *GoogleCloudBuilder) Labels() map[string]string {
	return map[string]string{
		constants.Labels.Builder: "google-cloud-builder",
	}
}

func (*GoogleCloudBuilder) BuilderName() string {
	return "GoogleCloudBuilder"
}

func (cb *GoogleCloudBuilder) Build(ctx context.Context, out io.Writer, tagger tag.Tagger, artifacts []*v1alpha2.Artifact) ([]Artifact, error) {
	client, err := google.DefaultClient(ctx, cloudbuild.CloudPlatformScope)
	if err != nil {
		return nil, errors.Wrap(err, "getting google client")
	}

	cbclient, err := cloudbuild.New(client)
	if err != nil {
		return nil, errors.Wrap(err, "getting builder")
	}

	cbclient.UserAgent = version.UserAgent()
	c, err := cstorage.NewClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "getting cloud storage client")
	}
	defer c.Close()

	var builds []Artifact
	for _, artifact := range artifacts {
		build, err := cb.buildArtifact(ctx, out, tagger, cbclient, c, artifact)
		if err != nil {
			return nil, errors.Wrapf(err, "building [%s]", artifact.ImageName)
		}

		builds = append(builds, *build)
	}

	return builds, nil
}

func (cb *GoogleCloudBuilder) buildArtifact(ctx context.Context, out io.Writer, tagger tag.Tagger, cbclient *cloudbuild.Service, c *cstorage.Client, artifact *v1alpha2.Artifact) (*Artifact, error) {
	fmt.Fprintf(out, "Building [%s]...\n", artifact.ImageName)

	// need to format build args as strings to pass to container builder docker
	var buildArgs []string
	for k, v := range artifact.DockerArtifact.BuildArgs {
		if v != nil {
			buildArgs = append(buildArgs, []string{"--build-arg", fmt.Sprintf("%s=%s", k, *v)}...)
		}
	}
	logrus.Debugf("Build args: %s", buildArgs)

	cbBucket := fmt.Sprintf("%s%s", cb.GoogleCloudBuild.ProjectID, constants.GCSBucketSuffix)
	buildObject := fmt.Sprintf("source/%s-%s.tar.gz", cb.GoogleCloudBuild.ProjectID, util.RandomID())

	if err := cb.createBucketIfNotExists(ctx, cbBucket); err != nil {
		return nil, errors.Wrap(err, "creating bucket if not exists")
	}
	if err := cb.checkBucketProjectCorrect(ctx, cbBucket); err != nil {
		return nil, errors.Wrap(err, "checking bucket is in correct project")
	}

	fmt.Fprintf(out, "Pushing code to gs://%s/%s\n", cbBucket, buildObject)
	if err := docker.UploadContextToGCS(ctx, artifact.DockerArtifact.DockerfilePath, artifact.Workspace, cbBucket, buildObject); err != nil {
		return nil, errors.Wrap(err, "uploading source tarball")
	}

	args := append([]string{"build", "--tag", artifact.ImageName, "-f", artifact.DockerArtifact.DockerfilePath}, buildArgs...)
	args = append(args, ".")
	call := cbclient.Projects.Builds.Create(cb.GoogleCloudBuild.ProjectID, &cloudbuild.Build{
		LogsBucket: cbBucket,
		Source: &cloudbuild.Source{
			StorageSource: &cloudbuild.StorageSource{
				Bucket: cbBucket,
				Object: buildObject,
			},
		},
		Steps: []*cloudbuild.BuildStep{
			{
				Name: "gcr.io/cloud-builders/docker",
				Args: args,
			},
		},
		Images: []string{artifact.ImageName},
	})
	op, err := call.Context(ctx).Do()
	if err != nil {
		return nil, errors.Wrap(err, "could not create build")
	}

	remoteID, err := getBuildID(op)
	if err != nil {
		return nil, errors.Wrapf(err, "getting build ID from op")
	}
	logsObject := fmt.Sprintf("log-%s.txt", remoteID)
	fmt.Fprintf(out, "Logs at available at \nhttps://console.cloud.google.com/m/cloudstorage/b/%s/o/%s\n", cbBucket, logsObject)
	var imageID string
	offset := int64(0)
watch:
	for {
		logrus.Debugf("current offset %d", offset)
		b, err := cbclient.Projects.Builds.Get(cb.GoogleCloudBuild.ProjectID, remoteID).Do()
		if err != nil {
			return nil, errors.Wrap(err, "getting build status")
		}

		r, err := cb.getLogs(ctx, offset, cbBucket, logsObject)
		if err != nil {
			return nil, errors.Wrap(err, "getting logs")
		}
		if r != nil {
			written, err := io.Copy(out, r)
			if err != nil {
				return nil, errors.Wrap(err, "copying logs to stdout")
			}
			offset += written
			r.Close()
		}
		switch b.Status {
		case StatusQueued, StatusWorking, StatusUnknown:
		case StatusSuccess:
			imageID, err = getImageID(b)
			if err != nil {
				return nil, errors.Wrap(err, "getting image id from finished build")
			}
			break watch
		case StatusFailure, StatusInternalError, StatusTimeout, StatusCancelled:
			return nil, fmt.Errorf("cloud build failed: %s", b.Status)
		default:
			return nil, fmt.Errorf("unknown status: %s", b.Status)
		}

		time.Sleep(RetryDelay)
	}

	if err := c.Bucket(cbBucket).Object(buildObject).Delete(ctx); err != nil {
		return nil, errors.Wrap(err, "cleaning up source tar after build")
	}
	logrus.Infof("Deleted object %s", buildObject)
	builtTag := fmt.Sprintf("%s@%s", artifact.ImageName, imageID)
	logrus.Infof("Image built at %s", builtTag)

	newTag, err := tagger.GenerateFullyQualifiedImageName(artifact.Workspace, &tag.Options{
		ImageName: artifact.ImageName,
		Digest:    imageID,
	})

	if err != nil {
		return nil, errors.Wrap(err, "generating tag")
	}

	if err := docker.AddTag(builtTag, newTag); err != nil {
		return nil, errors.Wrap(err, "tagging image")
	}

	return &Artifact{
		ImageName: artifact.ImageName,
		Tag:       newTag,
	}, nil
}

func getBuildID(op *cloudbuild.Operation) (string, error) {
	if op.Metadata == nil {
		return "", errors.New("missing Metadata in operation")
	}
	var buildMeta cloudbuild.BuildOperationMetadata
	if err := json.Unmarshal([]byte(op.Metadata), &buildMeta); err != nil {
		return "", err
	}
	if buildMeta.Build == nil {
		return "", errors.New("missing Build in operation metadata")
	}
	return buildMeta.Build.Id, nil
}

func getImageID(b *cloudbuild.Build) (string, error) {
	if b.Results == nil || len(b.Results.Images) == 0 {
		return "", errors.New("build failed")
	}
	return b.Results.Images[0].Digest, nil
}

func (cb *GoogleCloudBuilder) getLogs(ctx context.Context, offset int64, bucket, objectName string) (io.ReadCloser, error) {
	c, err := cstorage.NewClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "getting storage client")
	}
	defer c.Close()

	r, err := c.Bucket(bucket).Object(objectName).NewRangeReader(ctx, offset, -1)
	if err != nil {
		if gerr, ok := err.(*googleapi.Error); ok {
			switch gerr.Code {
			case 404, 416, 429, 503:
				logrus.Debugf("Status Code: %d, %s", gerr.Code, gerr.Body)
				return nil, nil
			}
		}
		if err == cstorage.ErrObjectNotExist {
			logrus.Debugf("Logs for %s %s not uploaded yet...", bucket, objectName)
			return nil, nil
		}
		return nil, errors.Wrap(err, "unknown error")
	}
	return r, nil
}

func (cb *GoogleCloudBuilder) checkBucketProjectCorrect(ctx context.Context, bucket string) error {
	c, err := cstorage.NewClient(ctx)
	if err != nil {
		return errors.Wrap(err, "getting storage client")
	}
	it := c.Buckets(ctx, cb.GoogleCloudBuild.ProjectID)
	// Set the prefix to the bucket we're looking for to only return that bucket and buckets with that prefix
	// that we'll filter further later on
	it.Prefix = bucket
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			return errors.Wrap(err, "bucket not found")
		}
		if err != nil {
			return errors.Wrap(err, "iterating over buckets")
		}
		// Since we can't filter on bucket name specifically, only prefix, we need to check equality here and not just prefix
		if attrs.Name == bucket {
			return nil
		}
	}

}

func (cb *GoogleCloudBuilder) createBucketIfNotExists(ctx context.Context, bucket string) error {
	c, err := cstorage.NewClient(ctx)
	if err != nil {
		return errors.Wrap(err, "getting storage client")
	}
	defer c.Close()

	_, err = c.Bucket(bucket).Attrs(ctx)

	if err == nil {
		// Bucket exists
		return nil
	}

	if err != cstorage.ErrBucketNotExist {
		return errors.Wrapf(err, "getting bucket %s", bucket)
	}

	if err := c.Bucket(bucket).Create(ctx, cb.GoogleCloudBuild.ProjectID, &cstorage.BucketAttrs{
		Name: bucket,
	}); err != nil {
		return err
	}
	logrus.Debugf("Created bucket %s in %s", bucket, cb.GoogleCloudBuild.ProjectID)
	return nil
}
