package steps

import (
	"context"
	"fmt"

	"rondo/internal/log"
)

type Step struct {
	Name string
	Run  func(ctx context.Context) error
}

func Execute(ctx context.Context, plan []Step) error {
	for _, s := range plan {
		if err := s.Run(ctx); err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
	}
	log.Step("done")
	return nil
}
