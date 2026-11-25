package jobs

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func LiveFor(ctx context.Context, r client.Reader, namespace, kind, name string) (*batchv1.Job, error) {
	var list batchv1.JobList
	if err := r.List(ctx, &list, client.InNamespace(namespace), client.MatchingLabels{
		LabelOwnerKind: kind,
		LabelOwnerName: name,
	}); err != nil {
		return nil, fmt.Errorf("listing the Jobs of %s/%s: %w", kind, name, err)
	}
	for i := range list.Items {
		if done, _ := Finished(&list.Items[i]); !done {
			return &list.Items[i], nil
		}
	}
	return nil, nil
}
