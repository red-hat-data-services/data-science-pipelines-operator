#!/bin/bash
# This is script is defined as the following:
# 1 - We declare the required environment variables
# 2 - Has the functions defined
# 3 - Setup the environment and run the tests by using the appropriated functions

set -e

# Env vars
echo "GIT_WORKSPACE=$GIT_WORKSPACE"
if [ "$GIT_WORKSPACE" = "" ]; then
    echo "GIT_WORKSPACE variable not defined. Should be the root of the source code. Example GIT_WORKSPACE=/home/dev/git/data-science-pipelines-operator" && exit 1
fi

CLEAN_INFRA=false
SKIP_DEPLOY=false
SKIP_CLEANUP=false
K8SAPISERVERHOST=""
DSPA_NAMESPACE="test-dspa"
DSPA_EXTERNAL_NAMESPACE="dspa-ext"
DSPA_K8S_NAMESPACE="test-k8s-dspa"
MINIO_NAMESPACE="test-minio"
MARIADB_NAMESPACE="test-mariadb"
PYPISERVER_NAMESPACE="test-pypiserver"
CERT_MANAGER_NAMESPACE="cert-manager"
ARGO_NAMESPACE="argo"
ARGO_VERSION="v3.6.7"
DEPLOY_EXTERNAL_ARGO=false
AWF_MANAGEMENT_STATE="Managed"
DSPA_DEPLOY_WAIT_TIMEOUT="300"
INTEGRATION_TESTS_DIR="${GIT_WORKSPACE}/tests"
DSPA_PATH="${GIT_WORKSPACE}/tests/resources/dspa-lite.yaml"
DSPA_EXTERNAL_PATH="${GIT_WORKSPACE}/tests/resources/dspa-external-lite.yaml"
DSPA_K8S_PATH="${GIT_WORKSPACE}/tests/resources/dspa-k8s.yaml"
CONFIG_DIR="${GIT_WORKSPACE}/config"
RESOURCES_DIR_CRD="${GIT_WORKSPACE}/.github/resources"
OPENDATAHUB_NAMESPACE="opendatahub"
RESOURCES_DIR_PYPI="${GIT_WORKSPACE}/.github/resources/pypiserver/base"
ENDPOINT_TYPE="service"
DSPO_IMAGE_REF="${DSPO_IMAGE_REF:-}"
CONTAINER_CLI="${CONTAINER_CLI:-docker}"
RUN_PKG_UPLOADER_IN_CONTAINER="${RUN_PKG_UPLOADER_IN_CONTAINER:-true}"

get_dspo_image() {
  if [ ! -z "$DSPO_IMAGE_REF" ]; then
    echo $DSPO_IMAGE_REF
  else
    if [ -z "$REGISTRY_ADDRESS" ]; then
      # this function is called by `IMG=$(get_dspo_image)` that captures the standard output of get_dspo_image
      set -x
      echo "REGISTRY_ADDRESS variable not defined."
      exit 1
    fi
    local image="${REGISTRY_ADDRESS}/data-science-pipelines-operator"
    echo $image
  fi
}

apply_crd() {
  echo "---------------------------------"
  echo "# Apply OCP CRDs"
  echo "---------------------------------"
  kubectl apply -f ${RESOURCES_DIR_CRD}/crds
  kubectl apply -f "${CONFIG_DIR}/crd/external/route.openshift.io_routes.yaml"
}

build_image() {
  IMG=$(get_dspo_image)
  echo "---------------------------------"
  echo "Building image: $IMG"
  echo "---------------------------------"
  ( cd $GIT_WORKSPACE && make podman-build -e IMG="$IMG" )
}

create_opendatahub_namespace() {
  echo "---------------------------------"
  echo "Create opendatahub namespace"
  echo "---------------------------------"
  kubectl get namespace $OPENDATAHUB_NAMESPACE >/dev/null 2>&1 || \
  kubectl create namespace $OPENDATAHUB_NAMESPACE
}

