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
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	registryv1alpha1 "github.com/appshellhq/appshell-controller/api/v1alpha1"
	"github.com/appshellhq/appshell-controller/internal/registry"
)

const (
	// Finalizer is held while deletionPolicy is Delete, so the application's packages are
	// deactivated before the resource goes. Never held under Orphan, so deleting an orphaned
	// resource can't wait on the registry.
	Finalizer = "registry.appshell.org/deactivate"

	// ReasonApplied: the registry application matches the spec.
	ReasonApplied = "Applied"
	// ReasonRegistryUnavailable: the apply failed in a way worth retrying soon.
	ReasonRegistryUnavailable = "RegistryUnavailable"
	// ReasonDeactivationFailed: a Delete policy couldn't deactivate, so the resource is held.
	ReasonDeactivationFailed = "DeactivationFailed"

	// refusedRetry is how soon a refused apply is tried again. A refusal changes when a person
	// does something — adds a trust record, publishes a package — so this is a poll, not a
	// backoff, and cheap: one request.
	refusedRetry = time.Minute

	// conflictRetry is how soon a write that lost a race is retried: expected, not an error.
	conflictRetry = time.Second
)

// Applier applies a resource to the registry. *registry.Client is the real one.
type Applier interface {
	Apply(ctx context.Context, r registry.Resource) (registry.Result, error)
}

// AppshellApplicationReconciler keeps each AppshellApplication's registry application at the
// state its spec declares.
type AppshellApplicationReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Registry Applier
	Recorder events.EventRecorder
	// Interval is how often an applied resource is applied again, which is what corrects drift:
	// a package activated by hand is reverted on the next one.
	Interval time.Duration
}

// Its own resources: it reads them, writes their status, and adds or removes its finalizer
// (a patch). It never creates or deletes one; that is its users' to do. Events, to say what
// it did.
// +kubebuilder:rbac:groups=registry.appshell.org,resources=appshellapplications,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=registry.appshell.org,resources=appshellapplications/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=registry.appshell.org,resources=appshellapplications/finalizers,verbs=update
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile brings one AppshellApplication's registry application to its spec.
func (r *AppshellApplicationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var app registryv1alpha1.AppshellApplication
	if err := r.Get(ctx, req.NamespacedName, &app); err != nil {
		// Deleted since it was queued: nothing left to reconcile.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !app.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &app)
	}

	if err := r.holdFinalizerFor(ctx, &app); err != nil {
		return retryOnConflict(err)
	}

	result, err := r.Registry.Apply(ctx, ResourceOf(&app))
	if err != nil {
		return r.failed(ctx, &app, err)
	}

	return r.applied(ctx, &app, result)
}

