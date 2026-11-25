package probe

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"rondo/api/v1alpha1"
)

func AssessInventory(ctx context.Context, c client.Reader, inventory []v1alpha1.InventoryEntry) (Health, string) {
	return AssessInventoryWith(ctx, c, inventory, nil)
}

func AssessInventoryWith(ctx context.Context, c client.Reader, inventory []v1alpha1.InventoryEntry,
	rules []v1alpha1.HealthRule) (Health, string) {
	declared := declaredTable(rules)
	assessments := make([]Assessment, 0, len(inventory))
	for _, e := range inventory {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(schema.GroupVersionKind{Group: e.Group, Version: e.Version, Kind: e.Kind})
		err := c.Get(ctx, types.NamespacedName{Namespace: e.Namespace, Name: e.Name}, u)
		switch {
		case apierrors.IsNotFound(err):
			assessments = append(assessments, Assessment{
				GVK: e.Kind, Namespace: e.Namespace, Name: e.Name,
				Health:  v1alpha1.HealthDegraded,
				Message: "object missing from the cluster (rejected by a webhook? deleted by a controller?)",
			})
		case err != nil:
			assessments = append(assessments, Assessment{
				GVK: e.Kind, Namespace: e.Namespace, Name: e.Name,
				Health: v1alpha1.HealthUnknown, Message: "read failed: " + err.Error(),
			})
		default:
			assessments = append(assessments, AssessWith(u, declared))
		}
	}
	return Aggregate(assessments)
}
