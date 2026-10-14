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
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	registryv1alpha1 "github.com/appshellhq/appshell-controller/api/v1alpha1"
	"github.com/appshellhq/appshell-controller/internal/registry"
)

// fakeRegistry stands in for the registry: it records what it was asked to apply, and answers
// with result, or err.
type fakeRegistry struct {
	mu      sync.Mutex
	applied []registry.Resource
	result  registry.Result
	err     error
}

func (f *fakeRegistry) Apply(_ context.Context, r registry.Resource) (registry.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, r)

	return f.result, f.err
}

func (f *fakeRegistry) calls() []registry.Resource {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]registry.Resource(nil), f.applied...)
}

const (
	interval = 10 * time.Minute
	// catalog is the package the tests' resources activate.
	catalog = "acme/catalog@1.2.0"
)

var _ = Describe("Reconciling an AppshellApplication", func() {
	ctx := context.Background()
	const name = "reconciled"
	key := types.NamespacedName{Name: name, Namespace: namespace}

	var (
		fake       *fakeRegistry
		recorder   *events.FakeRecorder
		reconciler *AppshellApplicationReconciler
	)

	create := func(spec registryv1alpha1.AppshellApplicationSpec) {
		Expect(k8sClient.Create(ctx, application(name, spec))).To(Succeed())
	}
	reconcileOnce := func() (reconcile.Result, error) {
		return reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	}
	fetch := func() *registryv1alpha1.AppshellApplication {
		var app registryv1alpha1.AppshellApplication
		Expect(k8sClient.Get(ctx, key, &app)).To(Succeed())

		return &app
	}
	ready := func() *metav1.Condition {
		return meta.FindStatusCondition(fetch().Status.Conditions, registryv1alpha1.ConditionReady)
	}
	// What the recorder emitted, without waiting for more.
	emitted := func() []string {
		var out []string
		for {
			select {
			case e := <-recorder.Events:
				out = append(out, e)
			default:
				return out
			}
		}
	}

	BeforeEach(func() {
		fake = &fakeRegistry{result: registry.Result{ID: storefront, Changed: new(true),
			Changes: []string{"activated " + catalog}}}
		recorder = events.NewFakeRecorder(20)
		reconciler = &AppshellApplicationReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), Registry: fake, Recorder: recorder, Interval: interval,
		}
	})

	AfterEach(func() { removeAll(ctx) })

	Context("applying", func() {
		It("applies the spec, and records that the application matches it", func() {
			create(registryv1alpha1.AppshellApplicationSpec{Name: storefront, Packages: []string{catalog}})

			result, err := reconcileOnce()
			Expect(err).NotTo(HaveOccurred())
			// Applied again on the interval: that's what reverts a package activated by hand.
			Expect(result.RequeueAfter).To(Equal(interval))

			Expect(fake.calls()).To(HaveLen(1))
			sent := fake.calls()[0]
			Expect(sent.Name).To(Equal(storefront))
			Expect(*sent.Spec.Packages).To(Equal([]string{catalog}))

			app := fetch()
			Expect(app.Status.ObservedGeneration).To(Equal(app.Generation))
			Expect(app.Status.AppliedPackages).To(Equal([]string{catalog}))
			Expect(app.Status.LastAppliedTime).NotTo(BeNil())
			Expect(ready().Status).To(Equal(metav1.ConditionTrue))
			Expect(ready().Reason).To(Equal(ReasonApplied))

			Expect(emitted()).To(ConsistOf(ContainSubstring("activated acme/catalog@1.2.0")))
		})

		// An apply on every interval that changes nothing would otherwise be an Event every time.
		It("says nothing when nothing moved", func() {
			fake.result = registry.Result{ID: storefront, Changed: new(false)}
			create(registryv1alpha1.AppshellApplicationSpec{Name: storefront})

			_, err := reconcileOnce()
			Expect(err).NotTo(HaveOccurred())
			Expect(ready().Status).To(Equal(metav1.ConditionTrue))
			Expect(emitted()).To(BeEmpty())
		})

		// A refusal changes when a person acts, so it's polled, not backed off — and reported
		// once, not once per poll.
		It("records a refusal with its reason, polls it, and warns once", func() {
			fake.err = &registry.Error{Status: 403, Message: "No trusted deployer on acme/storefront",
				Refusal: registry.RefusalNotTrusted}
			create(registryv1alpha1.AppshellApplicationSpec{Name: storefront})

			result, err := reconcileOnce()
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(refusedRetry))
			Expect(ready().Status).To(Equal(metav1.ConditionFalse))
			Expect(ready().Reason).To(Equal(string(registry.RefusalNotTrusted)))
			Expect(ready().Message).To(ContainSubstring("No trusted deployer"))

			_, err = reconcileOnce()
			Expect(err).NotTo(HaveOccurred())
			Expect(emitted()).To(HaveLen(1))
		})

		It("backs off a failure worth retrying, and says the registry is unavailable", func() {
			fake.err = &registry.Error{Message: "reaching the registry: connection refused"}
			create(registryv1alpha1.AppshellApplicationSpec{Name: storefront})

			_, err := reconcileOnce()
			Expect(err).To(HaveOccurred())
			Expect(ready().Status).To(Equal(metav1.ConditionFalse))
			Expect(ready().Reason).To(Equal(ReasonRegistryUnavailable))
		})

		// Another writer got there first. Retried against what's there now, rather than written
		// over, and not reported as an error: a lost race is expected.
		It("retries a status write that lost a race, instead of overwriting it", func() {
			create(registryv1alpha1.AppshellApplicationSpec{Name: storefront})
			watching, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
			Expect(err).NotTo(HaveOccurred())
			reconciler.Client = interceptor.NewClient(watching, interceptor.Funcs{
				SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch,
					...client.SubResourcePatchOption) error {
					return apierrors.NewConflict(schema.GroupResource{
						Group: registryv1alpha1.GroupVersion.Group, Resource: "appshellapplications",
					}, name, nil)
				},
			})

			result, err := reconcileOnce()
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(conflictRetry))
		})

		It("does nothing for a resource that's gone", func() {
			_, err := reconcileOnce()
			Expect(err).NotTo(HaveOccurred())
			Expect(fake.calls()).To(BeEmpty())
		})
	})

	Context("deleting", func() {
		deleteIt := func() { Expect(k8sClient.Delete(ctx, fetch())).To(Succeed()) }
		gone := func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &registryv1alpha1.AppshellApplication{}))
		}

		// Orphan holds no finalizer, so deleting can't wait on the registry, and doesn't touch it.
		It("lets an orphaned resource go, leaving the application alone", func() {
			create(registryv1alpha1.AppshellApplicationSpec{Name: storefront})
			_, err := reconcileOnce()
			Expect(err).NotTo(HaveOccurred())
			Expect(fetch().Finalizers).To(BeEmpty())

			deleteIt()
			Expect(gone()).To(BeTrue())
			Expect(fake.calls()).To(HaveLen(1)) // the apply, and no deactivation
		})

		It("deactivates every package before letting a Delete resource go", func() {
			create(registryv1alpha1.AppshellApplicationSpec{
				Name:           storefront,
				Packages:       []string{catalog},
				AllowOverlays:  new(true),
				DeletionPolicy: registryv1alpha1.DeletionPolicyDelete,
			})
			_, err := reconcileOnce()
			Expect(err).NotTo(HaveOccurred())
			Expect(controllerutil.ContainsFinalizer(fetch(), Finalizer)).To(BeTrue())

			deleteIt()
			_, err = reconcileOnce()
			Expect(err).NotTo(HaveOccurred())
			Expect(gone()).To(BeTrue())

			// An empty package set and nothing else: the application's settings are left alone.
			deactivation := fake.calls()[len(fake.calls())-1]
			Expect(deactivation.Name).To(Equal(storefront))
			Expect(*deactivation.Spec.Packages).To(BeEmpty())
			Expect(deactivation.Spec.AllowOverlays).To(BeNil())
			Expect(deactivation.Spec.Shell).To(BeNil())
		})

		// Never silently skips what the policy promised, and never holds a resource with no way out.
		It("holds a Delete resource it can't deactivate, until the policy becomes Orphan", func() {
			create(registryv1alpha1.AppshellApplicationSpec{Name: storefront,
				DeletionPolicy: registryv1alpha1.DeletionPolicyDelete})
			_, err := reconcileOnce()
			Expect(err).NotTo(HaveOccurred())

			fake.err = &registry.Error{Status: 403, Message: "No trusted deployer",
				Refusal: registry.RefusalNotTrusted}
			deleteIt()
			result, err := reconcileOnce()
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(refusedRetry))
			Expect(gone()).To(BeFalse())
			Expect(ready().Reason).To(Equal(ReasonDeactivationFailed))
			Expect(ready().Message).To(ContainSubstring("deletionPolicy: Orphan"))

			app := fetch()
			app.Spec.DeletionPolicy = registryv1alpha1.DeletionPolicyOrphan
			Expect(k8sClient.Update(ctx, app)).To(Succeed())
			calls := len(fake.calls())

			_, err = reconcileOnce()
			Expect(err).NotTo(HaveOccurred())
			Expect(gone()).To(BeTrue())
			Expect(fake.calls()).To(HaveLen(calls)) // let go without trying again
		})

		It("releases the finalizer when the policy changes from Delete to Orphan", func() {
			create(registryv1alpha1.AppshellApplicationSpec{Name: storefront,
				DeletionPolicy: registryv1alpha1.DeletionPolicyDelete})
			_, err := reconcileOnce()
			Expect(err).NotTo(HaveOccurred())

			app := fetch()
			app.Spec.DeletionPolicy = registryv1alpha1.DeletionPolicyOrphan
			Expect(k8sClient.Update(ctx, app)).To(Succeed())
			_, err = reconcileOnce()
			Expect(err).NotTo(HaveOccurred())
			Expect(fetch().Finalizers).To(BeEmpty())
		})
	})
})

