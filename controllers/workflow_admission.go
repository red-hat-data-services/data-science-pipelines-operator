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
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	dspav1 "github.com/opendatahub-io/data-science-pipelines-operator/api/v1"
	"github.com/opendatahub-io/data-science-pipelines-operator/controllers/config"
	admissionv1 "k8s.io/api/admission/v1"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kuberand "k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// WorkflowAdmission authorizes the account on executable objects before the
// controller uses it. The request identity is supplied by Kubernetes.
type WorkflowAdmission struct {
	Reader     client.Reader
	Client     client.Client
	SigningKey *ecdsa.PrivateKey
	Mode       func(context.Context) (WorkflowAdmissionMode, error)
}

type workflowAdmissionObject struct {
	Metadata metav1.ObjectMeta `json:"metadata"`
	Spec     json.RawMessage   `json:"spec"`
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

	if len(apps.Items) == 0 {
		return admission.Allowed("")
	}
	if h.Mode != nil {
		mode, err := h.Mode(ctx)
		if err != nil {
			return admission.Errored(http.StatusServiceUnavailable, err)
		}
		if mode == WorkflowAdmissionRollback {
			return admission.Allowed("")
		}
		if mode == WorkflowAdmissionStaged {
			// Probe only TLS, routing and webhook startup in this phase. No
			// stored objects receive approvals before fail-closed activation.
			if req.DryRun != nil && *req.DryRun && req.Resource.Resource == workflowResource {
				account, _ := unmarshalWorkflowAccount(current.Spec)
				if account == "workflow-admission-probe" {
					return admission.Denied(workflowAccountDenied + ": admission endpoint ready")
				}
			}
			return admission.Allowed("")
		}
	}
	resource := req.Resource.Resource
	isWorkflow := resource == workflowResource
	isCron := resource == cronWorkflowResource
	isScheduled := resource == scheduledWorkflowResource
	isUpdate := req.Operation == admissionv1.Update

	var api, scheduler, argo, persistence bool
	for _, app := range apps.Items {
		prefix := "system:serviceaccount:" + req.Namespace + ":"
		switch req.UserInfo.Username {
		case prefix + apiServerDefaultResourceNamePrefix + app.Name:
			api = true
		case prefix + scheduledWorkflowDefaultResourceNamePrefix + app.Name:
			scheduler = true
		case prefix + workflowControllerDefaultResourceNamePrefix + app.Name:
			argo = true
		case prefix + "ds-pipeline-persistenceagent-" + app.Name:
			persistence = true
		}
	}
	previousApproval := previous.Metadata.Annotations[workflowApprovalAnnotation]
	currentApproval := current.Metadata.Annotations[workflowApprovalAnnotation]
	if req.SubResource != "" && previousApproval != currentApproval {
		return admission.Denied("workflow approval may not be changed on a subresource")
	}

	// The controller and persistence agent serialize full Workflow updates, and
	// the legacy scheduler also writes status through the main resource.
	if isUpdate && ((isWorkflow && (argo || persistence)) || (isCron && argo) || (isScheduled && scheduler)) {
		sameSpec := sameWorkflowSpec(previous.Spec, current.Spec, isWorkflow)
		if isCron {
			sameSpec = sameCronWorkflowSpec(previous.Spec, current.Spec)
		}
		if previousApproval == currentApproval && sameSpec {
			return admission.Allowed("")
		}
		return admission.Denied("controller may not change the approved workflow specification")
	}
	if req.SubResource != "" {
		if isScheduled {
			return admission.Denied("only the managed scheduler may update schedule status")
		}
		account, err := h.executionAccount(ctx, req.Namespace, resource, previous.Spec)
		if err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if requiresWorkflowAccountAuthorization(account) {
			return h.authorizeAccount(ctx, req, account)
		}
		return admission.Allowed("")
	}

	account, err := h.executionAccount(ctx, req.Namespace, resource, current.Spec)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if isScheduled && !api {
		return admission.Denied("submit the schedule through the pipeline API")
	}

	if !requiresWorkflowAccountAuthorization(account) {
		if isUpdate && !api {
			oldAccount, err := h.executionAccount(ctx, req.Namespace, resource, previous.Spec)
			if err != nil {
				return admission.Errored(http.StatusBadRequest, err)
			}
			if requiresWorkflowAccountAuthorization(oldAccount) {
				if decision := h.authorizeAccount(ctx, req, oldAccount); !decision.Allowed {
					return decision
				}
			}
		}
		return patchWorkflowApproval(req.Object.Raw, "", "", isScheduled)
	}

	claim := workflowApproval{
		Namespace: req.Namespace, Resource: resource, Name: current.Metadata.Name,
		UID: string(current.Metadata.UID), Account: account,
	}
	if claim.Name == "" {
		claim.Name = req.Name
	}
	generatedName := ""
	if claim.Name == "" && !isUpdate && current.Metadata.GenerateName != "" {
		prefix := current.Metadata.GenerateName
		if len(prefix) > 58 {
			prefix = prefix[:58]
		}
		generatedName = prefix + kuberand.String(5)
		claim.Name = generatedName
	}
	if !isWorkflow {
		claim.SpecHash, err = approvalSpecHash(current.Spec, resource)
		if err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
	}
	switch {
	case isWorkflow && !isUpdate && (scheduler || argo):
		parentResource := cronWorkflowResource
		if scheduler {
			parentResource = scheduledWorkflowResource
		}
		allowed, err := h.authorizedSchedule(ctx, req.Namespace, parentResource, current, account)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}
		if !allowed {
			return admission.Denied(workflowAccountDenied + ": approve the parent schedule before launching a named-account workflow")
		}
	case isScheduled && api:
		// CreateJob requests approval after authorizing the actual executable
		// account. Reconciliation can carry forward an existing approval for
		// the same executable spec.
		if current.Metadata.Annotations[workflowApprovalAPIRequest] != "true" {
			// Legacy schedules remain editable by the API without minting
			// authorization for their stored definition.
			if !verifyWorkflowApproval(h.SigningKey, previousApproval, claim) {
				return patchWorkflowApproval(req.Object.Raw, "", "", true)
			}
		}
	case isWorkflow && api:
		// The pipeline API authorizes the literal account before submission.
		literal, err := unmarshalWorkflowAccount(current.Spec)
		if err != nil || literal != account {
			return admission.Denied(workflowAccountDenied + ": pipeline API must authorize the effective account explicitly")
		}
	default:
		if isUpdate {
			oldAccount, err := h.executionAccount(ctx, req.Namespace, resource, previous.Spec)
			if err != nil {
				return admission.Errored(http.StatusBadRequest, err)
			}
			if oldAccount != account && requiresWorkflowAccountAuthorization(oldAccount) {
				if decision := h.authorizeAccount(ctx, req, oldAccount); !decision.Allowed {
					return decision
				}
			}
		}
		if decision := h.authorizeAccount(ctx, req, account); !decision.Allowed {
			return decision
		}
	}
	if claim.Name == "" {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("workflow has neither name nor generateName"))
	}
	token, err := claim.sign(h.SigningKey)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return patchWorkflowApproval(req.Object.Raw, token, generatedName, isScheduled)
}