create_argo_namespace() {
  echo "---------------------------------"
  echo "Create Argo namespace"
  echo "---------------------------------"
  kubectl get namespace $ARGO_NAMESPACE >/dev/null 2>&1 || \
  kubectl create namespace $ARGO_NAMESPACE
}

deploy_argo_lite() {
  echo "---------------------------------"
  echo "Deploy Argo Lite"
  echo "---------------------------------"
  ( cd "${GIT_WORKSPACE}/.github/resources/argo-lite" && kubectl -n $OPENDATAHUB_NAMESPACE apply -k . )
}

deploy_argo_external() {
  echo "---------------------------------"
  echo "Deploy External Argo"
  echo "---------------------------------"
  kubectl apply -n $ARGO_NAMESPACE -f https://github.com/argoproj/argo-workflows/releases/download/$ARGO_VERSION/install.yaml
}

patch_external_argo_security_context() {
  echo "---------------------------------"
  echo "Patch External Argo workflow-controller security context"
  echo "---------------------------------"
  kubectl -n $ARGO_NAMESPACE patch deployment workflow-controller \
    --type='strategic' \
    --patch-file "${GIT_WORKSPACE}/.github/resources/argo-external/workflow-controller-deployment-patch.yaml"
}

patch_external_argo_executor_security_context() {
  echo "---------------------------------"
  echo "Patch External Argo workflow-controller executor security context"
  echo "---------------------------------"
  kubectl -n $ARGO_NAMESPACE patch configmap workflow-controller-configmap \
    --type='merge' \
    --patch-file "${GIT_WORKSPACE}/.github/resources/argo-external/workflow-controller-configmap-patch.yaml"
}

deploy_dspo() {
  IMG=$(get_dspo_image)
  echo "---------------------------------"
  echo "Deploying DSPO: $IMG"
  echo "---------------------------------"
  ( cd $GIT_WORKSPACE && make deploy -e IMG="$IMG" )
}

deploy_dspo_kind() {
  IMG=$(get_dspo_image)
  echo "---------------------------------"
  echo "Push DSPO Image and Deploying DSPO on Kind: $IMG"
  echo "---------------------------------"
  ( cd $GIT_WORKSPACE && make podman-push -e IMG="$IMG" )
  ( cd $GIT_WORKSPACE && make deploy-kind -e IMG="$IMG" )
}

deploy_minio() {
  echo "---------------------------------"
  echo "Create Minio Namespace"
  echo "---------------------------------"
  kubectl create namespace $MINIO_NAMESPACE
  echo "---------------------------------"
  echo "Deploy Minio"
  echo "---------------------------------"
  ( cd "${GIT_WORKSPACE}/.github/resources/minio" && kubectl -n $MINIO_NAMESPACE apply -k . )
}

deploy_mariadb() {
  echo "---------------------------------"
  echo "Create MariaDB Namespace"
  echo "---------------------------------"
  kubectl create namespace $MARIADB_NAMESPACE
  echo "---------------------------------"
  echo "Deploy MariaDB"
  echo "---------------------------------"
  ( cd "${GIT_WORKSPACE}/.github/resources/mariadb" && kubectl -n $MARIADB_NAMESPACE apply -k . )
}

deploy_pypi_server() {
  echo "---------------------------------"
  echo "Create Pypiserver Namespace"
  echo "---------------------------------"
  kubectl create namespace $PYPISERVER_NAMESPACE
  echo "---------------------------------"
  echo "Deploy pypi-server"
  echo "---------------------------------"
  ( cd "${GIT_WORKSPACE}/.github/resources/pypiserver/base" && kubectl -n $PYPISERVER_NAMESPACE apply -k . )
}

deploy_cert_manager() {
  echo "---------------------------------"
  echo "Create Cert Manager Namespace"
  echo "---------------------------------"
  kubectl create namespace $CERT_MANAGER_NAMESPACE
  echo "---------------------------------"
  echo "Deploy Cert Manager"
  echo "---------------------------------"
  ( kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml )
}