// holdFinalizerFor holds the finalizer exactly while the policy is Delete: added when it becomes
// Delete, and released when it becomes Orphan, so switching policy is all it takes.
func (r *AppshellApplicationReconciler) holdFinalizerFor(ctx context.Context, app *registryv1alpha1.AppshellApplication) error {
	want := app.Spec.DeletionPolicy == registryv1alpha1.DeletionPolicyDelete
	if want == controllerutil.ContainsFinalizer(app, Finalizer) {
		return nil
	}

	before := app.DeepCopy()
	if want {
		controllerutil.AddFinalizer(app, Finalizer)
	} else {
		controllerutil.RemoveFinalizer(app, Finalizer)
	}

	return r.Patch(ctx, app, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}

// finalize deactivates the application's packages when the policy is Delete, then lets the
// resource go.
//
// Deactivation is an apply of an empty package set and nothing else, so the application's
// settings are left as they are. If it can't succeed — the trust record is gone, say — the
// resource is held, with a condition saying how to let it go without deactivating: switch the
// policy to Orphan. A delete never silently skips what the policy promised.
//
// The finalizer is added when the controller first reconciles the resource. One deleted before
// that holds none, and goes: the controller never applied it, so there is nothing of its to
// deactivate. Adding the finalizer at creation instead would take a mutating webhook, and its
// certificates, for a resource that never reached the registry.
func (r *AppshellApplicationReconciler) finalize(ctx context.Context, app *registryv1alpha1.AppshellApplication) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(app, Finalizer) {
		return ctrl.Result{}, nil
	}

	if app.Spec.DeletionPolicy == registryv1alpha1.DeletionPolicyDelete {
		none := []string{}
		deactivate := registry.Resource{
			APIVersion: registry.APIVersion,
			Kind:       registry.Kind,
			Name:       app.Spec.Name,
			Spec:       registry.Spec{Packages: &none},
		}

		result, err := r.Registry.Apply(ctx, deactivate)
		if err != nil {
			before := app.DeepCopy()
			wasReason := readyReason(app)
			r.setReady(app, metav1.ConditionFalse, ReasonDeactivationFailed, fmt.Sprintf(
				"Deactivating %s before deletion failed: %v. Set deletionPolicy: Orphan to delete "+
					"this resource and leave the application as it is.", app.Spec.Name, err))
			if patchErr := r.patchStatus(ctx, app, before); patchErr != nil {
				return retryOnConflict(patchErr)
			}
			// Once, on entering the state, not on every poll while the resource is held.
			if wasReason != ReasonDeactivationFailed {
				r.event(app, corev1.EventTypeWarning, ReasonDeactivationFailed, "Deactivate", err.Error())
			}

			return requeueFor(err)
		}

		// Only when something moved: a retry after a lost race finds nothing left to deactivate.
		if moved(result) {
			r.event(app, corev1.EventTypeNormal, "Deactivated", "Deactivate",
				fmt.Sprintf("Deactivated %s: %s", app.Spec.Name, strings.Join(result.Changes, ", ")))
		}
	}

	before := app.DeepCopy()
	controllerutil.RemoveFinalizer(app, Finalizer)
	if err := r.Patch(ctx, app, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		return retryOnConflict(err)
	}

	return ctrl.Result{}, nil
}

// applied records a successful apply, and says what moved when anything did.
func (r *AppshellApplicationReconciler) applied(
	ctx context.Context, app *registryv1alpha1.AppshellApplication, result registry.Result,
) (ctrl.Result, error) {
	before := app.DeepCopy()
	now := metav1.Now()
	app.Status.AppliedPackages = slices.Clone(app.Spec.Packages)
	app.Status.LastAppliedTime = &now
	r.setReady(app, metav1.ConditionTrue, ReasonApplied, fmt.Sprintf("%s matches this resource.", app.Spec.Name))

	if err := r.patchStatus(ctx, app, before); err != nil {
		return retryOnConflict(err)
	}

	// Only when something moved: an unchanged apply on every interval would be an Event each time.
	if moved(result) {
		r.event(app, corev1.EventTypeNormal, ReasonApplied, "Apply",
			fmt.Sprintf("Applied %s: %s", app.Spec.Name, strings.Join(result.Changes, ", ")))
	}

	logf.FromContext(ctx).V(1).Info("Applied", "application", app.Spec.Name, "changes", result.Changes)

	return ctrl.Result{RequeueAfter: r.Interval}, nil
}

// failed records a failed apply. A refusal is polled again in a minute; anything else is
// returned, so it's retried with backoff.
func (r *AppshellApplicationReconciler) failed(
	ctx context.Context, app *registryv1alpha1.AppshellApplication, err error,
) (ctrl.Result, error) {
	reason := ReasonRegistryUnavailable
	if refusal, refused := registry.Refused(err); refused {
		reason = string(refusal)
	}

	before := app.DeepCopy()
	wasReason := readyReason(app)
	r.setReady(app, metav1.ConditionFalse, reason, err.Error())

	if patchErr := r.patchStatus(ctx, app, before); patchErr != nil {
		return retryOnConflict(patchErr)
	}

	// Once per change of reason, not once per retry.
	if wasReason != reason {
		r.event(app, corev1.EventTypeWarning, reason, "Apply", err.Error())
	}

	return requeueFor(err)
}

// moved reports whether an apply changed anything. A registry that predates `changed` doesn't send
// it, so its changes alone decide.
func moved(result registry.Result) bool {
	return (result.Changed == nil || *result.Changed) && len(result.Changes) > 0
}

