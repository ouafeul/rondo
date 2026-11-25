package runner

import (
	"context"
	"fmt"
)

type Fake struct {
	Calls   []Cmd
	Outputs map[string]string
	Errs    map[string]error
	Effect  func(Cmd) error
}

func (f *Fake) Run(_ context.Context, c Cmd) error {
	f.Calls = append(f.Calls, c)
	if err := f.effect(c); err != nil {
		return err
	}
	return f.Errs[c.String()]
}

func (f *Fake) Output(_ context.Context, c Cmd) (string, error) {
	f.Calls = append(f.Calls, c)
	if err := f.effect(c); err != nil {
		return "", err
	}
	if err := f.Errs[c.String()]; err != nil {
		return "", err
	}
	out, ok := f.Outputs[c.String()]
	if !ok {
		return "", fmt.Errorf("fake: no programmed output for %q", c.String())
	}
	return out, nil
}

func (f *Fake) effect(c Cmd) error {
	if f.Effect == nil {
		return nil
	}
	return f.Effect(c)
}