wait_for_dspo_dependencies() {
  echo "---------------------------------"
  echo "Wait for DSPO Dependencies"
  echo "---------------------------------"
  kubectl wait -n $OPENDATAHUB_NAMESPACE --timeout=60s --for=condition=Available=true deployment data-science-pipelines-operator-controller-manager
}

wait_for_dspo_redeploy() {
  echo "---------------------------------"
  echo "Wait for DSPO Redeploy"
  echo "---------------------------------"
  sleep_amount=10
  counter=0
  max_counter=20
  sleep $sleep_amount  # Initial sleep to allow for deployment to roll out new pod
  while [ $counter -lt $max_counter ]; do
    echo "Waiting for DSPO to redeploy, attempt $counter out of $max_counter..."
    num_pods=`kubectl get pods -n $OPENDATAHUB_NAMESPACE -l app.kubernetes.io/name=data-science-pipelines-operator --no-headers | wc -l`
    if [ $num_pods -eq 1 ]; then
      break
    fi
    counter=$((counter+1))
    sleep $sleep_amount
  done
  if [ $counter -eq $max_counter ]; then
    echo "Error:DSPO did not redeploy $(($counter * $sleep_amount)) seconds."
    exit 1
  fi
  echo "DSPO redeployed after $(($counter * $sleep_amount)) seconds."
}

wait_for_dependencies() {
  echo "---------------------------------"
  echo "Wait for Dependencies (Minio, Mariadb, Pypi server)"
  echo "---------------------------------"
  kubectl wait -n $MARIADB_NAMESPACE --timeout=60s --for=condition=Available=true deployment mariadb
  kubectl wait -n $MINIO_NAMESPACE --timeout=60s --for=condition=Available=true deployment minio
  kubectl wait -n $PYPISERVER_NAMESPACE --timeout=60s --for=condition=Available=true deployment pypi-server
}

wait_for_mlmd_grpc_external_namespace() {
  echo "---------------------------------"
  echo "Wait for MLMD GRPC in External Namespace"
  echo "---------------------------------"
  local deployment_name="ds-pipeline-metadata-grpc-dspa-ext"
  local timeout="${DSPA_DEPLOY_WAIT_TIMEOUT}s"
  local sleep_amount=5
  local counter=0
  local max_counter=24
  while [ "$counter" -lt "$max_counter" ]; do
    if kubectl get deployment -n "$DSPA_EXTERNAL_NAMESPACE" "$deployment_name" >/dev/null 2>&1; then
      break
    fi
    echo "Waiting for $deployment_name to be created, attempt $counter out of $max_counter..."
    counter=$((counter+1))
    sleep "$sleep_amount"
  done
  if [ "$counter" -eq "$max_counter" ]; then
    echo "Error: $deployment_name was not created after $(($counter * $sleep_amount)) seconds."
    exit 1
  fi

  kubectl wait -n "$DSPA_EXTERNAL_NAMESPACE" --timeout="$timeout" --for=condition=Available=true deployment "$deployment_name"

  counter=0
  while [ "$counter" -lt "$max_counter" ]; do
    endpoints=$(kubectl get endpoints -n "$DSPA_EXTERNAL_NAMESPACE" metadata-grpc-service -o jsonpath='{.subsets[*].addresses[*].ip}' 2>/dev/null || true)
    if [ -n "$endpoints" ]; then
      echo "MLMD GRPC endpoints are ready."
      return 0
    fi
    echo "Waiting for metadata-grpc-service endpoints, attempt $counter out of $max_counter..."
    counter=$((counter+1))
    sleep "$sleep_amount"
  done
  echo "Error: metadata-grpc-service endpoints are not ready after $(($counter * $sleep_amount)) seconds."
  exit 1
}

upload_python_packages_to_pypi_server() {
  echo "---------------------------------"
  echo "Upload Python Packages to pypi-server"
  echo "---------------------------------"
  ( cd "${GIT_WORKSPACE}/.github/scripts/python_package_upload" && sh package_upload_run.sh)
}

