package controllers

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const workflowResource = "workflows"
const cronWorkflowResource = "cronworkflows"
const scheduledWorkflowResource = "scheduledworkflows"

func unmarshalWorkflowAccount(raw json.RawMessage) (string, error) {
	var workflow struct {
		Account string `json:"serviceAccountName"`
	}
	err := json.Unmarshal(raw, &workflow)
	return workflow.Account, err
}

func (h WorkflowAdmission) executionAccount(ctx context.Context, namespace, resource string, spec json.RawMessage) (string, error) {
	if len(spec) == 0 {
		return "", nil
	}
	switch resource {
	case cronWorkflowResource:
		var cron struct {
			WorkflowSpec json.RawMessage `json:"workflowSpec"`
		}
		if err := json.Unmarshal(spec, &cron); err != nil {
			return "", err
		}
		return h.executionAccount(ctx, namespace, workflowResource, cron.WorkflowSpec)
	case scheduledWorkflowResource:
		var schedule struct {
			ServiceAccount string `json:"serviceAccount"`
			Workflow       *struct {
				Spec json.RawMessage `json:"spec"`
			} `json:"workflow"`
		}
		if err := json.Unmarshal(spec, &schedule); err != nil {
			return "", err
		}
		if schedule.Workflow == nil || len(schedule.Workflow.Spec) == 0 || string(schedule.Workflow.Spec) == "null" {
			return schedule.ServiceAccount, nil
		}
		legacy := schedule.Workflow.Spec
		if legacy[0] == '"' {
			var embedded string
			if err := json.Unmarshal(legacy, &embedded); err != nil {
				return "", err
			}
			legacy = json.RawMessage(embedded)
		}
		var workflow struct {
			Spec json.RawMessage `json:"spec"`
		}
		if err := json.Unmarshal(legacy, &workflow); err != nil {
			return "", err
		}
		if len(workflow.Spec) != 0 {
			legacy = workflow.Spec
		}
		return h.executionAccount(ctx, namespace, workflowResource, legacy)
	default:
		var workflow struct {
			ServiceAccount string `json:"serviceAccountName"`
			Template       *struct {
				Name         string `json:"name"`
				ClusterScope bool   `json:"clusterScope"`
			} `json:"workflowTemplateRef"`
		}
		if err := json.Unmarshal(spec, &workflow); err != nil {
			return "", err
		}
		if workflow.ServiceAccount != "" || workflow.Template == nil {
			return workflow.ServiceAccount, nil
		}
		gvk := schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "WorkflowTemplate"}
		if workflow.Template.ClusterScope {
			gvk.Kind = "ClusterWorkflowTemplate"
			namespace = ""
		}
		template := &unstructured.Unstructured{}
		template.SetGroupVersionKind(gvk)
		if err := h.Reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: workflow.Template.Name}, template); err != nil {
			return "", fmt.Errorf("resolve workflow template service account: %w", err)
		}
		account, _, err := unstructured.NestedString(template.Object, "spec", "serviceAccountName")
		return account, err
	}
}

func (h WorkflowAdmission) authorizedSchedule(ctx context.Context, namespace, resource string, child workflowAdmissionObject, account string) (bool, error) {
	owner := metav1.GetControllerOf(&child.Metadata)
	if owner == nil {
		return false, nil
	}
	var apiVersion, kind string
	switch resource {
	case cronWorkflowResource:
		apiVersion, kind = "argoproj.io/v1alpha1", "CronWorkflow"
	case scheduledWorkflowResource:
		apiVersion, kind = "kubeflow.org/v1beta1", "ScheduledWorkflow"
	default:
		return false, fmt.Errorf("unknown workflow schedule resource")
	}
	if owner.APIVersion != apiVersion || owner.Kind != kind || owner.UID == "" {
		return false, nil
	}
	parent := &unstructured.Unstructured{}
	parent.SetAPIVersion(apiVersion)
	parent.SetKind(kind)
	if err := h.Reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: owner.Name}, parent); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if parent.GetUID() != owner.UID {
		return false, nil
	}
	spec, err := json.Marshal(parent.Object["spec"])
	if err != nil {
		return false, err
	}
	hash, err := approvalSpecHash(spec, resource)
	if err != nil {
		return false, err
	}
	return verifyWorkflowApproval(h.SigningKey, parent.GetAnnotations()[workflowApprovalAnnotation], workflowApproval{
		Namespace: namespace, Resource: resource, Name: owner.Name, UID: string(owner.UID), Account: account, SpecHash: hash,
	}), nil
}
