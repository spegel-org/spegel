package containerd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/go-logr/logr"
	"github.com/pelletier/go-toml/v2"
	tomlu "github.com/pelletier/go-toml/v2/unstable"

	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/oci"
)

const (
	backupDir = "_backup"
)

// Refer to containerd registry configuration documentation for more information about required configuration.
// https://github.com/containerd/containerd/blob/main/docs/cri/config.md#registry-configuration
// https://github.com/containerd/containerd/blob/main/docs/hosts.md#registry-configuration---examples
func AddMirrorConfiguration(ctx context.Context, configPath string, mirroredRegistries, mirrorTargets []string, resolveTags, prependExisting bool, userinfo *url.Userinfo) error {
	log := logr.FromContextOrDiscard(ctx)

	// Parse and verify mirror urls.
	parsedMirroredRegistries, err := oci.ParseRegistries(mirroredRegistries, true)
	if err != nil {
		return err
	}
	parsedMirrorTargets, err := oci.ParseRegistries(mirrorTargets, false)
	if err != nil {
		return err
	}

	// Backup and clear configgurrationn.
	err = os.MkdirAll(configPath, 0o755)
	if err != nil {
		return err
	}
	err = backupConfig(log, configPath)
	if err != nil {
		return err
	}
	err = clearConfig(configPath)
	if err != nil {
		return err
	}

	// Write mirror configuration
	capabilities := []string{"pull"}
	if resolveTags {
		capabilities = append(capabilities, "resolve")
	}
	for _, mr := range parsedMirroredRegistries {
		templatedHosts, err := templateHosts(mr, parsedMirrorTargets, capabilities, userinfo)
		if err != nil {
			return err
		}
		if prependExisting {
			existingHosts, err := existingHosts(configPath, mr)
			if err != nil {
				return err
			}
			if existingHosts != "" {
				// If we are prepending we also want to keep files like certificates that may be referenced.
				backupRegDir := path.Join(configPath, backupDir, mr.Host)
				err = filepath.WalkDir(backupRegDir, func(path string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if d.IsDir() {
						return nil
					}
					if d.Name() == "hosts.toml" {
						return nil
					}
					src, err := os.Open(path)
					if err != nil {
						return err
					}
					defer src.Close()
					relPath, err := filepath.Rel(backupRegDir, path)
					if err != nil {
						return err
					}
					dstPath := filepath.Join(configPath, mr.Host, relPath)
					err = os.MkdirAll(filepath.Dir(dstPath), 0o755)
					if err != nil {
						return err
					}
					dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
					if err != nil {
						return err
					}
					defer dst.Close()
					_, err = io.Copy(dst, src)
					if err != nil {
						return err
					}
					return nil
				})
				if err != nil {
					return err
				}

				templatedHosts = templatedHosts + "\n\n" + existingHosts
				log.Info("prepending to existing containerd mirror configuration", "registry", mr.String())
			}

		}
		fp := path.Join(configPath, mr.Host, "hosts.toml")
		err = os.MkdirAll(filepath.Dir(fp), 0o755)
		if err != nil {
			return err
		}
		err = os.WriteFile(fp, []byte(templatedHosts), 0o644)
		if err != nil {
			return err
		}
		log.Info("added containerd mirror configuration", "registry", mr.String(), "path", fp)
	}
	return nil
}

func CleanupMirrorConfiguration(ctx context.Context, configPath string) error {
	log := logr.FromContextOrDiscard(ctx)

	// If backup directory does not exist it means mirrors was never configured or cleanup has already run.
	backupDirPath := path.Join(configPath, backupDir)
	ok, err := dirExists(backupDirPath)
	if err != nil {
		return err
	}
	if !ok {
		log.Info("skipping cleanup because backup directory does not exist")
		return nil
	}

	// Remove everything except _backup
	err = clearConfig(configPath)
	if err != nil {
		return err
	}

	// Move content from backup directory
	files, err := os.ReadDir(backupDirPath)
	if err != nil {
		return err
	}
	for _, fi := range files {
		oldPath := path.Join(backupDirPath, fi.Name())
		newPath := path.Join(configPath, fi.Name())
		err := os.Rename(oldPath, newPath)
		if err != nil {
			return err
		}
		log.Info("recovering containerd host configuration", "path", oldPath)
	}

	// Remove backup directory to indicate that cleanup has been run.
	err = os.RemoveAll(backupDirPath)
	if err != nil {
		return err
	}

	return nil
}

