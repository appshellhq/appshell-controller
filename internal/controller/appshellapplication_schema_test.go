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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// removeAll deletes every test resource, finalizers and all, so one test's leftovers can't
// change the next.
func removeAll(ctx context.Context) {
	var list registryv1alpha1.AppshellApplicationList
	Expect(k8sClient.List(ctx, &list, client.InNamespace(namespace))).To(Succeed())
	for i := range list.Items {
		app := &list.Items[i]
		if len(app.Finalizers) > 0 {
			app.Finalizers = nil
			Expect(client.IgnoreNotFound(k8sClient.Update(ctx, app))).To(Succeed())
		}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, app))).To(Succeed())
	}
}

// The CRD's own validation, enforced by a real API server: what a resource may say is decided
// here, before anything reaches the controller or the registry.
var _ = Describe("The AppshellApplication schema", func() {
	ctx := context.Background()

	AfterEach(func() { removeAll(ctx) })

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

	It("refuses a theme ref that isn't pinned, which would drift to each new version", func() {
		app := application("unpinned-theme", registryv1alpha1.AppshellApplicationSpec{
			Name:  storefront,
			Shell: &registryv1alpha1.ShellConfig{Theme: &registryv1alpha1.ThemeSelection{Ref: new("appshell/neutral-ice")}},
		})
		Expect(k8sClient.Create(ctx, app)).NotTo(Succeed())
	})

	It("accepts a pinned theme ref", func() {
		app := application("pinned-theme", registryv1alpha1.AppshellApplicationSpec{
			Name: storefront,
			Shell: &registryv1alpha1.ShellConfig{
				Theme: &registryv1alpha1.ThemeSelection{Ref: new("appshell/neutral-ice@1.0.0")},
			},
		})
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
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

	// A different application is a different resource; retargeting one would leave the first
	// application unreconciled and silently take over the second.
	It("keeps the name fixed once set", func() {
		app := application("fixed", registryv1alpha1.AppshellApplicationSpec{Name: storefront})
		Expect(k8sClient.Create(ctx, app)).To(Succeed())

		app.Spec.Name = "acme/elsewhere"
		Expect(k8sClient.Update(ctx, app)).To(MatchError(ContainSubstring("name is immutable")))
	})
})
