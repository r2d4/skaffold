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
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"syscall"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/color"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/util"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/watch"
)

type PortForwarder struct {
	output      io.Writer
	podSelector PodSelector
	podWatcher  watch.Interface

	// forwardedPods is a map of portForwardEntry.key() -> portForwardEntry
	forwardedPods *sync.Map
}

type portForwardEntry struct {
	resourceVersion int
	podName         string
	containerName   string
	port            int32

	cmd *exec.Cmd
}

func NewPortForwarder(out io.Writer, podSelector PodSelector, podWatcher watch.Interface) *PortForwarder {
	return &PortForwarder{
		output:        out,
		podSelector:   podSelector,
		podWatcher:    podWatcher,
		forwardedPods: &sync.Map{},
	}
}

func (p *PortForwarder) cleanupPorts() {
	p.forwardedPods.Range(func(k, v interface{}) bool {
		entry := v.(*portForwardEntry)
		if err := entry.stop(); err != nil {
			logrus.Warnf("cleaning up port forwards", err)
		}
		return false
	})
}

func (p *PortForwarder) Start(ctx context.Context) error {
	go func() {
		defer p.podWatcher.Stop()

		for {
			select {
			case <-ctx.Done():
				p.cleanupPorts()
				return
			case evt, ok := <-p.podWatcher.ResultChan():
				if !ok {
					return
				}

				// Pods will never be "added" in a state that they are ready for port-forwarding
				// so only watch "modified" events
				if evt.Type != watch.Modified {
					continue
				}

				pod, ok := evt.Object.(*v1.Pod)
				if !ok {
					continue
				}
				if p.podSelector.Select(pod) && pod.Status.Phase == v1.PodRunning && pod.DeletionTimestamp == nil {
					go func() {
						if err := p.portForwardPod(ctx, pod); err != nil {
							logrus.Warnf("port forwarding pod failed: %s", err)
						}
					}()
				}
			}
		}
	}()

	return nil
}

func (p *PortForwarder) portForwardPod(ctx context.Context, pod *v1.Pod) error {
	resourceVersion, err := strconv.Atoi(pod.ResourceVersion)
	if err != nil {
		return errors.Wrap(err, "converting resource version to integer")
	}
	for _, c := range pod.Spec.Containers {
		for _, port := range c.Ports {
			entry := &portForwardEntry{
				resourceVersion: resourceVersion,
				podName:         pod.Name,
				containerName:   c.Name,
				port:            port.ContainerPort,
			}
			v, ok := p.forwardedPods.Load(entry.key())

			if ok {
				prevEntry := v.(*portForwardEntry)

				// Check if this is a new generation of pod
				if entry.resourceVersion > prevEntry.resourceVersion {
					if err := prevEntry.stop(); err != nil {
						return errors.Wrap(err, "terminating port-forward process")
					}
				}
			}

			if err := entry.forward(p.output, p.forwardedPods); err != nil {
				return errors.Wrap(err, "port forwarding")
			}
		}
	}

	return nil
}

func (p *portForwardEntry) forward(output io.Writer, forwardedPods *sync.Map) error {
	portNumber := fmt.Sprintf("%d", p.port)
	color.Default.Fprintln(output, fmt.Sprintf("Port Forwarding %s %d -> %d", p.podName, p.port, p.port))
	cmd := exec.Command("kubectl", "port-forward", fmt.Sprintf("pod/%s", p.podName), portNumber, portNumber)
	p.cmd = cmd

	forwardedPods.Store(p.key(), p)
	if out, err := util.RunCmdOut(cmd); err != nil {
		return errors.Wrapf(err, "port forwarding pod: %s, port: %s, err: %s", p.podName, portNumber, string(out))
	}
	return nil
}

func (p *portForwardEntry) key() string {
	return fmt.Sprintf("%s-%d", p.containerName, p.port)
}

func (p *portForwardEntry) stop() error {
	if p.cmd == nil {
		return fmt.Errorf("No port-forward command found for %s/%s:%d", p.podName, p.containerName, p.port)
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return errors.Wrap(err, "terminating port-forward process")
	}
	return nil
}
