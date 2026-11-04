// Command start-obot is the entrypoint for an Obot container with this provider registry mounted.
// It adds the registry it was run from to the ones Obot loads, then runs the given command, or
// the Obot image's run.sh without one. Pass another plugin's launcher as the command to chain
// them.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/solidDoWant/obot-authentik-provider/pkg/launcher"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: start-obot: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find this executable: %w", err)
	}
	if executable, err = filepath.EvalSymlinks(executable); err != nil {
		return fmt.Errorf("resolve this executable: %w", err)
	}
	// The executable is at the root of the registry.
	registry := filepath.Dir(executable)
	if err := launcher.AddRegistry(launcher.EnvrcPath, registry); err != nil {
		return err
	}

	if len(args) == 0 {
		args = []string{"run.sh"}
	}
	command, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("find %s: %w", args[0], err)
	}
	return syscall.Exec(command, args, os.Environ())
}
