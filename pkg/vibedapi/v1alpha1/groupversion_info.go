// +kubebuilder:object:generate=true
// +groupName=vibed.dev

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the API group + version this package serves. Note that the
// CRD group (vibed.dev) is independent of vibeD's Go module path; we don't
// reuse agent-sandbox's group, since VibedApp is our type to evolve.
var GroupVersion = schema.GroupVersion{Group: "vibed.dev", Version: "v1alpha1"}

// SchemeBuilder collects the package's Go types so controllers can register
// them with a runtime.Scheme via AddToScheme.
var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

// AddToScheme adds this package's types to the given scheme. Required by
// controller-runtime's manager and by the typed client.
var AddToScheme = SchemeBuilder.AddToScheme

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&VibedApp{},
		&VibedAppList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