create_dspa_namespace() {
  echo "---------------------------------"
  echo "Create DSPA Namespace"
  echo "---------------------------------"
  kubectl get namespace $DSPA_NAMESPACE >/dev/null 2>&1 || \
  kubectl create namespace $DSPA_NAMESPACE
}

create_namespace_dspa_external_connections() {
  echo "---------------------------------"
  echo "Create Namespace for DSPA with External connections"
  echo "---------------------------------"
  kubectl create namespace $DSPA_EXTERNAL_NAMESPACE
}

create_dspa_k8s_namespace() {
  echo "---------------------------------"
  echo "Create DSPA Namespace with Kubernetes Pipeline Storage"
  echo "---------------------------------"
  kubectl get namespace $DSPA_K8S_NAMESPACE >/dev/null 2>&1 || \
  kubectl create namespace $DSPA_K8S_NAMESPACE
}

apply_mariadb_minio_secrets_configmaps_external_namespace() {
  echo "---------------------------------"
  echo "Apply MariaDB and Minio Secrets and Configmaps in the External Namespace"
  echo "---------------------------------"
  ( cd "${GIT_WORKSPACE}/.github/resources/external-pre-reqs" && kubectl -n $DSPA_EXTERNAL_NAMESPACE apply -k . )
}

apply_service_ca_configmap_external_namespace() {
  echo "---------------------------------"
  echo "Apply service-ca ConfigMap in External Namespace"
  echo "---------------------------------"
  tmp_ca_file=$(mktemp)
  kubectl -n $DSPA_EXTERNAL_NAMESPACE get configmap root-ca -o jsonpath='{.data.public\.crt}' > "$tmp_ca_file"
  kubectl -n $DSPA_EXTERNAL_NAMESPACE create configmap openshift-service-ca.crt \
    --from-file=service-ca.crt="$tmp_ca_file" \
    --dry-run=client -o yaml | kubectl apply -f -
  rm -f "$tmp_ca_file"
}

apply_mlmd_tls_secret_external_namespace() {
  echo "---------------------------------"
  echo "Apply MLMD TLS Secret in External Namespace"
  echo "---------------------------------"
  if ! command -v openssl >/dev/null 2>&1; then
    echo "Error: openssl is required to generate MLMD TLS certificates"
    exit 1
  fi

  local tmp_dir
  tmp_dir=$(mktemp -d)
  local tls_key="${tmp_dir}/tls.key"
  local tls_crt="${tmp_dir}/tls.crt"
  local secret_name="ds-pipeline-metadata-grpc-tls-certs-dspa-ext"
  local dns0="metadata-grpc-service"
  local dns1="metadata-grpc-service.${DSPA_EXTERNAL_NAMESPACE}"
  local dns2="metadata-grpc-service.${DSPA_EXTERNAL_NAMESPACE}.svc"
  local dns3="metadata-grpc-service.${DSPA_EXTERNAL_NAMESPACE}.svc.cluster.local"
  local san="subjectAltName=DNS:${dns0},DNS:${dns1},DNS:${dns2},DNS:${dns3}"

  openssl req -x509 -nodes -newkey rsa:2048 \
    -keyout "$tls_key" \
    -out "$tls_crt" \
    -days 365 \
    -subj "/CN=${dns3}" \
    -addext "$san"

  kubectl -n $DSPA_EXTERNAL_NAMESPACE create secret tls "$secret_name" \
    --cert="$tls_crt" \
    --key="$tls_key" \
    --dry-run=client -o yaml | kubectl apply -f -

  rm -rf "$tmp_dir"
}

