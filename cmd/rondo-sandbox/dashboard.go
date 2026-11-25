package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"rondo/internal/kobj"
	"rondo/internal/sandbox"
)

func dashboardCommand() *cobra.Command {
	var port int
	var noOpen bool
	dashboard := &cobra.Command{
		Use:   "dashboard",
		Short: "The RONDO dashboard on localhost, through a port-forward to the management cluster, until Ctrl-C",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDashboard(cmd.Context(), cmd.OutOrStdout(), port, !noOpen)
		},
		ValidArgsFunction: cobra.NoFileCompletions,
	}
	dashboard.Flags().IntVar(&port, "port", 8080, "workstation port")
	dashboard.Flags().BoolVar(&noOpen, "no-open", false, "does not open the browser")
	_ = dashboard.RegisterFlagCompletionFunc("port", fixed("8080", "9090"))
	return dashboard
}

// runDashboard forwards a workstation port to the RONDO dashboard, which the sandbox's
// management cluster serves without an ingress, and opens it once the forward listens. It
// goes through the sandbox's kubeconfig, never the workstation's.
func runDashboard(ctx context.Context, out io.Writer, port int, open bool) error {
	kc, err := sandbox.Kubeconfig(settings(), "")
	if err != nil {
		return err
	}
	if _, err := os.Stat(kc); err != nil {
		return fmt.Errorf("no sandbox cluster (%s): rondo-sandbox deploy", kc)
	}
	path, err := exec.LookPath("kubectl")
	if err != nil {
		return fmt.Errorf("kubectl not found in PATH")
	}
	c := exec.CommandContext(ctx, path, "--kubeconfig", kc, "-n", kobj.PlatformNamespace, //nolint:gosec // fixed arguments
		"port-forward", "svc/"+kobj.DashboardName, fmt.Sprintf("%d:%d", port, kobj.DashboardPort))
	c.Stderr = os.Stderr
	stdout, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	if err := c.Start(); err != nil {
		return err
	}
	url := fmt.Sprintf("http://localhost:%d", port)
	scanner := bufio.NewScanner(stdout)
	announced := false
	for scanner.Scan() {
		if announced || !strings.HasPrefix(scanner.Text(), "Forwarding from") {
			continue
		}
		announced = true
		fmt.Fprintf(out, "RONDO dashboard: %s (Ctrl-C to stop)\n", url)
		if open {
			openBrowser(url)
		}
	}
	err = c.Wait()
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("port-forward to the dashboard: %w (port %d already taken? --port picks another)", err, port)
	}
	return nil
}

func openBrowser(url string) {
	for _, opener := range []string{"xdg-open", "open"} {
		if path, err := exec.LookPath(opener); err == nil {
			_ = exec.Command(path, url).Start() //nolint:gosec // a localhost URL
			return
		}
	}
}
