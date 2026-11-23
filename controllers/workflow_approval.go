package controllers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const workflowApprovalAnnotation = "pipelines.opendatahub.io/service-account-approval"
const workflowApprovalKeyName = "dspo-workflow-approval"
const workflowApprovalAPIRequest = "pipelines.opendatahub.io/authorize-scheduled-workflow"

type workflowApproval struct {
	Namespace string `json:"namespace"`
	Resource  string `json:"resource"`
	Name      string `json:"name,omitempty"`
	UID       string `json:"uid,omitempty"`
	Account   string `json:"account"`
	SpecHash  string `json:"specHash,omitempty"`
}

func EnsureWorkflowApprovalKey(ctx context.Context, reader client.Reader, writer client.Writer, namespace string) (*ecdsa.PrivateKey, error) {
	if namespace == "" {
		return nil, fmt.Errorf("workflow approval key namespace must be configured")
	}
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: namespace, Name: workflowApprovalKeyName}
	if err := reader.Get(ctx, key, secret); apierrors.IsNotFound(err) {
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		encoded, err := x509.MarshalECPrivateKey(private)
		if err != nil {
			return nil, err
		}
		immutable := true
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Immutable:  &immutable,
			Data:       map[string][]byte{"signing-key": encoded},
		}
		if err := writer.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		if err := reader.Get(ctx, key, secret); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	keyData, err := x509.ParseECPrivateKey(secret.Data["signing-key"])
	if err != nil || keyData.Curve != elliptic.P256() {
		return nil, fmt.Errorf("workflow approval key %s must be a P-256 key", key)
	}
	return keyData, nil
}

func EncodeWorkflowApprovalPublicKey(key *ecdsa.PrivateKey) (string, error) {
	publicKey, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(publicKey), nil
}

func (claim workflowApproval) sign(key *ecdsa.PrivateKey) (string, error) {
	if key == nil {
		return "", fmt.Errorf("workflow approval signing key unavailable")
	}
	message, err := json.Marshal(claim)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(message)
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(message) + "." +
		base64.RawURLEncoding.EncodeToString(signature), nil
}

func verifyWorkflowApproval(key *ecdsa.PrivateKey, token string, expected workflowApproval) bool {
	if key == nil {
		return false
	}
	body, signature, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	message, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	digest := sha256.Sum256(message)
	if err != nil || !ecdsa.VerifyASN1(&key.PublicKey, digest[:], sig) {
		return false
	}
	var actual workflowApproval
	if err := json.Unmarshal(message, &actual); err != nil {
		return false
	}
	if actual.UID == "" {
		actual.UID = expected.UID
	}
	return actual == expected
}

func approvalSpecHash(spec json.RawMessage, resource string) (string, error) {
	decoded, err := decodeWorkflowSpec(spec)
	if err != nil {
		return "", err
	}
	switch resource {
	case cronWorkflowResource:
		workflow, _ := decoded["workflowSpec"].(map[string]any)
		normalizeArgoWorkflowSpec(workflow)
		decoded = workflow
	case scheduledWorkflowResource:
		decoded = map[string]any{
			"workflow": decoded["workflow"], "serviceAccount": decoded["serviceAccount"],
			"pipelineId": decoded["pipelineId"], "pipelineVersionId": decoded["pipelineVersionId"],
		}
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}
