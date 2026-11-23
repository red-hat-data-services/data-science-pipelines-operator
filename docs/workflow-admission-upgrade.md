# Workflow account admission in RHOAI 2.25

Build and package the matching DSPO and Argo workflow controller together.
Validate the final Pod policy with the Argo image digest selected for the release.

The operator takes checks in this order:

1. Deploy the webhook server, its Service, and its OpenShift serving certificate.
2. Register admission with `failurePolicy: Ignore`. The API server must
   reach the webhook with certificate verification before the operator changes
   the policy to `Fail`.
3. Deploy the configured Argo workflow controller with the public approval key.
   The private key is stored in `dspo-workflow-approval` in the operator
   namespace. For a custom operator namespace, set
   `DSPO_WORKFLOW_APPROVAL_KEY_NAMESPACE` to that namespace, where project users
   have no write or Secret access.

Pending and suspended Workflows with a non-default account require an
authorized spec update before further Pods can start. An authorized update
also approves an existing Argo CronWorkflow. Review the referenced template
and account before making that update.

The API server's identity is subject to the same `serviceaccounts/use`
check as other submitters. DSP submissions, retries, and schedules that use
a named service account, including the operator-configured pipeline runner,
require an explicit RBAC delegation to that API server identity. Any
delegation authorizes the API server for that account across its callers.

## Rollback

Pause the managed workflow controllers before rolling back admission.
Before stopping the webhook endpoint, atomically annotate the
`workflow-account` MutatingWebhookConfiguration with
`pipelines.opendatahub.io/workflow-admission-rollback: "true"` and change its
webhook failure policy to `Ignore`. The annotation prevents the operator
from restoring `Fail`. Verify the setting has propagated to the API server
before replacing the DSPO or removing its Service.

After the older DSPO and workflow controllers have rolled out, remove the
workflow-account webhook configuration and its signing Secret. A later
upgrade will issue a new signing key and require fresh approvals.
