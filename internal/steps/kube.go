package steps

import (
	"context"

	"rondo/internal/runner"
)

type KubeClient struct {
	ws     Workspace
	runner runner.CommandRunner
}

func NewKubeClient(ws Workspace, run runner.CommandRunner) *KubeClient {
	return &KubeClient{ws: ws, runner: run}
}

func (k *KubeClient) Output(ctx context.Context, kubeconfig string, args ...string) (string, error) {
	return k.runner.Output(ctx, runner.Cmd{
		Name: k.ws.Tool("kubectl"), Args: args,
		Env: map[string]string{"KUBECONFIG": kubeconfig},
	})
}

func (k *KubeClient) run() runner.CommandRunner { return k.runner }

func (k *KubeClient) Run(ctx context.Context, kubeconfig string, args ...string) error {
	return k.runner.Run(ctx, runner.Cmd{
		Name: k.ws.Tool("kubectl"), Args: args,
		Env: map[string]string{"KUBECONFIG": kubeconfig},
	})
}

func (k *KubeClient) ApplyDir(ctx context.Context, kubeconfig, dir string) error {
	return k.runner.Run(ctx, runner.Cmd{
		Name: k.ws.Tool("kubectl"), Args: []string{"apply", "-f", dir},
		Env: map[string]string{"KUBECONFIG": kubeconfig},
	})
}

type ServerSideApply struct {
	Kubeconfig     string
	Dir            string
	Namespace      string
	FieldManager   string
	ForceConflicts bool
}

func (k *KubeClient) ApplyDirServerSide(ctx context.Context, a ServerSideApply) error {
	args := []string{"apply", "--server-side", "--field-manager=" + a.FieldManager,
		"-n", a.Namespace, "-R", "-f", a.Dir}
	if a.ForceConflicts {
		args = append(args, "--force-conflicts")
	}
	return k.runner.Run(ctx, runner.Cmd{
		Name: k.ws.Tool("kubectl"), Args: args,
		Env: map[string]string{"KUBECONFIG": a.Kubeconfig},
	})
}

func (k *KubeClient) ApplyStdin(ctx context.Context, kubeconfig, manifest string) error {
	return k.runner.Run(ctx, runner.Cmd{
		Name: k.ws.Tool("kubectl"), Args: []string{"apply", "-f", "-"},
		Env: map[string]string{"KUBECONFIG": kubeconfig}, Stdin: manifest,
	})
}