var _ = Describe("ResourceOf", func() {
	It("says the same thing in the registry's words, leaving out what the spec leaves out", func() {
		resource := ResourceOf(&registryv1alpha1.AppshellApplication{Spec: registryv1alpha1.AppshellApplicationSpec{
			Name: storefront,
			Shell: &registryv1alpha1.ShellConfig{
				Root:      new("ContainerModule/Container"),
				RootProps: &apiextensionsv1.JSON{Raw: []byte(`{"mode":"compact"}`)},
			},
			Overrides:        &apiextensionsv1.JSON{Raw: []byte(`{"vars":{"cart":{"currency":"EUR"}}}`)},
			TokenEnforcement: new(registryv1alpha1.EnforcementLevel("block")),
		}})

		Expect(resource.APIVersion).To(Equal(registry.APIVersion))
		Expect(resource.Kind).To(Equal(registry.Kind))
		Expect(resource.Name).To(Equal(storefront))
		Expect(string(resource.Spec.Shell)).To(MatchJSON(`{"root":"ContainerModule/Container","rootProps":{"mode":"compact"}}`))
		Expect(string(resource.Spec.Overrides)).To(MatchJSON(`{"vars":{"cart":{"currency":"EUR"}}}`))
		Expect(*resource.Spec.TokenEnforcement).To(Equal("block"))

		encoded, err := json.Marshal(resource.Spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).NotTo(ContainSubstring("packages"))
		Expect(string(encoded)).NotTo(ContainSubstring("allowOverlays"))
	})

	// Left out or empty leaves the application's packages alone; only deletion sends an empty set.
	It("sends no package set for an empty one", func() {
		resource := ResourceOf(&registryv1alpha1.AppshellApplication{Spec: registryv1alpha1.AppshellApplicationSpec{
			Name: storefront, Packages: []string{},
		}})
		Expect(resource.Spec.Packages).To(BeNil())
	})
})
