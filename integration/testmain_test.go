// +build integration

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

package integration

import (
	"flag"
	"fmt"

	"os"
	"os/exec"
	"testing"

	"github.com/GoogleCloudPlatform/skaffold/pkg/skaffold/util"
	"github.com/sirupsen/logrus"
)

var provider = flag.String("provider", "", "cloud provider to run integration test")
var gkeComputeZone = flag.String("gke-compute-zone", "", "compute zone that gke cluster is located in")
var gkeClusterContext = flag.String("gke-cluster-context", "", "name of the gke cluster context")
var gcpProject = flag.String("gcp-project", "", "the project that the GKE cluster is part of")

func TestMain(m *testing.M) {
	flag.Parse()
	switch *provider {
	case "gke":
		fmt.Println("Running on GKE")
		gkeSetup()
	}
	os.Exit(m.Run())
}

func gkeSetup() {
	cmd := exec.Command("gcloud", "container", "clusters", "get-credentials", *gkeClusterContext, "--zone", *gkeComputeZone, "--project", *gcpProject)
	out, stderr, err := util.RunCommand(cmd, nil)
	if err != nil {
		logrus.Fatal("stdout %s, stderr: %s, err: %s", string(out), string(stderr), err)
	}
}