apply_apiserver_proxy_tls_secret_external_namespace() {
  echo "---------------------------------"
  echo "Apply API Server Proxy TLS Secret in External Namespace"
  echo "---------------------------------"
  if ! command -v openssl >/dev/null 2>&1; then
    echo "Error: openssl is required to generate API Server TLS certificates"
    exit 1
  fi

  local tmp_dir
  tmp_dir=$(mktemp -d)
  local tls_key="${tmp_dir}/tls.key"
  local tls_crt="${tmp_dir}/tls.crt"
  local secret_name="ds-pipelines-proxy-tls-dspa-ext"
  local dns0="ds-pipeline-dspa-ext"
  local dns1="ds-pipeline-dspa-ext.${DSPA_EXTERNAL_NAMESPACE}"
  local dns2="ds-pipeline-dspa-ext.${DSPA_EXTERNAL_NAMESPACE}.svc"
  local dns3="ds-pipeline-dspa-ext.${DSPA_EXTERNAL_NAMESPACE}.svc.cluster.local"
  local dns4="ml-pipeline"
  local dns5="ml-pipeline.${DSPA_EXTERNAL_NAMESPACE}"
  local dns6="ml-pipeline.${DSPA_EXTERNAL_NAMESPACE}.svc"
  local dns7="ml-pipeline.${DSPA_EXTERNAL_NAMESPACE}.svc.cluster.local"
  local san="subjectAltName=DNS:${dns0},DNS:${dns1},DNS:${dns2},DNS:${dns3},DNS:${dns4},DNS:${dns5},DNS:${dns6},DNS:${dns7}"

  openssl req -x509 -nodes -newkey rsa:2048 \
    -keyout "$tls_key" \
    -out "$tls_crt" \
    -days 365 \
    -subj "/CN=${dns3}" \
    -addext "$san"

  kubectl -n "$DSPA_EXTERNAL_NAMESPACE" create secret tls "$secret_name" \
    --cert="$tls_crt" \
    --key="$tls_key" \
    --dry-run=client -o yaml | kubectl apply -f -

  local root_ca_bundle="${tmp_dir}/root-ca-public.crt"
  local service_ca_bundle="${tmp_dir}/service-ca.crt"
  local generated_tls_cert
  generated_tls_cert="$(cat "$tls_crt")"
  local existing_root_ca
  existing_root_ca="$(kubectl -n "$DSPA_EXTERNAL_NAMESPACE" get configmap root-ca -o jsonpath='{.data.public\.crt}')"
  local existing_service_ca
  existing_service_ca="$(kubectl -n "$DSPA_EXTERNAL_NAMESPACE" get configmap openshift-service-ca.crt -o jsonpath='{.data.service-ca\.crt}')"

  printf '%s\n%s\n' "$existing_root_ca" "$generated_tls_cert" > "$root_ca_bundle"
  printf '%s\n%s\n' "$existing_service_ca" "$generated_tls_cert" > "$service_ca_bundle"

  kubectl -n "$DSPA_EXTERNAL_NAMESPACE" create configmap root-ca \
    --from-file=public.crt="$root_ca_bundle" \
    --dry-run=client -o yaml | kubectl apply -f -
  kubectl -n "$DSPA_EXTERNAL_NAMESPACE" create configmap openshift-service-ca.crt \
    --from-file=service-ca.crt="$service_ca_bundle" \
    --dry-run=client -o yaml | kubectl apply -f -

  rm -rf "$tmp_dir"
}

apply_pip_server_configmap() {
  echo "---------------------------------"
  echo "Apply PIP Server ConfigMap"
  echo "---------------------------------"
  for ns in $DSPA_NAMESPACE $DSPA_K8S_NAMESPACE; do
    echo "Applying ConfigMap in namespace: $ns"
    ( cd "${GIT_WORKSPACE}/.github/resources/pypiserver/base" && kubectl apply -f "$RESOURCES_DIR_PYPI/nginx-tls-config.yaml" -n "$ns" )
  done
}

apply_webhook_certs() {
  echo "---------------------------------"
  echo "Apply Webhook Certs"
  echo "---------------------------------"
  ( cd "${GIT_WORKSPACE}/.github/resources/webhook" && kubectl -n $OPENDATAHUB_NAMESPACE apply -k . )
}

