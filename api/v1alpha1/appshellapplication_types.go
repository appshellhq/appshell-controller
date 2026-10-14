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

package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// The spec mirrors the appshell registry's Application resource, the body of its
// `POST /v1/apply` (apiVersion registry.appshell.org/v1, kind Application), so a resource here
// and a file applied with `appshell app apply` say the same thing in the same words.

// DeletionPolicy says what deleting an AppshellApplication does to the registry application it
// stands for.
// +kubebuilder:validation:Enum=Orphan;Delete
type DeletionPolicy string

const (
	// DeletionPolicyOrphan leaves the registry application as it is: its packages stay active,
	// and it simply stops being reconciled. The default, so a resource deleted by mistake, or
	// pruned by a sync, can't take an application down.
	DeletionPolicyOrphan DeletionPolicy = "Orphan"

	// DeletionPolicyDelete deactivates the application's packages before the resource goes.
	// It concerns this one application only, never others that activate the same packages.
	DeletionPolicyDelete DeletionPolicy = "Delete"
)

// EnforcementLevel is how the registry treats a requirement an application doesn't meet.
// +kubebuilder:validation:Enum=off;warn;block
type EnforcementLevel string

// ColorScheme is the color scheme an application's theme renders in.
// +kubebuilder:validation:Enum=system;light;dark
type ColorScheme string

// ThemeSelection chooses an application's theme: a published theme pinned by ref, or a base
// and accent the registry resolves to one.
type ThemeSelection struct {
	// ref is a published theme, pinned, as scope/name@version.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Ref *string `json:"ref,omitempty"`

	// base is a theme preset to resolve, with accent, to a published theme.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Base *string `json:"base,omitempty"`

	// accent is the accent to resolve, with base, to a published theme.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Accent *string `json:"accent,omitempty"`

	// colorScheme is the color scheme the theme renders in.
	// +optional
	ColorScheme *ColorScheme `json:"colorScheme,omitempty"`

	// overrides sets roles in the design-token contract, by role name. The registry validates
	// the roles against the contract it versions.
	// +optional
	Overrides map[string]string `json:"overrides,omitempty"`
}

// ShellConfig is the shell an application renders in.
type ShellConfig struct {
	// root is the component the shell renders at its root, as Module/Component.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Root *string `json:"root,omitempty"`

	// rootProps are passed to the root component as its props.
	// +optional
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	RootProps *apiextensionsv1.JSON `json:"rootProps,omitempty"`

	// title is the application's title.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Title *string `json:"title,omitempty"`

	// description is the application's description.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Description *string `json:"description,omitempty"`

	// theme chooses the application's theme.
	// +optional
	Theme *ThemeSelection `json:"theme,omitempty"`

	// faviconUrl is the favicon the shell links to: an http or https URL, or a path relative to the
	// application, as the registry accepts. Never another scheme.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^(https?://|[^:]*$)`
	FaviconURL *string `json:"faviconUrl,omitempty"`

	// stylesheetUrl is a stylesheet the shell links to, over http or https.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://`
	StylesheetURL *string `json:"stylesheetUrl,omitempty"`

	// shellBundleUrl replaces the shell bundle the registry serves, over http or https.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://`
	ShellBundleURL *string `json:"shellBundleUrl,omitempty"`
}

// AppshellApplicationSpec is an application's desired state in the appshell registry.
type AppshellApplicationSpec struct {
	// name is the registry application this resource stands for, as scope/name. Always
	// qualified: the controller has no scope of its own, and the registry refuses a workload's
	// apply that doesn't name one. Fixed once set; a different application is a different
	// resource.
	// +required
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9-_]*/[a-z0-9][a-z0-9-_.]*$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="name is immutable; a different application is a different resource"
	Name string `json:"name"`

	// shell is the shell the application renders in.
	// +optional
	Shell *ShellConfig `json:"shell,omitempty"`

	// packages is the full set of packages the application activates, each pinned as
	// scope/name@version. Any package active in the application but missing here is
	// deactivated. Left out, or empty, the application's packages are left alone.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:items:MaxLength=512
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9][a-z0-9-_]*/[a-z0-9][a-z0-9-_.]*@.+$`
	Packages []string `json:"packages,omitempty"`

	// overrides are the application's configuration overrides, as the registry's overrides
	// object: vars by federation scope, and the rest it defines.
	// +optional
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	Overrides *apiextensionsv1.JSON `json:"overrides,omitempty"`

	// allowOverrides lets a request override the application's configuration.
	// +optional
	AllowOverrides *bool `json:"allowOverrides,omitempty"`

	// allowOverlays lets a development overlay apply to the application in a browser session.
	// +optional
	AllowOverlays *bool `json:"allowOverlays,omitempty"`

	// servingBaseUrl is the origin the application's packages are served from, when it isn't
	// the registry's own.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://`
	ServingBaseURL *string `json:"servingBaseUrl,omitempty"`

	// sharedBaselines are the shared dependencies the application pins, by package name, as the
	// registry's shared objects.
	// +optional
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	SharedBaselines *apiextensionsv1.JSON `json:"sharedBaselines,omitempty"`

	// sharedDepsEnforcement is how the registry treats shared-dependency conflicts.
	// +optional
	SharedDepsEnforcement *EnforcementLevel `json:"sharedDepsEnforcement,omitempty"`

	// tokenEnforcement is how the registry treats design tokens a package needs and the theme
	// doesn't provide.
	// +optional
	TokenEnforcement *EnforcementLevel `json:"tokenEnforcement,omitempty"`

	// varsEnforcement is how the registry treats vars a package needs and the application
	// doesn't supply.
	// +optional
	VarsEnforcement *EnforcementLevel `json:"varsEnforcement,omitempty"`

	// deletionPolicy is what deleting this resource does to the registry application.
	// +optional
	// +kubebuilder:default=Orphan
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// Condition types and reasons an AppshellApplication reports.
const (
	// ConditionReady is True when the registry application matches the spec at the
	// observed generation.
	ConditionReady = "Ready"
)

// AppshellApplicationStatus is what the controller last observed.
type AppshellApplicationStatus struct {
	// observedGeneration is the generation of the spec the status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions describe the resource's state. Ready is True when the registry application
	// matches the spec at observedGeneration.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// appliedPackages are the packages as of the last successful apply.
	// +optional
	// +listType=set
	AppliedPackages []string `json:"appliedPackages,omitempty"`

	// lastAppliedTime is when the registry last accepted an apply of this resource.
	// +optional
	LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=aapp,categories=appshell
// +kubebuilder:printcolumn:name="Application",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AppshellApplication is an application in the appshell registry, kept at the state its spec
// declares: reconciled through the registry's apply API with the controller's own workload
// identity, under a trusted-deployment record on that application.
type AppshellApplication struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec is the application's desired state in the registry.
	// +required
	Spec AppshellApplicationSpec `json:"spec"`

	// status is what the controller last observed.
	// +optional
	Status AppshellApplicationStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AppshellApplicationList contains a list of AppshellApplication
type AppshellApplicationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AppshellApplication `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &AppshellApplication{}, &AppshellApplicationList{})
		return nil
	})
}
