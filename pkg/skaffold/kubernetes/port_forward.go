package kubernetes

import (
	"context"
	"io"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/watch"
)

type PortForwarder struct {
	output      io.Writer
	podSelector PodSelector

	podWatcher watch.Interface

	forwardedPods map[string]*v1.Pod
}

func NewPortForwarder(out io.Writer, podSelector PodSelector, podWatcher watch.Interface) *PortForwarder {
	return &PortForwarder{
		output:        out,
		podSelector:   podSelector,
		podWatcher:    podWatcher,
		forwardedPods: map[string]*v1.Pod{},
	}
}

func (p *PortForwarder) Start(ctx context.Context) error {
	go func() {
		defer p.podWatcher.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-p.podWatcher.ResultChan():
				if !ok {
					return
				}

				if evt.Type != watch.Added && evt.Type != watch.Modified {
					continue
				}

				pod, ok := evt.Object.(*v1.Pod)
				if !ok {
					continue
				}

				if p.podSelector.Select(pod) {

				}
			}
		}
	}()

	return nil
}

func (p *PortForwarder) portForward(ctx context.Context, pod *v1.Pod) error {
	t := pod.
	return nil
}