run_tests() {
  echo "---------------------------------"
  echo "Run tests"
  echo "---------------------------------"
  ( cd $GIT_WORKSPACE && make integrationtest K8SAPISERVERHOST=${K8SAPISERVERHOST} DSPANAMESPACE=${DSPA_NAMESPACE} DSPAPATH=${DSPA_PATH} ENDPOINT_TYPE=${ENDPOINT_TYPE} INTTEST_AWF_MANAGEMENT_STATE=${AWF_MANAGEMENT_STATE} INTTEST_SKIP_DEPLOY=${SKIP_DEPLOY} INTTEST_SKIP_CLEANUP=${SKIP_CLEANUP} )
}

run_tests_external_argo() {
  echo "---------------------------------"
  echo "Run tests"
  echo "---------------------------------"
  ( cd $GIT_WORKSPACE && make integrationtest K8SAPISERVERHOST=${K8SAPISERVERHOST} DSPANAMESPACE=${DSPA_NAMESPACE} DSPAPATH=${DSPA_PATH} ENDPOINT_TYPE=${ENDPOINT_TYPE} INTTEST_AWF_MANAGEMENT_STATE=${AWF_MANAGEMENT_STATE} INTTEST_SKIP_DEPLOY=${SKIP_DEPLOY} INTTEST_SKIP_CLEANUP=${SKIP_CLEANUP} )
}

run_tests_dspa_external_connections() {
  echo "---------------------------------"
  echo "Run tests for DSPA with External Connections"
  echo "---------------------------------"
  local external_skip_deploy="${SKIP_DEPLOY}"
  if [ "$SKIP_DEPLOY" = false ]; then
    echo "Pre-deploy DSPA external to gate on MLMD readiness"
    kubectl -n "$DSPA_EXTERNAL_NAMESPACE" apply -f "$DSPA_EXTERNAL_PATH"
    wait_for_mlmd_grpc_external_namespace
    external_skip_deploy=true
  fi
  ( cd $GIT_WORKSPACE && make integrationtest K8SAPISERVERHOST=${K8SAPISERVERHOST} DSPANAMESPACE=${DSPA_EXTERNAL_NAMESPACE} DSPAPATH=${DSPA_EXTERNAL_PATH} ENDPOINT_TYPE=${ENDPOINT_TYPE} MINIONAMESPACE=${MINIO_NAMESPACE} INTTEST_AWF_MANAGEMENT_STATE=${AWF_MANAGEMENT_STATE} INTTEST_SKIP_DEPLOY=${external_skip_deploy} INTTEST_SKIP_CLEANUP=${SKIP_CLEANUP})
}

run_tests_dspa_k8s() {
  echo "---------------------------------"
  echo "Run tests for DSPA with Kubernetes Pipeline Storage"
  echo "---------------------------------"
  if [ "$TARGET" = "kind" ]; then
    echo "Detected kind target: deploying cert-manager"
    deploy_cert_manager
    echo "Waiting for Cert Manager pods to be ready"
    kubectl wait -n $CERT_MANAGER_NAMESPACE --timeout=90s --for=condition=Ready pods --all
    apply_webhook_certs
  fi
  ( cd $GIT_WORKSPACE && make integrationtest K8SAPISERVERHOST=${K8SAPISERVERHOST} DSPANAMESPACE=${DSPA_K8S_NAMESPACE} DSPAPATH=${DSPA_K8S_PATH} ENDPOINT_TYPE=${ENDPOINT_TYPE} INTTEST_AWF_MANAGEMENT_STATE=${AWF_MANAGEMENT_STATE} INTTEST_SKIP_DEPLOY=${SKIP_DEPLOY} INTTEST_SKIP_CLEANUP=${SKIP_CLEANUP})
}

update_dspo_env() {
  echo "---------------------------------"
  echo "Update DSPO Environment Variable"
  echo "---------------------------------"
  envkey=$1
  envval=$2
  echo "Updating DSPO Environment Variable: $envkey to $envval"
  kubectl set env -n $OPENDATAHUB_NAMESPACE deployment/data-science-pipelines-operator-controller-manager $envkey="$envval"
}

