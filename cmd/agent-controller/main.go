// Command agent-controller runs sandboxed agent sessions on Kubernetes.
//
// One binary serves two roles: "controller" admits and executes AgentRuns,
// "server" exposes the REST API, the MCP server and the Logins page.
package main

import (
	"fmt"
	"io"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/mudler/agent-controller/internal/controller"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: agent-controller <command> [flags]

commands:
  controller   run the controller (admission, schedules, launcher Jobs)
  server       run the REST API, MCP server and Logins page
  token        manage API tokens ("token new --name N --scopes a,b")
  version      print the version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches the subcommand and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "controller":
		return runController(args[1:], stderr)
	case "server":
		return runServer(args[1:], stderr)
	case "token":
		return runToken(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func runController(args []string, stderr io.Writer) int {
	cfg, opts, err := controller.ParseControllerFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "controller: %v\n", err)
		return 2
	}
	ctrl.SetLogger(zap.New(zap.WriteTo(stderr)))
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		fmt.Fprintf(stderr, "controller: loading kubeconfig: %v\n", err)
		return 1
	}
	mgr, err := controller.NewManager(restCfg, cfg, opts)
	if err != nil {
		fmt.Fprintf(stderr, "controller: %v\n", err)
		return 1
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		fmt.Fprintf(stderr, "controller: %v\n", err)
		return 1
	}
	return 0
}

func runServer(_ []string, stderr io.Writer) int {
	fmt.Fprintln(stderr, "server: not implemented")
	return 1
}

func runToken(_ []string, _ io.Writer, stderr io.Writer) int {
	fmt.Fprintln(stderr, "token: not implemented")
	return 1
}
