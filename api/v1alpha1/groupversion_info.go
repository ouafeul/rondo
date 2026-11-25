// +kubebuilder:object:generate=true
// +groupName=rondo.ouafeul.github.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var GroupVersion = schema.GroupVersion{Group: "rondo.ouafeul.github.io", Version: "v1alpha1"}

var SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

var AddToScheme = SchemeBuilder.AddToScheme