undeploy_kind_resources() {
  echo "---------------------------------"
  echo "Clean up resources created for testing on kind"
  echo "---------------------------------"
  ( cd $GIT_WORKSPACE && make undeploy-kind )
}

remove_namespace_created_for_rhoai() {
  echo "---------------------------------"
  echo "Clean up resources created for testing on RHOAI"
  echo "---------------------------------"
  kubectl delete projects $DSPA_NAMESPACE --now || true
  kubectl delete projects $DSPA_EXTERNAL_NAMESPACE --now || true
  kubectl delete projects $DSPA_K8S_NAMESPACE --now || true
  kubectl delete projects $MINIO_NAMESPACE --now || true
  kubectl delete projects $MARIADB_NAMESPACE --now || true
  kubectl delete projects $PYPISERVER_NAMESPACE --now || true
}

setup_kind_requirements() {
  apply_crd
  build_image
  create_opendatahub_namespace
  deploy_argo_lite
  deploy_dspo_kind
  deploy_minio
  deploy_mariadb
  deploy_pypi_server
  wait_for_dspo_dependencies
  wait_for_dependencies
  upload_python_packages_to_pypi_server
  create_dspa_namespace
  create_namespace_dspa_external_connections
  create_dspa_k8s_namespace
  apply_mariadb_minio_secrets_configmaps_external_namespace
  apply_service_ca_configmap_external_namespace
  apply_mlmd_tls_secret_external_namespace
  apply_apiserver_proxy_tls_secret_external_namespace
  apply_pip_server_configmap
}

setup_openshift_ci_requirements() {
  apply_crd
  create_opendatahub_namespace
  deploy_argo_lite
  deploy_dspo
  deploy_minio
  deploy_mariadb
  deploy_pypi_server
  wait_for_dspo_dependencies
  wait_for_dependencies
  upload_python_packages_to_pypi_server
  create_dspa_namespace
  create_namespace_dspa_external_connections
  create_dspa_k8s_namespace
  apply_mariadb_minio_secrets_configmaps_external_namespace
  apply_service_ca_configmap_external_namespace
  apply_mlmd_tls_secret_external_namespace
  apply_apiserver_proxy_tls_secret_external_namespace
  apply_pip_server_configmap
}

setup_rhoai_requirements() {
  deploy_minio
  deploy_mariadb
  deploy_pypi_server
  wait_for_dependencies
  upload_python_packages_to_pypi_server
  create_dspa_namespace
  create_namespace_dspa_external_connections
  create_dspa_k8s_namespace
  apply_mariadb_minio_secrets_configmaps_external_namespace
  apply_service_ca_configmap_external_namespace
  apply_mlmd_tls_secret_external_namespace
  apply_apiserver_proxy_tls_secret_external_namespace
  apply_pip_server_configmap
}

setup_external_argo() {
  update_dspo_env "DSPO_ARGOWORKFLOWSCONTROLLERS" "{\"managementState\": \"$AWF_MANAGEMENT_STATE\"}"
  create_argo_namespace
  deploy_argo_external
  patch_external_argo_security_context
  patch_external_argo_executor_security_context
  wait_for_dspo_redeploy
}

