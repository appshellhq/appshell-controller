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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	registryv1alpha1 "github.com/appshellhq/appshell-controller/api/v1alpha1"
)

const (
	namespace = "default"
	// storefront is the registry application the tests' resources stand for.
	storefront = "acme/storefront"
)

func application(name string, spec registryv1alpha1.AppshellApplicationSpec) *registryv1alpha1.AppshellApplication {
	return &registryv1alpha1.AppshellApplication{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       spec,
	}
}

var _ = Describe("AppshellApplication", func() {
	ctx := context.Background()

	// The CRD's own validation, enforced by a real API server: what a resource may say is
	// decided here, before anything reaches the controller or the registry.
	Context("the resource's schema", func() {
		AfterEach(func() {
			Expect(k8sClient.DeleteAllOf(ctx, &registryv1alpha1.AppshellApplication{},
				client.InNamespace(namespace))).To(Succeed())
		})

		It("defaults deletionPolicy to Orphan, so a mistaken delete can't take an application down", func() {
			app := application("defaulted", registryv1alpha1.AppshellApplicationSpec{Name: storefront})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			Expect(app.Spec.DeletionPolicy).To(Equal(registryv1alpha1.DeletionPolicyOrphan))
		})

		It("requires a scope-qualified name, since the controller has no scope of its own", func() {
			app := application("unscoped", registryv1alpha1.AppshellApplicationSpec{Name: "storefront"})
			Expect(k8sClient.Create(ctx, app)).NotTo(Succeed())
		})

		It("refuses a package that isn't pinned", func() {
			app := application("unpinned", registryv1alpha1.AppshellApplicationSpec{
				Name:     storefront,
				Packages: []string{"acme/catalog"},
			})
			Expect(k8sClient.Create(ctx, app)).NotTo(Succeed())
		})

		It("refuses a deletion policy it doesn't know", func() {
			app := application("policy", registryv1alpha1.AppshellApplicationSpec{
				Name:           storefront,
				DeletionPolicy: "Cascade",
			})
			Expect(k8sClient.Create(ctx, app)).NotTo(Succeed())
		})

		It("refuses a stylesheet that isn't http or https", func() {
			app := application("stylesheet", registryv1alpha1.AppshellApplicationSpec{
				Name:  storefront,
				Shell: &registryv1alpha1.ShellConfig{StylesheetURL: new("javascript:alert(1)")},
			})
			Expect(k8sClient.Create(ctx, app)).NotTo(Succeed())
		})

		It("refuses a favicon under any scheme but http or https", func() {
			app := application("favicon", registryv1alpha1.AppshellApplicationSpec{
				Name:  storefront,
				Shell: &registryv1alpha1.ShellConfig{FaviconURL: new("javascript:alert(1)")},
			})
			Expect(k8sClient.Create(ctx, app)).NotTo(Succeed())
		})

		It("accepts a favicon as a relative path, as the registry does", func() {
			app := application("relative-favicon", registryv1alpha1.AppshellApplicationSpec{
				Name:  storefront,
				Shell: &registryv1alpha1.ShellConfig{FaviconURL: new("/favicon.ico")},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		// A different application is a different resource; retargeting one would leave the
		// first application unreconciled and silently take over the second.
		It("keeps the name fixed once set", func() {
			app := application("fixed", registryv1alpha1.AppshellApplicationSpec{Name: storefront})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			app.Spec.Name = "acme/elsewhere"
			Expect(k8sClient.Update(ctx, app)).To(MatchError(ContainSubstring("name is immutable")))
		})
	})

	Context("reconciling", func() {
		const name = "reconciled"
		key := types.NamespacedName{Name: name, Namespace: namespace}

		BeforeEach(func() {
			Expect(k8sClient.Create(ctx, application(name, registryv1alpha1.AppshellApplicationSpec{
				Name:     storefront,
				Packages: []string{"acme/catalog@1.2.0"},
			}))).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.DeleteAllOf(ctx, &registryv1alpha1.AppshellApplication{},
				client.InNamespace(namespace))).To(Succeed())
		})

		It("records the generation it saw, and that it has yet to apply it", func() {
			reconciler := &AppshellApplicationReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			var app registryv1alpha1.AppshellApplication
			Expect(k8sClient.Get(ctx, key, &app)).To(Succeed())
			Expect(app.Status.ObservedGeneration).To(Equal(app.Generation))

			ready := meta.FindStatusCondition(app.Status.Conditions, registryv1alpha1.ConditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionUnknown))
			Expect(ready.Reason).To(Equal(ReasonAwaitingApply))
		})

		// Another writer got there first. Retried against what's there now, rather than written
		// over, and not reported as an error: a lost race is expected.
		It("retries a status write that lost a race, instead of overwriting it", func() {
			watching, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
			Expect(err).NotTo(HaveOccurred())
			conflicted := interceptor.NewClient(watching, interceptor.Funcs{
				SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch,
					...client.SubResourcePatchOption) error {
					return apierrors.NewConflict(schema.GroupResource{
						Group: registryv1alpha1.GroupVersion.Group, Resource: "appshellapplications",
					}, name, nil)
				},
			})
			reconciler := &AppshellApplicationReconciler{Client: conflicted, Scheme: k8sClient.Scheme()}

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(conflictRetry))
		})

		It("does nothing for a resource that's gone", func() {
			reconciler := &AppshellApplicationReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "absent", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
