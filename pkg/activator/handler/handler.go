/*
Copyright 2018 The Knative Authors

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

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"

	"go.opencensus.io/plugin/ochttp"
	"go.opencensus.io/trace"
	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/logging"
	pkgnet "knative.dev/pkg/network"
	"knative.dev/pkg/tracing"
	tracingconfig "knative.dev/pkg/tracing/config"
	"knative.dev/serving/pkg/activator"
	activatorconfig "knative.dev/serving/pkg/activator/config"
	"knative.dev/serving/pkg/activator/util"
	"knative.dev/serving/pkg/network"
	"knative.dev/serving/pkg/queue"
)

// Throttler is the interface that Handler calls to Try to proxy the user request.
type Throttler interface {
	Try(context.Context, func(string) error) error
}

// activationHandler will wait for an active endpoint for a revision
// to be available before proxying the request
type activationHandler struct {
	transport        http.RoundTripper
	tracingTransport http.RoundTripper
	throttler        Throttler
	bufferPool       httputil.BufferPool
	kubeClient       kubernetes.Interface
}

// New constructs a new http.Handler that deals with revision activation.
func New(ctx context.Context, t Throttler, kubeClient kubernetes.Interface) http.Handler {
	defaultTransport := pkgnet.AutoTransport
	return &activationHandler{
		transport:        defaultTransport,
		tracingTransport: &ochttp.Transport{Base: defaultTransport},
		throttler:        t,
		bufferPool:       network.NewBufferPool(),
		kubeClient:       kubeClient,
	}
}

func (a *activationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logger := logging.FromContext(r.Context())
	tracingConfig := activatorconfig.FromContext(r.Context()).Tracing
	tracingEnabled := tracingConfig.Backend != tracingconfig.None
	tryContext, trySpan := r.Context(), (*trace.Span)(nil)
	if tracingEnabled {
		tryContext, trySpan = trace.StartSpan(r.Context(), "throttler_try")
	}

	name := r.Header.Get(activator.RevisionHeaderName)
	namespace := r.Header.Get(activator.RevisionHeaderNamespace)
	if tracingConfig.KubeTracing && trySpan.SpanContext().IsSampled() {
		lo := metav1.ListOptions{LabelSelector: fmt.Sprintf("app=%s", name)}
		watch, err := a.kubeClient.CoreV1().Pods(namespace).Watch(lo)
		if err != nil {
			logger.Errorw("Failed to set up pod watch for tracing", zap.Error(err))
		} else {
			defer watch.Stop()

			stopCh := make(chan struct{})
			defer close(stopCh)

			go tracing.TracePodStartup(tryContext, stopCh, watch.ResultChan())
		}
	}

	if err := a.throttler.Try(tryContext, func(dest string) error {
		trySpan.End()

		proxyCtx, proxySpan := r.Context(), (*trace.Span)(nil)
		if tracingEnabled {
			proxyCtx, proxySpan = trace.StartSpan(r.Context(), "activator_proxy")
		}
		a.proxyRequest(logger, w, r.WithContext(proxyCtx), &url.URL{
			Scheme: "http",
			Host:   dest,
		}, tracingEnabled)
		proxySpan.End()

		return nil
	}); err != nil {
		// Set error on our capacity waiting span and end it
		trySpan.Annotate([]trace.Attribute{trace.StringAttribute("activator.throttler.error", err.Error())}, "ThrottlerTry")
		trySpan.End()

		logger.Errorw("Throttler try error", zap.Error(err))

		switch err {
		case context.DeadlineExceeded, queue.ErrRequestQueueFull:
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}
}

func (a *activationHandler) proxyRequest(logger *zap.SugaredLogger, w http.ResponseWriter, r *http.Request, target *url.URL, tracingEnabled bool) {
	network.RewriteHostIn(r)
	r.Header.Set(network.ProxyHeaderName, activator.Name)

	// Setup the reverse proxy.
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.BufferPool = a.bufferPool
	proxy.Transport = a.transport
	if tracingEnabled {
		proxy.Transport = a.tracingTransport
	}
	proxy.FlushInterval = -1
	proxy.ErrorHandler = pkgnet.ErrorHandler(logger)
	util.SetupHeaderPruning(proxy)

	proxy.ServeHTTP(w, r)
}
