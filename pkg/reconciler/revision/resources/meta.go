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

package resources

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"knative.dev/pkg/kmeta"
	"knative.dev/serving/pkg/apis/serving"
	v1 "knative.dev/serving/pkg/apis/serving/v1"
)

var (
	excludeLabels = sets.NewString(
		serving.RouteLabelKey,
		serving.RoutingStateLabelKey,
	)

	excludeAnnotations = sets.NewString(
		serving.RevisionLastPinnedAnnotationKey,
		serving.RevisionPreservedAnnotationKey,
		serving.RoutingStateModifiedAnnotationKey,
		serving.RoutesAnnotationKey,
	)
)

// makeLabels constructs the labels we will apply to K8s resources.
func makeLabels(revision *v1.Revision) map[string]string {
	labels := kmeta.FilterMap(revision.GetLabels(), excludeLabels.Has)
	labels = kmeta.UnionMaps(labels, map[string]string{
		serving.RevisionLabelKey: revision.Name,
		serving.RevisionUID:      string(revision.UID),
	})

	// If users don't specify an app: label we will automatically
	// populate it with the revision name to get the benefit of richer
	// tracing information.
	if _, ok := labels[AppLabelKey]; !ok {
		labels[AppLabelKey] = revision.Name
	}
	return labels
}

func makeAnnotations(revision *v1.Revision) map[string]string {
	return kmeta.UnionMaps(kmeta.FilterMap(revision.GetAnnotations(), excludeAnnotations.Has), map[string]string{
		"sidecar.istio.io/inject":                          "true",
		"sidecar.istio.io/interceptionMode":                "NONE",
		"status.sidecar.istio.io/port":                     "0",
		"sidecar.istio.io/rewriteAppHTTPProbers":           "false",
		"traffic.sidecar.istio.io/includeInboundPorts":     "",
		"traffic.sidecar.istio.io/includeOutputPorts":      "",
		"traffic.sidecar.istio.io/includeOutboundIPRanges": "",
		"proxy.istio.io/config": `proxyMetadata:
  OUTPUT_CERTS: /etc/istio-certs`,
		"sidecar.istio.io/userVolume":      `[{"name": "istio-certs", "emptyDir": {"medium": "Memory"}}]`,
		"sidecar.istio.io/userVolumeMount": `[{"name": "istio-certs", "mountPath": "/etc/istio-certs"}]`,
	})
}

// makeSelector constructs the Selector we will apply to K8s resources.
func makeSelector(revision *v1.Revision) *metav1.LabelSelector {
	return &metav1.LabelSelector{
		MatchLabels: map[string]string{
			serving.RevisionUID: string(revision.UID),
		},
	}
}
