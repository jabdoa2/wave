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
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Handler performs the main business logic of the Wave controller
type Handler struct {
	client.Client
	recorder          record.EventRecorder
	watchedConfigmaps map[types.NamespacedName]map[types.NamespacedName]bool
	watchedSecrets    map[types.NamespacedName]map[types.NamespacedName]bool
}

// NewHandler constructs a new instance of Handler
func NewHandler(c client.Client, r record.EventRecorder) *Handler {
	return &Handler{Client: c, recorder: r, watchedConfigmaps: make(map[types.NamespacedName]map[types.NamespacedName]bool), watchedSecrets: make(map[types.NamespacedName]map[types.NamespacedName]bool)}
}

// HandleDeployment is called by the deployment controller to reconcile deployments
func (h *Handler) HandleDeployment(instance *appsv1.Deployment) (reconcile.Result, error) {
	return h.handlePodController(&deployment{Deployment: instance})
}

// HandleStatefulSet is called by the StatefulSet controller to reconcile StatefulSets
func (h *Handler) HandleStatefulSet(instance *appsv1.StatefulSet) (reconcile.Result, error) {
	return h.handlePodController(&statefulset{StatefulSet: instance})
}

// HandleDaemonSet is called by the DaemonSet controller to reconcile DaemonSets
func (h *Handler) HandleDaemonSet(instance *appsv1.DaemonSet) (reconcile.Result, error) {
	return h.handlePodController(&daemonset{DaemonSet: instance})
}

// handlePodController reconciles the state of a podController
func (h *Handler) handlePodController(instance podController) (reconcile.Result, error) {
	log := logf.Log.WithName("wave").WithValues("namespace", instance.GetNamespace(), "name", instance.GetName())

	// To cleanup legacy ownerReferences and finalizer
	if hasFinalizer(instance) {
		log.V(0).Info("Removing old finalizer")
		return h.deleteOwnerReferencesAndFinalizer(instance)
	}

	// If the required annotation isn't present, ignore the instance
	if !hasRequiredAnnotation(instance) {
		h.removeWatchesForInstance(instance)
		return reconcile.Result{}, nil
	}

	// Get all children that the instance currently references
	current, err := h.getCurrentChildren(instance)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("error fetching current children: %v", err)
	}

	h.removeWatchesForInstance(instance)
	h.watchChildrenForInstance(instance, current)

	hash, err := calculateConfigHash(current)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("error calculating configuration hash: %v", err)
	}

	// Update the desired state of the Deployment in a DeepCopy
	copy := instance.DeepCopyPodController()
	setConfigHash(copy, hash)

	// If the desired state doesn't match the existing state, update it
	if !reflect.DeepEqual(instance, copy) {
		log.V(0).Info("Updating instance hash", "hash", hash)
		h.recorder.Eventf(copy.GetApiObject(), corev1.EventTypeNormal, "ConfigChanged", "Configuration hash updated to %s", hash)

		err := h.Update(context.TODO(), copy.GetApiObject())
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("error updating instance %s/%s: %v", instance.GetNamespace(), instance.GetName(), err)
		}
	}
	return reconcile.Result{}, nil
}
