#!/bin/bash
# Build DSP and Argo component images for KinD integration tests (RHOAIENG-82788).
# Images are pushed to the local KinD registry (REGISTRY_ADDRESS).

set -euo pipefail

KIND_IMAGE_TAG="${KIND_IMAGE_TAG:-kind-ci}"
CONTAINER_CLI="${CONTAINER_CLI:-podman}"
DSP_SOURCE_DIR="${DSP_SOURCE_DIR:-}"
ARGO_SOURCE_DIR="${ARGO_SOURCE_DIR:-}"
BUILD_ARGO_IMAGES="${BUILD_ARGO_IMAGES:-true}"

build_and_push_image() {
  local name="$1"
  local dockerfile="$2"
  local context="$3"
  local target="${4:-}"
  local image="${REGISTRY_ADDRESS}/${name}:${KIND_IMAGE_TAG}"
  local target_args=()

  if [ -n "$target" ]; then
    target_args=(--target "$target")
  fi

  echo "---------------------------------"
  echo "Building ${image}"
  echo "  dockerfile: ${dockerfile}"
  echo "  context:    ${context}"
  echo "---------------------------------"
  ${CONTAINER_CLI} build "${target_args[@]}" -f "${dockerfile}" -t "${image}" "${context}"
  ${CONTAINER_CLI} push --tls-verify=false "${image}"
}

build_dsp_kind_images() {
  if [ -z "$DSP_SOURCE_DIR" ] || [ ! -d "$DSP_SOURCE_DIR" ]; then
    echo "DSP_SOURCE_DIR is not set or does not exist: ${DSP_SOURCE_DIR:-<unset>}"
    exit 1
  fi

  echo "Building DSP component images from ${DSP_SOURCE_DIR}"
  build_and_push_image "apiserver" "${DSP_SOURCE_DIR}/backend/Dockerfile" "${DSP_SOURCE_DIR}"
  build_and_push_image "persistenceagent" "${DSP_SOURCE_DIR}/backend/Dockerfile.persistenceagent" "${DSP_SOURCE_DIR}"
  build_and_push_image "scheduledworkflow" "${DSP_SOURCE_DIR}/backend/Dockerfile.scheduledworkflow" "${DSP_SOURCE_DIR}"
  build_and_push_image "launcher" "${DSP_SOURCE_DIR}/backend/Dockerfile.launcher" "${DSP_SOURCE_DIR}"
  build_and_push_image "driver" "${DSP_SOURCE_DIR}/backend/Dockerfile.driver" "${DSP_SOURCE_DIR}"
}

build_argo_kind_images() {
  if [ "$BUILD_ARGO_IMAGES" != "true" ]; then
    echo "Skipping Argo image builds (BUILD_ARGO_IMAGES=${BUILD_ARGO_IMAGES})"
    return 0
  fi

  if [ -z "$ARGO_SOURCE_DIR" ] || [ ! -d "$ARGO_SOURCE_DIR" ]; then
    echo "ARGO_SOURCE_DIR is not set or does not exist: ${ARGO_SOURCE_DIR:-<unset>}"
    exit 1
  fi

  echo "Building Argo component images from ${ARGO_SOURCE_DIR}"
  build_and_push_image "workflow-controller" "${ARGO_SOURCE_DIR}/Dockerfile" "${ARGO_SOURCE_DIR}" "workflow-controller"
  build_and_push_image "argoexec" "${ARGO_SOURCE_DIR}/Dockerfile" "${ARGO_SOURCE_DIR}" "argoexec"
}

build_kind_component_images() {
  if [ -z "$REGISTRY_ADDRESS" ]; then
    echo "REGISTRY_ADDRESS must be set for KinD component image builds"
    exit 1
  fi

  build_dsp_kind_images
  build_argo_kind_images
}
