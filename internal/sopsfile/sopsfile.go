// Package sopsfile reads a sops-encrypted YAML file's metadata without
// decrypting it, and runs sops to change one. Values are only ever passed
// to sops on stdin, never on its command line.
package sopsfile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ryanmwright/careen/internal/execx"
)

// Meta is what a secrets file says about itself in the clear.
type Meta struct {
	// Names are the secrets' keys, in file order. Their values are
	// encrypted; the names are not.
	Names           []string  `json:"names"`
	AgeRecipients   []string  `json:"ageRecipients"`
	PGPFingerprints []string  `json:"pgpFingerprints"`
	LastModified    time.Time `json:"lastModified"`
}

// ReadMeta reads path's metadata.
func ReadMeta(path string) (*Meta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m := &Meta{Names: []string{}, AgeRecipients: []string{}, PGPFingerprints: []string{}}
	if len(doc.Content) == 0 {
		return m, nil
	}
	top := doc.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if name := top.Content[i].Value; name != "sops" {
			m.Names = append(m.Names, name)
			continue
		}
		var s struct {
			Age []struct {
				Recipient string `yaml:"recipient"`
			} `yaml:"age"`
			PGP []struct {
				FP string `yaml:"fp"`
			} `yaml:"pgp"`
			LastModified time.Time `yaml:"lastmodified"`
		}
		if err := top.Content[i+1].Decode(&s); err != nil {
			return nil, fmt.Errorf("%s: sops metadata: %w", path, err)
		}
		for _, a := range s.Age {
			m.AgeRecipients = append(m.AgeRecipients, a.Recipient)
		}
		for _, p := range s.PGP {
			m.PGPFingerprints = append(m.PGPFingerprints, p.FP)
		}
		m.LastModified = s.LastModified
	}
	return m, nil
}

// Sops runs sops in a repo, so its .sops.yaml applies. Paths are relative
// to Dir or absolute.
type Sops struct {
	Dir string
	// Env is added to sops's environment, e.g. SOPS_AGE_KEY_FILE in tests.
	Env []string
}

func (s Sops) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sops", args...)
	cmd.Dir = s.Dir
	cmd.Env = append(os.Environ(), s.Env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sops %s: %w: %s", args[0], err, execx.Tail(stderr.String(), 3))
	}
	return out, nil
}

// UpdateKeys re-encrypts file's data key to the recipients .sops.yaml
// gives it now. It needs a key that can decrypt the file today.
func (s Sops) UpdateKeys(ctx context.Context, file string) error {
	_, err := s.run(ctx, nil, "updatekeys", "--yes", file)
	return err
}

// Create writes a new file holding one secret, encrypted to the
// recipients .sops.yaml gives it. Only public keys are needed.
func (s Sops) Create(ctx context.Context, file, name, value string) error {
	return s.CreateValues(ctx, file, map[string]string{name: value})
}

// CreateValues writes a new file holding several secrets. Names ending in
// _unencrypted stay readable without a key (sops's default
// unencrypted_suffix), for public values that belong next to secrets.
func (s Sops) CreateValues(ctx context.Context, file string, values map[string]string) error {
	plain, err := yaml.Marshal(values)
	if err != nil {
		return err
	}
	out, err := s.run(ctx, plain, "encrypt", "--filename-override", file, "--input-type", "yaml", "--output-type", "yaml")
	if err != nil {
		return err
	}
	path := file
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.Dir, file)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// Set sets one secret in an existing file. It needs a key that can decrypt
// the file.
func (s Sops) Set(ctx context.Context, file, name, value string) error {
	v, _ := json.Marshal(value)
	_, err := s.run(ctx, v, "set", "--value-stdin", file, index(name))
	return err
}

// Extract decrypts one secret from a file. It needs a key that can decrypt
// the file; sops doesn't read .sops.yaml for this.
func (s Sops) Extract(ctx context.Context, file, name string) (string, error) {
	out, err := s.run(ctx, nil, "decrypt", "--extract", index(name), file)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Plain reads a value sops left unencrypted (a name ending in
// _unencrypted) without decrypting anything.
func Plain(path, name string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	v, ok := doc[name].(string)
	if !ok || strings.HasPrefix(v, "ENC[") {
		return "", fmt.Errorf("%s has no plain value %s", path, name)
	}
	return v, nil
}

// Unset removes one secret from a file.
func (s Sops) Unset(ctx context.Context, file, name string) error {
	_, err := s.run(ctx, nil, "unset", file, index(name))
	return err
}

// Edit is the command that opens file in $EDITOR through sops; the caller
// runs it with a terminal.
func (s Sops) Edit(file string) *exec.Cmd {
	cmd := exec.Command("sops", file)
	cmd.Dir = s.Dir
	cmd.Env = append(os.Environ(), s.Env...)
	return cmd
}

func index(name string) string {
	b, _ := json.Marshal(name)
	return "[" + string(b) + "]"
}
