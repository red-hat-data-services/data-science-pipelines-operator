package controllers

import (
	"context"
	"fmt"

	"github.com/opendatahub-io/data-science-pipelines-operator/controllers/config"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=mutatingwebhookconfigurations,verbs=get;update,resourceNames=workflow-account
func getWorkflowAdmissionConfiguration(ctx context.Context, reader client.Reader) (*unstructured.Unstructured, error) {
	configuration := &unstructured.Unstructured{}
	configuration.SetAPIVersion("admissionregistration.k8s.io/v1")
	configuration.SetKind("MutatingWebhookConfiguration")
	err := reader.Get(ctx, client.ObjectKey{Name: "workflow-account"}, configuration)
	return configuration, err
}

func workflowAdmissionWebhook(configuration *unstructured.Unstructured) (map[string]any, error) {
	webhooks, _, err := unstructured.NestedSlice(configuration.Object, "webhooks")
	if err != nil || len(webhooks) != 1 {
		return nil, fmt.Errorf("workflow admission configuration must contain one webhook")
	}
	webhook, ok := webhooks[0].(map[string]any)
	if !ok || webhook["name"] != "workflow-account.opendatahub.io" {
		return nil, fmt.Errorf("workflow admission configuration has an unexpected webhook")
	}
	return webhook, nil
}

type WorkflowAdmissionMode int

const (
	WorkflowAdmissionStaged WorkflowAdmissionMode = iota
	WorkflowAdmissionEnforced
	WorkflowAdmissionRollback
)

const workflowAdmissionRollbackAnnotation = "pipelines.opendatahub.io/workflow-admission-rollback"

func GetWorkflowAdmissionMode(ctx context.Context, reader client.Reader) (WorkflowAdmissionMode, error) {
	configuration, err := getWorkflowAdmissionConfiguration(ctx, reader)
	if err != nil {
		return WorkflowAdmissionStaged, client.IgnoreNotFound(err)
	}
	if configuration.GetAnnotations()[workflowAdmissionRollbackAnnotation] == "true" {
		return WorkflowAdmissionRollback, nil
	}
	webhook, err := workflowAdmissionWebhook(configuration)
	if err != nil {
		return WorkflowAdmissionStaged, err
	}
	if webhook["failurePolicy"] == "Fail" {
		return WorkflowAdmissionEnforced, nil
	}
	return WorkflowAdmissionStaged, nil
}

func (r *DSPAReconciler) activateWorkflowAdmission(params *DSPAParams) error {
	ctx := context.Background()
	configuration, err := getWorkflowAdmissionConfiguration(ctx, r.Client)
	if apierrors.IsNotFound(err) {
		manifest, err := config.Manifest(r.Client, r.TemplatesPath+"workflow-admission/configuration.yaml.tmpl", params)
		if err != nil {
			return err
		}
		// Apply only on first registration. Later reconciles must preserve
		// both the injected CA and the activation state.
		resources := manifest.Resources()
		if len(resources) != 1 {
			return fmt.Errorf("expected exactly one workflow admission configuration")
		}
		if err := r.Create(ctx, &resources[0]); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		return fmt.Errorf("waiting for workflow admission to become active")
	}
	if err != nil {
		return err
	}
	if configuration.GetAnnotations()[workflowAdmissionRollbackAnnotation] == "true" {
		return fmt.Errorf("workflow admission rollback in progress")
	}
	webhook, err := workflowAdmissionWebhook(configuration)
	if err != nil {
		return err
	}
	caBundle, _, err := unstructured.NestedString(webhook, "clientConfig", "caBundle")
	if err != nil || caBundle == "" {
		return fmt.Errorf("waiting for OpenShift to inject the workflow admission CA bundle")
	}
	if err := r.checkWorkflowAdmission(params); err != nil {
		return err
	}
	if webhook["failurePolicy"] == "Fail" {
		return nil
	}
	webhook["failurePolicy"] = "Fail"
	if err := unstructured.SetNestedSlice(configuration.Object, []any{webhook}, "webhooks"); err != nil {
		return err
	}
	if err := r.Update(ctx, configuration); err != nil {
		return err
	}
	return r.checkWorkflowAdmission(params)
}