func backupConfig(log logr.Logger, configPath string) error {
	backupDirPath := path.Join(configPath, backupDir)
	ok, err := dirExists(backupDirPath)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	files, err := os.ReadDir(configPath)
	if err != nil {
		return err
	}
	err = os.MkdirAll(backupDirPath, 0o755)
	if err != nil {
		return err
	}
	for _, fi := range files {
		oldPath := path.Join(configPath, fi.Name())
		newPath := path.Join(backupDirPath, fi.Name())
		err := os.Rename(oldPath, newPath)
		if err != nil {
			return err
		}
		log.Info("backing up containerd host configuration", "path", oldPath)
	}
	return nil
}

func clearConfig(configPath string) error {
	files, err := os.ReadDir(configPath)
	if err != nil {
		return err
	}
	for _, fi := range files {
		if fi.Name() == backupDir {
			continue
		}
		filePath := path.Join(configPath, fi.Name())
		err := os.RemoveAll(filePath)
		if err != nil {
			return err
		}
	}
	return nil
}

func templateHosts(parsedMirrorRegistry url.URL, parsedMirrorTargets []url.URL, capabilities []string, userinfo *url.Userinfo) (string, error) {
	server := parsedMirrorRegistry.String()
	if parsedMirrorRegistry.String() == "https://docker.io" {
		server = "https://registry-1.docker.io"
	}
	if parsedMirrorRegistry == oci.WildcardRegistryURL {
		server = ""
	}
	authorization := ""
	if userinfo != nil {
		authorization = httpx.UserinfoHeaderValue(*userinfo)
	}

	hc := struct {
		Authorization string
		Server        string
		Capabilities  string
		MirrorTargets []url.URL
	}{
		Server:        server,
		Capabilities:  fmt.Sprintf("['%s']", strings.Join(capabilities, "', '")),
		MirrorTargets: parsedMirrorTargets,
		Authorization: authorization,
	}
	tmpl, err := template.New("").Parse(`{{- with .Server }}server = '{{ . }}'{{ end }}
{{- $authorization := .Authorization }}
{{ range .MirrorTargets }}
[host.'{{ .String }}']
capabilities = {{ $.Capabilities }}
dial_timeout = '200ms'
{{- if $authorization }}
[host.'{{ .String }}'.header]
Authorization = '{{ $authorization }}'
{{- end }}
{{ end }}`)
	if err != nil {
		return "", err
	}
	buf := bytes.NewBuffer(nil)
	err = tmpl.Execute(buf, hc)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func existingHosts(configPath string, parsedMirrorRegistry url.URL) (string, error) {
	fp := path.Join(configPath, backupDir, parsedMirrorRegistry.Host, "hosts.toml")
	b, err := os.ReadFile(fp)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	type hostFile struct {
		Hosts map[string]any `toml:"host"`
	}

	var hf hostFile
	err = toml.Unmarshal(b, &hf)
	if err != nil {
		return "", err
	}
	if len(hf.Hosts) == 0 {
		return "", nil
	}

	hosts := []string{}
	parser := tomlu.Parser{}
	parser.Reset(b)
	for parser.NextExpression() {
		err := parser.Error()
		if err != nil {
			return "", err
		}
		e := parser.Expression()
		if e.Kind != tomlu.Table {
			continue
		}
		ki := e.Key()
		if ki.Next() && string(ki.Node().Data) == "host" && ki.Next() && ki.IsLast() {
			hosts = append(hosts, string(ki.Node().Data))
		}
	}

	ehs := []string{}
	for _, h := range hosts {
		data := hostFile{
			Hosts: map[string]any{
				h: hf.Hosts[h],
			},
		}
		b, err := toml.Marshal(data)
		if err != nil {
			return "", err
		}
		eh := strings.TrimPrefix(string(b), "[host]\n")
		ehs = append(ehs, eh)
	}
	return strings.TrimSpace(strings.Join(ehs, "\n")), nil
}

func dirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.IsDir(), nil
}
