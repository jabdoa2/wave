/*
Copyright 2018 Pusher Ltd. and Wave Contributors

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

package core

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// configMetadata contains information about ConfigMaps/Secrets referenced
// within PodTemplates
//
// maps of configMetadata are return from the getChildNamesByType method
// configMetadata is also used to pass info through the getObject methods
type configMetadata struct {
	required bool
	allKeys  bool
	keys     map[string]struct{}
}

type configMetadataMap map[types.NamespacedName]configMetadata

type NotFoundError struct {
	string
}

func (e *NotFoundError) Error() string {
	return e.string
}

// getResult is returned from the getObject method as a helper struct to be
// passed into a channel
type getResult struct {
	err      error
	notFound bool
	obj      Object
	metadata configMetadata
}

// getCurrentChildren returns a list of all Secrets and ConfigMaps that are
// referenced in the Deployment's spec.  Any reference to a whole ConfigMap or Secret
// (i.e. via an EnvFrom or a Volume) will result in one entry in the list, irrespective of
// whether individual elements are also references (i.e. via an Env entry).
func (h *Handler) getCurrentChildren(configMaps configMetadataMap, secrets configMetadataMap) ([]configObject, error) {
	// get all of ConfigMaps and Secrets
	resultsChan := make(chan getResult)
	for name, metadata := range configMaps {
		go func(name types.NamespacedName, metadata configMetadata) {
			resultsChan <- h.getConfigMap(name, metadata)
		}(name, metadata)
	}
	for name, metadata := range secrets {
		go func(name types.NamespacedName, metadata configMetadata) {
			resultsChan <- h.getSecret(name, metadata)
		}(name, metadata)
	}

	// Range over and collect results from the gets
	var errs []string
	var notFoundErrs []string
	var children []configObject
	for i := 0; i < len(configMaps)+len(secrets); i++ {
		result := <-resultsChan
		if result.err != nil {
			if result.notFound {
				notFoundErrs = append(notFoundErrs, result.err.Error())
			} else {
				errs = append(errs, result.err.Error())
			}
		}
		if result.obj != nil {
			children = append(children, configObject{
				object:   result.obj,
				required: result.metadata.required,
				allKeys:  result.metadata.allKeys,
				keys:     result.metadata.keys,
			})
		}
	}

	// If there were any errors, don't return any children
	if len(errs) > 0 {
		return []configObject{}, fmt.Errorf("error(s) encountered when geting children: %s", strings.Join(errs, ", "))
	}

	// If there we did not find required elements
	if len(notFoundErrs) > 0 {
		return []configObject{}, &NotFoundError{fmt.Sprintf("required children not found: %s", strings.Join(notFoundErrs, ", "))}
	}

	// No errors, return the list of children
	return children, nil
}

// getChildNamesByType parses the Deployment object and returns two maps,
// the first containing ConfigMap metadata for all referenced ConfigMaps, keyed on the name of the ConfigMap,
// the second containing Secret metadata for all referenced Secrets, keyed on the name of the Secrets
func getChildNamesByType(obj podController) (configMetadataMap, configMetadataMap) {
	// Create sets for storing the names fo the ConfigMaps/Secrets
	configMaps := make(configMetadataMap)
	secrets := make(configMetadataMap)

	// Range through all Volumes and check the VolumeSources for ConfigMaps
	// and Secrets
	for _, vol := range obj.GetPodTemplate().Spec.Volumes {
		if cm := vol.VolumeSource.ConfigMap; cm != nil {
			configMaps[GetNamespacedName(cm.Name, obj.GetNamespace())] = configMetadata{required: isRequired(cm.Optional), allKeys: true}
		}
		if s := vol.VolumeSource.Secret; s != nil {
			secrets[GetNamespacedName(s.SecretName, obj.GetNamespace())] = configMetadata{required: isRequired(s.Optional), allKeys: true}
		}

		if projection := vol.VolumeSource.Projected; projection != nil {
			for _, source := range projection.Sources {
				if cm := source.ConfigMap; cm != nil {
					if cm.Items == nil {
						configMaps[GetNamespacedName(cm.Name, obj.GetNamespace())] = configMetadata{required: isRequired(cm.Optional), allKeys: true}
					} else {
						keys := make(map[string]struct{})
						for _, item := range cm.Items {
							keys[item.Key] = struct{}{}
						}
						configMaps[GetNamespacedName(cm.Name, obj.GetNamespace())] = configMetadata{required: isRequired(cm.Optional), allKeys: false, keys: keys}
					}
				}
				if s := source.Secret; s != nil {
					if s.Items == nil {
						secrets[GetNamespacedName(s.Name, obj.GetNamespace())] = configMetadata{required: isRequired(s.Optional), allKeys: true}
					} else {
						keys := make(map[string]struct{})
						for _, item := range s.Items {
							keys[item.Key] = struct{}{}
						}
						secrets[GetNamespacedName(s.Name, obj.GetNamespace())] = configMetadata{required: isRequired(s.Optional), allKeys: false, keys: keys}
					}
				}
			}
		}

		// Parse deployment annotations for cms/secrets used inside the pod
		if annotations := obj.GetAnnotations(); annotations != nil {
			if configMapString, ok := annotations[ExtraConfigMapsAnnotation]; ok {
				for _, cm := range strings.Split(configMapString, ",") {
					parts := strings.Split(cm, "/")
					if len(parts) == 1 {
						configMaps[GetNamespacedName(parts[0], obj.GetNamespace())] = configMetadata{required: false, allKeys: true}
					} else if len(parts) == 2 {
						configMaps[GetNamespacedName(parts[1], parts[0])] = configMetadata{required: false, allKeys: true}
					}
				}
			}
			if secretString, ok := annotations[ExtraSecretsAnnotation]; ok {
				for _, secret := range strings.Split(secretString, ",") {
					parts := strings.Split(secret, "/")
					if len(parts) == 1 {
						secrets[GetNamespacedName(parts[0], obj.GetNamespace())] = configMetadata{required: false, allKeys: true}
					} else if len(parts) == 2 {
						secrets[GetNamespacedName(parts[1], parts[0])] = configMetadata{required: false, allKeys: true}
					}
				}
			}
		}
	}

	// Range through all Containers and their respective EnvFrom,
	// then check the EnvFromSources for ConfigMaps and Secrets
	for _, container := range obj.GetPodTemplate().Spec.Containers {
		for _, env := range container.EnvFrom {
			if cm := env.ConfigMapRef; cm != nil {
				configMaps[GetNamespacedName(cm.Name, obj.GetNamespace())] = configMetadata{required: isRequired(cm.Optional), allKeys: true}
			}
			if s := env.SecretRef; s != nil {
				secrets[GetNamespacedName(s.Name, obj.GetNamespace())] = configMetadata{required: isRequired(s.Optional), allKeys: true}
			}
		}
	}

	// Range through all Containers and their respective Env
	for _, container := range obj.GetPodTemplate().Spec.Containers {
		for _, env := range container.Env {
			if valFrom := env.ValueFrom; valFrom != nil {
				if cm := valFrom.ConfigMapKeyRef; cm != nil {
					configMaps[GetNamespacedName(cm.Name, obj.GetNamespace())] = parseConfigMapKeyRef(configMaps[GetNamespacedName(cm.Name, obj.GetNamespace())], cm)
				}
				if s := valFrom.SecretKeyRef; s != nil {
					secrets[GetNamespacedName(s.Name, obj.GetNamespace())] = parseSecretKeyRef(secrets[GetNamespacedName(s.Name, obj.GetNamespace())], s)
				}
			}
		}
	}

	return configMaps, secrets
}

func isRequired(b *bool) bool {
	return b == nil || !*b
}

// parseConfigMapKeyRef updates the metadata for a ConfigMap to include the keys specified in this ConfigMapKeySelector
func parseConfigMapKeyRef(metadata configMetadata, cm *corev1.ConfigMapKeySelector) configMetadata {
	if !metadata.allKeys {
		if metadata.keys == nil {
			metadata.keys = make(map[string]struct{})
		}
		if cm.Optional == nil || !*cm.Optional {
			metadata.required = true
		}
		metadata.keys[cm.Key] = struct{}{}
	}
	return metadata
}

// parseSecretKeyRef updates the metadata for a Secret to include the keys specified in this SecretKeySelector
func parseSecretKeyRef(metadata configMetadata, s *corev1.SecretKeySelector) configMetadata {
	if !metadata.allKeys {
		if metadata.keys == nil {
			metadata.keys = make(map[string]struct{})
		}
		if s.Optional == nil || !*s.Optional {
			metadata.required = true
		}
		metadata.keys[s.Key] = struct{}{}
	}
	return metadata
}

// getConfigMap gets a ConfigMap with the given name and namespace from the
// API server.
func (h *Handler) getConfigMap(name types.NamespacedName, metadata configMetadata) getResult {
	return h.getObject(name, metadata, &corev1.ConfigMap{})
}

// getSecret gets a Secret with the given name and namespace from the
// API server.
func (h *Handler) getSecret(name types.NamespacedName, metadata configMetadata) getResult {
	return h.getObject(name, metadata, &corev1.Secret{})
}

// getObject gets the Object with the given name and namespace from the API
// server
func (h *Handler) getObject(objectName types.NamespacedName, metadata configMetadata, obj Object) getResult {
	err := h.Get(context.TODO(), objectName, obj)
	if err != nil {
		if errors.IsNotFound(err) {
			if metadata.required {
				return getResult{err: err, notFound: true}
			}
			return getResult{metadata: metadata, notFound: true}
		} else {
			return getResult{err: err, notFound: false}
		}
	}
	return getResult{obj: obj, metadata: metadata, notFound: false}
}

// getExistingChildren returns a list of all Secrets and ConfigMaps that are
// owned by the Deployment instance
func (h *Handler) getExistingChildren(obj podController) ([]Object, error) {
	inNamespace := client.InNamespace(obj.GetNamespace())

	// List all ConfigMaps in the Deployment's namespace
	configMaps := &corev1.ConfigMapList{}
	err := h.List(context.TODO(), configMaps, inNamespace)
	if err != nil {
		return []Object{}, fmt.Errorf("error listing ConfigMaps: %v", err)
	}

	// List all Secrets in the Deployment's namespcae
	secrets := &corev1.SecretList{}
	err = h.List(context.TODO(), secrets, inNamespace)
	if err != nil {
		return []Object{}, fmt.Errorf("error listing Secrets: %v", err)
	}

	// Iterate over the ConfigMaps/Secrets and add the ones owned by the
	// Deployment to the output list children
	children := []Object{}
	for _, cm := range configMaps.Items {
		if isOwnedBy(&cm, obj) {
			children = append(children, cm.DeepCopy())
		}
	}
	for _, s := range secrets.Items {
		if isOwnedBy(&s, obj) {
			children = append(children, s.DeepCopy())
		}
	}

	return children, nil
}

// isOwnedBy returns true if the child has an owner reference that points to
// the owner object
func isOwnedBy(child, owner metav1.Object) bool {
	for _, ref := range child.GetOwnerReferences() {
		if ref.UID == owner.GetUID() {
			return true
		}
	}
	return false
}