# Run
while [ "$#" -gt 0 ]; do
  case "$1" in
    --skip-deploy)
      SKIP_DEPLOY=true
      shift
      ;;
    --skip-cleanup)
      SKIP_CLEANUP=true
      shift
      ;;
    --kind)
      TARGET="kind"
      shift
      ;;
    --openshift-ci)
      TARGET="openshift-ci"
      shift
      ;;
    --rhoai)
      TARGET="rhoai"
      shift
      ;;
    # The clean-infra option is helpful when rerunning tests on the same target environment, as it eliminates
    # the need to manually delete the necessary infrastructure. By default, this setting is set to false.
    # If true, before running the test, it delete the necessary infrastructure.
    --clean-infra)
      CLEAN_INFRA=true
      shift
      ;;
    --k8s-api-server-host)
      shift
      if [[ -n "$1" ]]; then
        K8SAPISERVERHOST="$1"
        shift
      else
        echo "Error: --k8s-api-server-host requires a value"
        exit 1
      fi
      ;;
    --dspa-namespace)
      shift
      if [[ -n "$1" ]]; then
        DSPA_NAMESPACE="$1"
        shift
      else
        echo "Error: --dspa-namespace requires a value"
        exit 1
      fi
      ;;
    --dspa-external-namespace)
      shift
      if [[ -n "$1" ]]; then
        DSPA_EXTERNAL_NAMESPACE="$1"
        shift
      else
        echo "Error: --dspa-external-namespace requires a value"
        exit 1
      fi
      ;;
    --dspa-k8s-namespace)
      shift
      if [[ -n "$1" ]]; then
        DSPA_K8S_NAMESPACE="$1"
        shift
      else
        echo "Error: --dspa-k8s-namespace requires a value"
        exit 1
      fi
      ;;
    --dspa-path)
      shift
      if [[ -n "$1" ]]; then
        DSPA_PATH="$1"
        shift
      else
        echo "Error: --dspa-path requires a value"
        exit 1
      fi
      ;;
    --external-dspa-path)
      shift
      if [[ -n "$1" ]]; then
        DSPA_EXTERNAL_PATH="$1"
        shift
      else
        echo "Error: --external-dspa-path requires a value"
        exit 1
      fi
      ;;
    --dspa-k8s-path)
      shift
      if [[ -n "$1" ]]; then
        DSPA_K8S_PATH="$1"
        shift
      else
        echo "Error: --dspa-k8s-path requires a value"
        exit 1
      fi
      ;;
    --deploy-external-argo)
      DEPLOY_EXTERNAL_ARGO=true
      AWF_MANAGEMENT_STATE=Removed
      shift
      ;;
    --external-argo-version)
      shift
      if [[ -n "$1" ]]; then
	      ARGO_VERSION="$1"
	      shift
      else
	      echo "Error: --external-argo-version requires a value (in form of vX.Y.Z)"
	      exit 1
      fi
      ;;
    --kube-config)
      shift
      if [[ -n "$1" ]]; then
        KUBECONFIGPATH="$1"
        shift
      else
        echo "Error: --kube-config requires a value"
        exit 1
      fi
      ;;
    --endpoint-type)
      shift
      if [[ -n "$1" ]]; then
        ENDPOINT_TYPE="$1"
        shift
      else
        echo "Error: --endpoint-type requires a value [service, route]"
        exit 1
      fi
      ;;
    *)
      echo "Unknown command line switch: $1"
      exit 1
      ;;
  esac
done

if [ "$K8SAPISERVERHOST" = "" ]; then
  echo "K8SAPISERVERHOST is empty. It will use suite_test.go::Defaultk8sApiServerHost"
  echo "If the TARGET is OpenShift or RHOAI. You can use: oc whoami --show-server"
fi

if [ "$SKIP_DEPLOY" = true ]; then
  echo "Skipping deployment"
else
  if [ "$TARGET" = "kind" ]; then
    if [ "$CLEAN_INFRA" = true ] ; then
        undeploy_kind_resources
    fi
    setup_kind_requirements
  elif [ "$TARGET" = "openshift-ci" ]; then
    setup_openshift_ci_requirements
  elif [ "$TARGET" = "rhoai" ]; then
    if [ "$CLEAN_INFRA" = true ] ; then
        remove_namespace_created_for_rhoai
    fi
    setup_rhoai_requirements
  fi

  # Update to remove on-board Argo Workflow Controllers for BYOArgo test cases
  if [ "$DEPLOY_EXTERNAL_ARGO" = true ]; then
    setup_external_argo
  fi
fi

run_tests
run_tests_dspa_external_connections
run_tests_dspa_k8s