// readyReason is the Ready condition's current reason, or "" before there is one.
func readyReason(app *registryv1alpha1.AppshellApplication) string {
	if ready := meta.FindStatusCondition(app.Status.Conditions, registryv1alpha1.ConditionReady); ready != nil {
		return ready.Reason
	}

	return ""
}

func (r *AppshellApplicationReconciler) setReady(
	app *registryv1alpha1.AppshellApplication, status metav1.ConditionStatus, reason, message string,
) {
	app.Status.ObservedGeneration = app.Generation
	meta.SetStatusCondition(&app.Status.Conditions, metav1.Condition{
		Type:               registryv1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: app.Generation,
	})
}

// patchStatus writes the status, optimistically locked to the version read: a merge patch
// replaces lists like conditions whole, so without the lock a concurrent write would be lost.
func (r *AppshellApplicationReconciler) patchStatus(
	ctx context.Context, app, before *registryv1alpha1.AppshellApplication,
) error {
	err := r.Status().Patch(ctx, app, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))

	return client.IgnoreNotFound(err)
}

func (r *AppshellApplicationReconciler) event(app *registryv1alpha1.AppshellApplication, kind, reason, action, note string) {
	if r.Recorder != nil {
		r.Recorder.Eventf(app, nil, kind, reason, action, "%s", note)
	}
}

// requeueFor polls a refusal and backs off anything else.
func requeueFor(err error) (ctrl.Result, error) {
	if _, refused := registry.Refused(err); refused {
		return ctrl.Result{RequeueAfter: refusedRetry}, nil
	}

	return ctrl.Result{}, err
}

// retryOnConflict retries a write that lost a race shortly, without reporting it as an error.
func retryOnConflict(err error) (ctrl.Result, error) {
	if apierrors.IsConflict(err) {
		return ctrl.Result{RequeueAfter: conflictRetry}, nil
	}

	return ctrl.Result{}, err
}

// ResourceOf is the registry's apply resource for an AppshellApplication: the same desired
// state, in the registry's own words. Fields the spec leaves out are left out, so the registry
// leaves them as they are; packages left out, or empty, leave the application's packages alone.
func ResourceOf(app *registryv1alpha1.AppshellApplication) registry.Resource {
	s := app.Spec
	spec := registry.Spec{
		AllowOverrides:        s.AllowOverrides,
		AllowOverlays:         s.AllowOverlays,
		ServingBaseURL:        s.ServingBaseURL,
		MountPath:             s.MountPath,
		SharedDepsEnforcement: (*string)(s.SharedDepsEnforcement),
		TokenEnforcement:      (*string)(s.TokenEnforcement),
		VarsEnforcement:       (*string)(s.VarsEnforcement),
	}
	if s.Shell != nil {
		// A typed struct of strings, maps and raw JSON: it always encodes.
		spec.Shell, _ = json.Marshal(s.Shell)
	}
	if s.Overrides != nil {
		spec.Overrides = s.Overrides.Raw
	}
	if s.SharedBaselines != nil {
		spec.SharedBaselines = s.SharedBaselines.Raw
	}
	if len(s.Packages) > 0 {
		packages := slices.Clone(s.Packages)
		spec.Packages = &packages
	}

	return registry.Resource{APIVersion: registry.APIVersion, Kind: registry.Kind, Name: s.Name, Spec: spec}
}

// deletionStarted admits the update that marks a resource for deletion, so a Delete policy's
// finalizer is handled as soon as the resource is deleted.
var deletionStarted = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		return e.ObjectOld.GetDeletionTimestamp().IsZero() && !e.ObjectNew.GetDeletionTimestamp().IsZero()
	},
	CreateFunc:  func(event.CreateEvent) bool { return false },
	DeleteFunc:  func(event.DeleteEvent) bool { return false },
	GenericFunc: func(event.GenericEvent) bool { return false },
}

// SetupWithManager sets up the controller with the Manager.
//
// It reacts to a change of spec (a new generation) and to deletion starting, and to nothing else
// about its resources: not to its own status writes, which would otherwise trigger another apply,
// whose status write would trigger another, applying continuously instead of on the interval.
// The interval itself is a requeue, and a restart lists every resource afresh.
func (r *AppshellApplicationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.AppshellApplication{},
			builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, deletionStarted))).
		Named("appshellapplication").
		Complete(r)
}
