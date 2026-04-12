/*
Copyright 2025 Gluesys FlexA Inc.

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

package webhook

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	log "github.com/sirupsen/logrus"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	annotationKey = "flexa.io/lustre-qos"
	envVarName    = "LUSTRE_JOBID"
)

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

func hasEnv(envs []corev1.EnvVar, name string) bool {
	for _, e := range envs {
		if e.Name == name {
			return true
		}
	}
	return false
}

func createPatches(pod *corev1.Pod) ([]patchOperation, error) {
	jobid, ok := pod.Annotations[annotationKey]
	if !ok || jobid == "" {
		return nil, nil
	}

	var patches []patchOperation
	for i, c := range pod.Spec.Containers {
		if hasEnv(c.Env, envVarName) {
			continue
		}
		envVar := corev1.EnvVar{Name: envVarName, Value: jobid}
		if len(c.Env) == 0 {
			patches = append(patches, patchOperation{
				Op:    "add",
				Path:  fmt.Sprintf("/spec/containers/%d/env", i),
				Value: []corev1.EnvVar{envVar},
			})
		} else {
			patches = append(patches, patchOperation{
				Op:    "add",
				Path:  fmt.Sprintf("/spec/containers/%d/env/-", i),
				Value: envVar,
			})
		}
	}

	for i, c := range pod.Spec.InitContainers {
		if hasEnv(c.Env, envVarName) {
			continue
		}
		envVar := corev1.EnvVar{Name: envVarName, Value: jobid}
		if len(c.Env) == 0 {
			patches = append(patches, patchOperation{
				Op:    "add",
				Path:  fmt.Sprintf("/spec/initContainers/%d/env", i),
				Value: []corev1.EnvVar{envVar},
			})
		} else {
			patches = append(patches, patchOperation{
				Op:    "add",
				Path:  fmt.Sprintf("/spec/initContainers/%d/env/-", i),
				Value: envVar,
			})
		}
	}

	return patches, nil
}

func HandleMutate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Errorf("webhook: failed to read request body: %v", err)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var admissionReview admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &admissionReview); err != nil {
		log.Errorf("webhook: failed to unmarshal admission review: %v", err)
		http.Error(w, "failed to unmarshal", http.StatusBadRequest)
		return
	}

	req := admissionReview.Request
	if req == nil {
		log.Error("webhook: admission request is nil")
		http.Error(w, "empty request", http.StatusBadRequest)
		return
	}

	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		log.Errorf("webhook: failed to unmarshal pod: %v", err)
		sendResponse(w, req.UID, false, "failed to unmarshal pod", nil)
		return
	}

	patches, err := createPatches(&pod)
	if err != nil {
		log.Errorf("webhook: failed to create patches: %v", err)
		sendResponse(w, req.UID, false, err.Error(), nil)
		return
	}

	if len(patches) == 0 {
		log.Debugf("webhook: no patches needed for pod %s/%s", pod.Namespace, pod.Name)
		sendResponse(w, req.UID, true, "", nil)
		return
	}

	patchBytes, err := json.Marshal(patches)
	if err != nil {
		log.Errorf("webhook: failed to marshal patches: %v", err)
		sendResponse(w, req.UID, false, err.Error(), nil)
		return
	}

	log.Infof("webhook: injecting %s=%s into pod %s/%s (%d patches)",
		envVarName, pod.Annotations[annotationKey], pod.Namespace, pod.Name, len(patches))

	patchType := admissionv1.PatchTypeJSONPatch
	sendResponse(w, req.UID, true, "", &admissionv1.AdmissionResponse{
		UID:       req.UID,
		Allowed:   true,
		PatchType: &patchType,
		Patch:     patchBytes,
	})
}

func sendResponse(w http.ResponseWriter, uid types.UID, allowed bool, message string, resp *admissionv1.AdmissionResponse) {
	if resp == nil {
		resp = &admissionv1.AdmissionResponse{
			UID:     uid,
			Allowed: allowed,
		}
		if message != "" {
			resp.Result = &metav1.Status{Message: message}
		}
	}

	review := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admission.k8s.io/v1",
			Kind:       "AdmissionReview",
		},
		Response: resp,
	}

	respBytes, err := json.Marshal(review)
	if err != nil {
		log.Errorf("webhook: failed to marshal response: %v", err)
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(respBytes)
}

func StartWebhookServer(certFile, keyFile, listenAddr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/mutate", HandleMutate)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}

	log.Infof("webhook: starting server on %s", listenAddr)
	return server.ListenAndServeTLS(certFile, keyFile)
}
