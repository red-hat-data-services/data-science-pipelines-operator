/*
Copyright 2023.

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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"

	dspav1 "github.com/opendatahub-io/data-science-pipelines-operator/api/v1"
	"github.com/opendatahub-io/data-science-pipelines-operator/controllers/config"
	admissionv1 "k8s.io/api/admission/v1"
	authv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// WorkflowAdmission authorizes the account on executable objects before the
// controller uses it. The request identity is supplied by Kubernetes.
type WorkflowAdmission struct {
	Reader client.Reader
	Client client.Client
}

type workflowAdmissionObject struct {
	Spec json.RawMessage `json:"spec"`
}

func (obj workflowAdmissionObject) workflowAccount() (string, error) {
	var spec struct {
		ServiceAccountName string `json:"serviceAccountName"`
	}
	if len(obj.Spec) == 0 {
		return "", nil
	}
	err := json.Unmarshal(obj.Spec, &spec)
	return spec.ServiceAccountName, err
}

func requiresWorkflowAccountAuthorization(account string) bool {
	return account != "" && account != "default"
}

const workflowAccountDenied = "workflow service account use is not authorized"

func (h WorkflowAdmission) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Namespace == "" || (req.Operation != admissionv1.Create && req.Operation != admissionv1.Update) {
		return admission.Allowed("")
	}

	var management ArgoWorkflowsControllersConfig
	if err := json.Unmarshal([]byte(config.GetStringConfigWithDefault("DSPO.ArgoWorkflowsControllers", config.DefaultArgoWorkflowsControllers)), &management); err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if management.GetManagementState() != "Managed" {
		return admission.Allowed("")
	}

	var apps dspav1.DataSciencePipelinesApplicationList
	if err := h.Reader.List(ctx, &apps, client.InNamespace(req.Namespace)); err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	var current, previous workflowAdmissionObject
	if err := json.Unmarshal(req.Object.Raw, &current); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if req.Operation == admissionv1.Update {
		if err := json.Unmarshal(req.OldObject.Raw, &previous); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
	}

	isSchedule := req.Resource.Group == "kubeflow.org" && req.Resource.Resource == "scheduledworkflows"
	account, err := current.workflowAccount()
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	oldAccount, err := previous.workflowAccount()
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if !isSchedule && !requiresWorkflowAccountAuthorization(account) && !requiresWorkflowAccountAuthorization(oldAccount) {
		return admission.Allowed("")
	}

	protected := false
	for _, app := range apps.Items {
		protected = true
		prefix := "system:serviceaccount:" + req.Namespace + ":"
		switch req.UserInfo.Username {
		case prefix + apiServerDefaultResourceNamePrefix + app.Name:
			// The pipeline API authorizes the user's run or schedule before submitting it.
			return admission.Allowed("")
		case prefix + scheduledWorkflowDefaultResourceNamePrefix + app.Name:
			if !isSchedule && req.Operation == admissionv1.Create {
				return admission.Allowed("")
			}
			if isSchedule && req.Operation == admissionv1.Update && sameWorkflowSpec(previous.Spec, current.Spec, false) {
				return admission.Allowed("")
			}
		case prefix + workflowControllerDefaultResourceNamePrefix + app.Name,
			prefix + "ds-pipeline-persistenceagent-" + app.Name:
			if !isSchedule && req.Operation == admissionv1.Update && sameWorkflowSpec(previous.Spec, current.Spec, true) {
				return admission.Allowed("")
			}
		}
	}
	if !protected {
		return admission.Allowed("")
	}
	if isSchedule {
		// A raw schedule can cause the trusted scheduler to submit a Workflow.
		// Require the pipeline API to create or change its executable specification.
		return admission.Denied("submit the schedule through the pipeline API")
	}

	if account != oldAccount && requiresWorkflowAccountAuthorization(oldAccount) {
		if decision := h.authorizeAccount(ctx, req, oldAccount); !decision.Allowed {
			return decision
		}
	}
	if requiresWorkflowAccountAuthorization(account) {
		return h.authorizeAccount(ctx, req, account)
	}
	return admission.Allowed("")
}

func (h WorkflowAdmission) authorizeAccount(ctx context.Context, req admission.Request, account string) admission.Response {
	extra := make(map[string]authv1.ExtraValue, len(req.UserInfo.Extra))
	for key, value := range req.UserInfo.Extra {
		extra[key] = authv1.ExtraValue(value)
	}
	review := &authv1.SubjectAccessReview{Spec: authv1.SubjectAccessReviewSpec{
		User: req.UserInfo.Username, Groups: req.UserInfo.Groups, UID: req.UserInfo.UID, Extra: extra,
		ResourceAttributes: &authv1.ResourceAttributes{
			Namespace: req.Namespace, Resource: "serviceaccounts", Name: account, Verb: "use",
		},
	}}
	if err := h.Client.Create(ctx, review); err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if review.Status.EvaluationError != "" {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("could not authorize service account: %s", review.Status.EvaluationError))
	}
	if !review.Status.Allowed || review.Status.Denied {
		return admission.Denied(fmt.Sprintf("%s: %q", workflowAccountDenied, account))
	}
	return admission.Allowed("")
}

func sameWorkflowSpec(previous, current json.RawMessage, normalizeArgo bool) bool {
	decode := func(raw json.RawMessage) (map[string]any, error) {
		var spec map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		err := decoder.Decode(&spec)
		return spec, err
	}
	oldSpec, oldErr := decode(previous)
	newSpec, newErr := decode(current)
	if oldErr != nil || newErr != nil {
		return false
	}
	if normalizeArgo {
		normalizeArgoWorkflowSpec(oldSpec)
		normalizeArgoWorkflowSpec(newSpec)
	}
	return reflect.DeepEqual(oldSpec, newSpec)
}

// Argo's typed Workflow updates serialize some zero-valued structs as empty
// objects. Ignore those representations only at their known spec paths.
func normalizeArgoWorkflowSpec(spec map[string]any) {
	removeEmptyObjects(spec, "arguments")
	normalizeArgoHooks(spec["hooks"])
	normalizeArgoTemplate(spec["templateDefaults"])
	templates, _ := spec["templates"].([]any)
	for _, template := range templates {
		normalizeArgoTemplate(template)
	}
}

func normalizeArgoTemplate(value any) {
	template, ok := value.(map[string]any)
	if !ok {
		return
	}
	removeEmptyObjects(template, "inputs", "outputs", "metadata")
	for _, key := range []string{"container", "script"} {
		normalizeArgoContainer(template[key])
	}
	containerSet, _ := template["containerSet"].(map[string]any)
	containers, _ := containerSet["containers"].([]any)
	for _, container := range containers {
		normalizeArgoContainer(container)
	}
	for _, key := range []string{"initContainers", "sidecars"} {
		containers, _ := template[key].([]any)
		for _, container := range containers {
			normalizeArgoContainer(container)
		}
	}

	dag, _ := template["dag"].(map[string]any)
	tasks, _ := dag["tasks"].([]any)
	for _, task := range tasks {
		normalizeArgoStep(task)
	}
	steps, _ := template["steps"].([]any)
	for _, group := range steps {
		parallelSteps, _ := group.([]any)
		for _, step := range parallelSteps {
			normalizeArgoStep(step)
		}
	}
}

func normalizeArgoContainer(value any) {
	container, _ := value.(map[string]any)
	removeEmptyObjects(container, "resources")
	if container["name"] == "" {
		delete(container, "name")
	}
}

func normalizeArgoStep(value any) {
	step, _ := value.(map[string]any)
	removeEmptyObjects(step, "arguments")
	normalizeArgoHooks(step["hooks"])
	normalizeArgoTemplate(step["inline"])
}

func normalizeArgoHooks(value any) {
	hooks, _ := value.(map[string]any)
	for _, value := range hooks {
		hook, _ := value.(map[string]any)
		removeEmptyObjects(hook, "arguments")
	}
}

func removeEmptyObjects(object map[string]any, keys ...string) {
	for _, key := range keys {
		if value, ok := object[key].(map[string]any); ok && len(value) == 0 {
			delete(object, key)
		}
	}
}
