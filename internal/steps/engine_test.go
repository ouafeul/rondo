package steps

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExecuteOrderAndErrorWrap(t *testing.T) {
	var order []string
	mk := func(name string, err error) Step {
		return Step{Name: name, Run: func(context.Context) error {
			order = append(order, name)
			return err
		}}
	}

	if err := Execute(context.Background(), []Step{mk("a", nil), mk("b", nil)}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "a,b" {
		t.Errorf("order: %v", order)
	}

	order = nil
	boom := errors.New("boom")
	err := Execute(context.Background(), []Step{mk("a", nil), mk("b", boom), mk("c", nil)})
	if err == nil || !strings.HasPrefix(err.Error(), "b: ") || !errors.Is(err, boom) {
		t.Fatalf("error: %v", err)
	}
	if strings.Join(order, ",") != "a,b" {
		t.Errorf("step c run after a failure: %v", order)
	}
}
