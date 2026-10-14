/*
Copyright 2026 The Appshell Authors.

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

package controller

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	registryv1alpha1 "github.com/appshellhq/appshell-controller/api/v1alpha1"
)

// ReasonAwaitingApply is the Ready reason until the controller applies to the registry.
const ReasonAwaitingApply = "AwaitingApply"

// conflictRetry is how soon a status write that lost a race is retried: expected, not an error.
const conflictRetry = time.Second

// AppshellApplicationReconciler keeps each AppshellApplication's registry application at the
// state its spec declares.
type AppshellApplicationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Its own resources and nothing else: it reads them, records status, and holds a finalizer
// while a Delete policy deactivates. It never creates, updates or deletes an AppshellApplication,
// which is its users' to do.
// +kubebuilder:rbac:groups=registry.appshell.org,resources=appshellapplications,verbs=get;list;watch
// +kubebuilder:rbac:groups=registry.appshell.org,resources=appshellapplications/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=registry.appshell.org,resources=appshellapplications/finalizers,verbs=update

// Reconcile brings one AppshellApplication's registry application to its spec.
//
// The skeleton records that it saw the generation and has yet to apply it; applying through
// the registry's API, the deletion policy and the status that follows are the next step
// (appshellhq/appshell#25).
func (r *AppshellApplicationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var app registryv1alpha1.AppshellApplication
	if err := r.Get(ctx, req.NamespacedName, &app); err != nil {
		// Deleted since it was queued: nothing left to reconcile.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	before := app.DeepCopy()
	app.Status.ObservedGeneration = app.Generation
	meta.SetStatusCondition(&app.Status.Conditions, metav1.Condition{
		Type:               registryv1alpha1.ConditionReady,
		Status:             metav1.ConditionUnknown,
		Reason:             ReasonAwaitingApply,
		Message:            "Not yet applied to the registry.",
		ObservedGeneration: app.Generation,
	})

	// Optimistically locked to the version read: a merge patch replaces lists like conditions
	// whole, so without the lock a concurrent status write would be silently overwritten. With
	// it, a newer version is a conflict, and the request is retried against what's there now.
	patch := client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})
	if err := r.Status().Patch(ctx, &app, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		if apierrors.IsConflict(err) {
			log.V(1).Info("Status changed since it was read; retrying", "application", app.Spec.Name)

			return ctrl.Result{RequeueAfter: conflictRetry}, nil
		}

		return ctrl.Result{}, err
	}

	log.V(1).Info("Observed", "application", app.Spec.Name, "generation", app.Generation)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AppshellApplicationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.AppshellApplication{}).
		Named("appshellapplication").
		Complete(r)
}
