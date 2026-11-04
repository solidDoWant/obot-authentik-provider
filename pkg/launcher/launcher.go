// Package launcher adds a provider registry to the list that the Obot image loads when it starts.
package launcher

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// EnvrcPath is the file that the Obot image's run.sh sources for its provider registries.
	EnvrcPath = "/obot-providers/.envrc.providers"

	registriesVariable = "OBOT_SERVER_PROVIDER_REGISTRIES"
)

// RegistryLine returns the line that adds registry to the registries that envrc sets.
func RegistryLine(registry string) (string, error) {
	if !filepath.IsAbs(registry) || strings.ContainsAny(registry, "\"$`\\\n,") {
		return "", fmt.Errorf("registry path %q must be absolute and can't hold quotes, $, `, \\, commas, or newlines", registry)
	}
	return fmt.Sprintf(`export %[1]s="${%[1]s:+${%[1]s},}%[2]s"`, registriesVariable, registry), nil
}

// AddRegistry appends the line for registry to the envrc file, unless it's already there. The
// file must exist: if it doesn't, the Obot image no longer starts the way this package expects.
func AddRegistry(envrc, registry string) error {
	line, err := RegistryLine(registry)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(envrc)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s doesn't exist, so this Obot image doesn't load provider registries the way this version of the provider expects", envrc)
	}
	if err != nil {
		return err
	}
	for existing := range bytes.Lines(content) {
		if strings.TrimSpace(string(existing)) == line {
			return nil
		}
	}

	file, err := os.OpenFile(envrc, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open %s for writing (the container's root filesystem must be writable): %w", envrc, err)
	}
	prefix := ""
	if len(content) > 0 && content[len(content)-1] != '\n' {
		prefix = "\n"
	}
	if _, err := file.WriteString(prefix + line + "\n"); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", envrc, err)
	}
	return file.Close()
}
