//go:build test_all || test_unit

/*

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

package controllers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"

	dspav1 "github.com/opendatahub-io/data-science-pipelines-operator/api/v1"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	authn "k8s.io/api/authentication/v1"
	authz "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestWorkflowAdmission(t *testing.T) {
	const (
		direct         = "developer"
		api            = "system:serviceaccount:team:ds-pipeline-example"
		scheduler      = "system:serviceaccount:team:ds-pipeline-scheduledworkflow-example"
		controller     = "system:serviceaccount:team:ds-pipeline-workflow-controller-example"
		runner         = `{"spec":{"serviceAccountName":"pipeline-runner-example"}}`
		defaultAccount = `{"spec":{"serviceAccountName":"default"}}`
		schedule       = `{"spec":{"enabled":true}}`
		namedSchedule  = `{"spec":{"serviceAccount":"pipeline-runner-example"}}`
		minimalRun     = `{"spec":{"serviceAccountName":"pipeline-runner-example","templates":[
			{"name":"graph","dag":{"tasks":[{"name":"run","template":"job"}]}},
			{"name":"job","container":{"image":"example","command":["echo"]}}
		]}}`
		serializedRun = `{"spec":{"serviceAccountName":"pipeline-runner-example","arguments":{},"templates":[
			{"name":"graph","inputs":{},"outputs":{},"metadata":{},"dag":{"tasks":[{"name":"run","template":"job","arguments":{}}]}},
			{"name":"job","inputs":{},"outputs":{},"metadata":{},"container":{"name":"","image":"example","command":["echo"],"resources":{}}}
		]}}`
	)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	scheme := runtime.NewScheme()
	require.NoError(t, dspav1.AddToScheme(scheme))
	app := &dspav1.DataSciencePipelinesApplication{ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "team"}}
	argo := metav1.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}
	cron := metav1.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "cronworkflows"}
	schedules := metav1.GroupVersionResource{Group: "kubeflow.org", Version: "v1beta1", Resource: "scheduledworkflows"}

	tests := []struct {
		name        string
		user        string
		resource    metav1.GroupVersionResource
		spec        string
		previous    string
		grant       bool
		reviewError bool
		allowed     bool
	}{
		{name: "default workflow", user: direct, resource: argo, spec: defaultAccount, allowed: true},
		{name: "direct runner requires use", user: direct, resource: argo, spec: runner},
		{name: "direct runner granted", user: direct, resource: argo, spec: runner, grant: true, allowed: true},
		{name: "cannot update to runner", user: direct, resource: argo, spec: runner, previous: defaultAccount},
		{name: "cannot update protected workflow to default", user: direct, resource: argo, spec: defaultAccount, previous: runner},
		{name: "review error fails closed", user: direct, resource: argo, spec: runner, reviewError: true},
		{name: "managed API default-account submission", user: api, resource: argo, spec: defaultAccount, allowed: true},
		{name: "managed API named-account submission requires use", user: api, resource: argo, spec: runner},
		{name: "managed API named-account retry requires use", user: api, resource: argo, spec: runner, previous: runner},
		{name: "managed API cannot remove named account without use", user: api, resource: argo, spec: defaultAccount, previous: runner},
		{name: "managed API named-account submission with explicit use", user: api, resource: argo, spec: runner, grant: true, allowed: true},
		{name: "managed scheduler needs approved parent", user: scheduler, resource: argo, spec: runner},
		{name: "controller cannot submit a new named run", user: controller, resource: argo, spec: runner},
		{name: "controller can write cron status", user: controller, resource: cron, spec: `{"spec":{"workflowSpec":{"serviceAccountName":"pipeline-runner-example","arguments":{}}}}`, previous: `{"spec":{"workflowSpec":{"serviceAccountName":"pipeline-runner-example"}}}`, allowed: true},
		{name: "controller cannot replace cron account", user: controller, resource: cron, spec: `{"spec":{"workflowSpec":{"serviceAccountName":"other"}}}`, previous: `{"spec":{"workflowSpec":{"serviceAccountName":"pipeline-runner-example"}}}`},
		{name: "controller can write status", user: controller, resource: argo, spec: runner, previous: runner, allowed: true},
		{name: "controller can write status after Argo serialization", user: controller, resource: argo, spec: serializedRun, previous: minimalRun, allowed: true},
		{name: "submitter still needs use after equivalent serialization", user: direct, resource: argo, spec: serializedRun, previous: minimalRun},
		{name: "controller cannot change a container command", user: controller, resource: argo,
			spec:     `{"spec":{"serviceAccountName":"pipeline-runner-example","templates":[{"container":{"command":["goodbye"],"resources":{}}}]}}`,
			previous: `{"spec":{"serviceAccountName":"pipeline-runner-example","templates":[{"container":{"command":["hello"]}}]}}`},
		{name: "controller cannot add an unrelated empty field", user: controller, resource: argo,
			spec:     `{"spec":{"serviceAccountName":"pipeline-runner-example","templates":[{"securityContext":{}}]}}`,
			previous: `{"spec":{"serviceAccountName":"pipeline-runner-example","templates":[{}]}}`},
		{name: "direct schedule denied", user: direct, resource: schedules, spec: schedule},
		{name: "managed API schedule", user: api, resource: schedules, spec: schedule, allowed: true},
		{name: "managed API named-account schedule requires use", user: api, resource: schedules, spec: namedSchedule},
		{name: "managed API named-account schedule with explicit use", user: api, resource: schedules, spec: namedSchedule, grant: true, allowed: true},
		{name: "legacy request annotation does not approve an embedded account", user: api, resource: schedules, spec: `{"metadata":{"annotations":{"pipelines.opendatahub.io/authorize-scheduled-workflow":"true"}},"spec":{"workflow":{"spec":{"serviceAccountName":"pipeline-runner-example"}}}}`},
		{name: "managed scheduler can write status", user: scheduler, resource: schedules, spec: schedule, previous: schedule, allowed: true},
		{name: "scheduler cannot change executable schedule", user: scheduler, resource: schedules, spec: schedule, previous: `{"spec":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app.DeepCopy()).WithInterceptorFuncs(interceptor.Funcs{
				Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
					review := obj.(*authz.SubjectAccessReview)
					require.Equal(t, test.user, review.Spec.User)
					require.Equal(t, []string{"example-group"}, review.Spec.Groups)
					require.Equal(t, "team", review.Spec.ResourceAttributes.Namespace)
					require.Equal(t, "serviceaccounts", review.Spec.ResourceAttributes.Resource)
					require.Equal(t, "pipeline-runner-example", review.Spec.ResourceAttributes.Name)
					require.Equal(t, "use", review.Spec.ResourceAttributes.Verb)
					if test.reviewError {
						return errors.New("review unavailable")
					}
					review.Status.Allowed = test.grant
					return nil
				},
			}).Build()
			request := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				Namespace: "team", Name: "workflow", Resource: test.resource, Operation: admissionv1.Create,
				Object:   runtime.RawExtension{Raw: []byte(test.spec)},
				UserInfo: authn.UserInfo{Username: test.user, Groups: []string{"example-group"}},
			}}
			if test.previous != "" {
				request.Operation = admissionv1.Update
				request.OldObject.Raw = []byte(test.previous)
			}
			response := (WorkflowAdmission{Reader: kube, Client: kube, SigningKey: key}).Handle(context.Background(), request)
			require.Equal(t, test.allowed, response.Allowed, "%+v", response.Result)
		})
	}
}

func TestWorkflowSpecResourceQuantities(t *testing.T) {
	a := json.RawMessage(`{"templates":[{"container":{"resources":{"requests":{"cpu":"1","memory":"1Gi"}}}}]}`)
	b := json.RawMessage(`{"templates":[{"container":{"resources":{"requests":{"cpu":"1000m","memory":1073741824}}}}]}`)
	c := json.RawMessage(`{"templates":[{"container":{"resources":{"requests":{"cpu":"1100m","memory":1073741824}}}}]}`)
	require.True(t, sameWorkflowSpec(a, b, true))
	require.False(t, sameWorkflowSpec(a, c, true))
}

func TestNamedCronWorkflowRequiresApproval(t *testing.T) {
	ctx := context.Background()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	parent := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "CronWorkflow",
		"metadata": map[string]any{"name": "schedule", "namespace": "team", "uid": "parent-uid"},
		"spec":     map[string]any{"workflowSpec": map[string]any{"serviceAccountName": "pipeline-runner"}},
	}}
	reader := fake.NewClientBuilder().WithObjects(parent).Build()
	handler := WorkflowAdmission{Reader: reader, Client: reader, SigningKey: key}
	child := workflowAdmissionObject{Metadata: metav1.ObjectMeta{
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "argoproj.io/v1alpha1", Kind: "CronWorkflow", Name: "schedule", UID: "parent-uid"}},
	}}
	controller := true
	child.Metadata.OwnerReferences[0].Controller = &controller
	allowed, err := handler.authorizedSchedule(ctx, "team", cronWorkflowResource, child, "pipeline-runner")
	require.NoError(t, err)
	require.False(t, allowed)

	spec, err := json.Marshal(parent.Object["spec"])
	require.NoError(t, err)
	hash, err := approvalSpecHash(spec, cronWorkflowResource)
	require.NoError(t, err)
	claim := workflowApproval{Namespace: "team", Resource: cronWorkflowResource, Name: "schedule", UID: "parent-uid", Account: "pipeline-runner", SpecHash: hash}
	token, err := claim.sign(key)
	require.NoError(t, err)
	parent.SetAnnotations(map[string]string{workflowApprovalAnnotation: token})
	require.NoError(t, reader.Update(ctx, parent))

	allowed, err = handler.authorizedSchedule(ctx, "team", cronWorkflowResource, child, "pipeline-runner")
	require.NoError(t, err)
	require.True(t, allowed)
}
