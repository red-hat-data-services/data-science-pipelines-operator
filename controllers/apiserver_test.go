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
	"testing"

	"github.com/opendatahub-io/data-science-pipelines-operator/controllers/config"

	dspav1 "github.com/opendatahub-io/data-science-pipelines-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestDeployAPIServer(t *testing.T) {
	testNamespace := "testnamespace"
	testDSPAName := "testdspa"
	expectedAPIServerName := apiServerDefaultResourceNamePrefix + testDSPAName

	// Construct DSPASpec with deployed APIServer
	dspa := &dspav1.DataSciencePipelinesApplication{
		Spec: dspav1.DSPASpec{
			PodToPodTLS: boolPtr(false),
			APIServer: &dspav1.APIServer{
				Deploy: true,
			},
			MLMD: &dspav1.MLMD{
				Deploy: true,
			},
			Database: &dspav1.Database{
				DisableHealthCheck: false,
				MariaDB: &dspav1.MariaDB{
					Deploy: true,
				},
			},
			ObjectStorage: &dspav1.ObjectStorage{
				DisableHealthCheck: false,
				Minio: &dspav1.Minio{
					Deploy: false,
					Image:  "someimage",
				},
			},
		},
	}

	// Enrich DSPA with name+namespace
	dspa.Name = testDSPAName
	dspa.Namespace = testNamespace

	// Create Context, Fake Controller and Params
	ctx, params, reconciler := CreateNewTestObjects()
	err := params.ExtractParams(ctx, dspa, reconciler.Client, reconciler.Log)
	assert.Nil(t, err)

	params.ObjectStorageConnection.CredentialsSecret = &dspav1.S3CredentialSecret{SecretName: "artifacts"}

	// Assert APIServer Deployment doesn't yet exist
	deployment := &appsv1.Deployment{}
	created, err := reconciler.IsResourceCreated(ctx, deployment, expectedAPIServerName, testNamespace)
	assert.False(t, created)
	assert.Nil(t, err)

	// Reconciliation must replace the previous runner's rules on upgrade.
	runnerKey := types.NamespacedName{Name: "pipeline-runner-" + testDSPAName, Namespace: testNamespace}
	runnerRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: runnerKey.Name, Namespace: testNamespace},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"create"}}},
	}
	require.NoError(t, reconciler.Create(ctx, runnerRole))

	// Run test reconciliation
	err = reconciler.ReconcileAPIServer(ctx, dspa, params)
	assert.Nil(t, err)

	// Assert APIServer Deployment now exists
	deployment = &appsv1.Deployment{}
	created, err = reconciler.IsResourceCreated(ctx, deployment, expectedAPIServerName, testNamespace)
	assert.True(t, created)
	assert.Nil(t, err)

	require.NoError(t, reconciler.Get(ctx, runnerKey, runnerRole))
	assert.ElementsMatch(t, []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{"artifacts"}, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{"kfp-launcher"}, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"persistentvolumeclaims"}, Verbs: []string{"create", "get", "delete"}},
		{APIGroups: []string{"argoproj.io"}, Resources: []string{"workflowtaskresults"}, Verbs: []string{"create", "patch"}},
	}, runnerRole.Rules)

	// Ensure readiness is handled
	apiServerReady, err := reconciler.evaluateCondition(ctx, dspa, params.APIServerDefaultResourceName, config.APIServerReady)
	assert.Equal(t, "Deploying", apiServerReady.Reason)
	assert.Nil(t, err)
}

func TestDontDeployAPIServer(t *testing.T) {
	testNamespace := "testnamespace"
	testDSPAName := "testdspa"
	expectedAPIServerName := apiServerDefaultResourceNamePrefix + testDSPAName

	// Construct DSPASpec with non-deployed APIServer
	dspa := &dspav1.DataSciencePipelinesApplication{
		Spec: dspav1.DSPASpec{
			APIServer: &dspav1.APIServer{
				Deploy: false,
			},
		},
	}

	// Enrich DSPA with name+namespace
	dspa.Namespace = testNamespace
	dspa.Name = testDSPAName

	// Create Context, Fake Controller and Params
	ctx, params, reconciler := CreateNewTestObjects()

	// Ensure APIServer Deployment doesn't yet exist
	created, err := reconciler.IsResourceCreated(ctx, dspa, testDSPAName, testNamespace)
	assert.False(t, created)
	assert.Nil(t, err)

	// Run test reconciliation
	err = reconciler.ReconcileAPIServer(ctx, dspa, params)
	assert.Nil(t, err)

	// Ensure APIServer Deployment still doesn't exist
	created, err = reconciler.IsResourceCreated(ctx, dspa, expectedAPIServerName, testNamespace)
	assert.False(t, created)
	assert.Nil(t, err)
}

func TestApiServerEndpoints(t *testing.T) {
	testNamespace := "testnamespace"
	testDSPAName := "testdspa"
	expectedAPIServerName := apiServerDefaultResourceNamePrefix + testDSPAName

	// Construct DSPASpec with deployed APIServer
	dspa := &dspav1.DataSciencePipelinesApplication{
		Spec: dspav1.DSPASpec{
			PodToPodTLS: boolPtr(false),
			APIServer: &dspav1.APIServer{
				Deploy: true,
			},
			MLMD: &dspav1.MLMD{
				Deploy: true,
			},
			Database: &dspav1.Database{
				DisableHealthCheck: false,
				MariaDB: &dspav1.MariaDB{
					Deploy: true,
				},
			},
			ObjectStorage: &dspav1.ObjectStorage{
				DisableHealthCheck: false,
				Minio: &dspav1.Minio{
					Deploy: false,
					Image:  "someimage",
				},
			},
		},
	}

	// Enrich DSPA with name+namespace
	dspa.Name = testDSPAName
	dspa.Namespace = testNamespace

	// Create Context, Fake Controller and Params
	ctx, params, reconciler := CreateNewTestObjects()
	err := params.ExtractParams(ctx, dspa, reconciler.Client, reconciler.Log)
	assert.Nil(t, err)

	// Assert APIServer Deployment doesn't yet exist
	deployment := &appsv1.Deployment{}
	created, err := reconciler.IsResourceCreated(ctx, deployment, expectedAPIServerName, testNamespace)
	assert.False(t, created)
	assert.Nil(t, err)

	// Run test reconciliation
	err = reconciler.ReconcileAPIServer(ctx, dspa, params)
	assert.Nil(t, err)

	dspa_created := &dspav1.DataSciencePipelinesApplication{}
	created, err = reconciler.IsResourceCreated(ctx, dspa, testDSPAName, testNamespace)
	assert.NotNil(t, dspa_created.Status.Components.APIServer.Url)
	assert.NotNil(t, dspa_created.Status.Components.APIServer.ExternalUrl)
}
