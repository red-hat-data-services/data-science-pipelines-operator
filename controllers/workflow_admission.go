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
			if isSchedule && req.Operation == admissionv1.Update && sameWorkflowSpec(previous.Spec, current.Spec) {
				return admission.Allowed("")
			}
		case prefix + workflowControllerDefaultResourceNamePrefix + app.Name,
			prefix + "ds-pipeline-persistenceagent-" + app.Name:
			if !isSchedule && req.Operation == admissionv1.Update && sameWorkflowSpec(previous.Spec, current.Spec) {
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

func sameWorkflowSpec(previous, current json.RawMessage) bool {
	var oldSpec, newSpec any
	return json.Unmarshal(previous, &oldSpec) == nil && json.Unmarshal(current, &newSpec) == nil &&
		reflect.DeepEqual(oldSpec, newSpec)
}