// Only mutate the metadata wrapper so Kubernetes quantities and unknown spec
// fields round-trip exactly.
func patchWorkflowApproval(raw []byte, approval, generatedName string, clearAPIRequest bool) admission.Response {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	var metadata map[string]json.RawMessage
	if raw := object["metadata"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
	}
	if metadata == nil {
		metadata = map[string]json.RawMessage{}
	}
	var annotations map[string]string
	if raw := metadata["annotations"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &annotations); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	if clearAPIRequest {
		delete(annotations, workflowApprovalAPIRequest)
	}
	if approval == "" {
		delete(annotations, workflowApprovalAnnotation)
	} else {
		annotations[workflowApprovalAnnotation] = approval
	}
	if len(annotations) == 0 {
		delete(metadata, "annotations")
	} else {
		metadata["annotations"], _ = json.Marshal(annotations)
	}
	if generatedName != "" {
		metadata["name"], _ = json.Marshal(generatedName)
	}
	object["metadata"], _ = json.Marshal(metadata)
	updated, _ := json.Marshal(object)
	return admission.PatchResponseFromRaw(raw, updated)
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

func decodeWorkflowSpec(raw json.RawMessage) (map[string]any, error) {
	var spec map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	err := decoder.Decode(&spec)
	return spec, err
}

func sameWorkflowSpec(previous, current json.RawMessage, normalizeArgo bool) bool {
	oldSpec, oldErr := decodeWorkflowSpec(previous)
	newSpec, newErr := decodeWorkflowSpec(current)
	if oldErr != nil || newErr != nil {
		return false
	}
	if normalizeArgo {
		normalizeArgoWorkflowSpec(oldSpec)
		normalizeArgoWorkflowSpec(newSpec)
	}
	return reflect.DeepEqual(oldSpec, newSpec)
}

func sameCronWorkflowSpec(previous, current json.RawMessage) bool {
	a, err := decodeWorkflowSpec(previous)
	if err != nil {
		return false
	}
	b, err := decodeWorkflowSpec(current)
	if err != nil {
		return false
	}
	for _, cron := range []map[string]any{a, b} {
		workflow, _ := cron["workflowSpec"].(map[string]any)
		normalizeArgoWorkflowSpec(workflow)
	}
	return reflect.DeepEqual(a, b)
}

// Argo's typed Workflow updates serialize some zero-valued structs as empty
// objects. Ignore those representations only at their known spec paths.
func normalizeArgoWorkflowSpec(spec map[string]any) {
	removeEmptyObjects(spec, "arguments")
	claims, _ := spec["volumeClaimTemplates"].([]any)
	for _, value := range claims {
		claim, _ := value.(map[string]any)
		claimSpec, _ := claim["spec"].(map[string]any)
		normalizeResourceRequirements(claimSpec["resources"])
	}
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
	normalizeResourceRequirements(container["resources"])
	if container["name"] == "" {
		delete(container, "name")
	}
}

func normalizeResourceRequirements(value any) {
	requirements, _ := value.(map[string]any)
	for _, field := range []string{"requests", "limits"} {
		quantities, _ := requirements[field].(map[string]any)
		for name, raw := range quantities {
			var text string
			switch v := raw.(type) {
			case string:
				text = v
			case json.Number:
				text = v.String()
			default:
				continue
			}
			quantity, err := resource.ParseQuantity(text)
			if err != nil {
				continue
			}
			canonical := quantity.AsDec().String()
			if strings.Contains(canonical, ".") {
				canonical = strings.TrimRight(strings.TrimRight(canonical, "0"), ".")
			}
			quantities[name] = canonical
		}
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
