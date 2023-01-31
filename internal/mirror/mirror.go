package mirror

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"path"

	"github.com/go-logr/logr"
	"github.com/spf13/afero"
	"github.com/xenitab/spegel/internal/registry"
	"go.uber.org/multierr"
)

// AddMirrorConfiguration sets up registry configuration to direct pulls through mirror.
// Refer to containerd registry configuration documentation for mor information about required configuration.
// https://github.com/containerd/containerd/blob/main/docs/cri/config.md#registry-configuration
// https://github.com/containerd/containerd/blob/main/docs/hosts.md#registry-configuration---examples
func AddMirrorConfiguration(ctx context.Context, fs afero.Fs, configPath, addr string, registryURLs []url.URL) error {
	if err := validate(registryURLs); err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	mirrorURL, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%s", port))
	if err != nil {
		return err
	}
	for _, registryURL := range registryURLs {
		content := hostsFileContent(registryURL, *mirrorURL)
		fp := path.Join(configPath, registryURL.Host, "hosts.toml")
		err := fs.MkdirAll(path.Dir(fp), 0755)
		if err != nil {
			return err
		}
		err = afero.WriteFile(fs, fp, []byte(content), 0644)
		if err != nil {
			return err
		}
		logr.FromContextOrDiscard(ctx).Info("added containerd mirror configuration", "registry", registryURL.String(), "path", fp)
	}
	return nil
}

// RemoveMirrorConfiguration removes all mirror configuration for all registries passed in the list.
func RemoveMirrorConfiguration(ctx context.Context, fs afero.Fs, configPath string, registryURLs []url.URL) error {
	errs := []error{}
	for _, registryURL := range registryURLs {
		dp := path.Join(configPath, registryURL.Host)
		err := fs.RemoveAll(dp)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		logr.FromContextOrDiscard(ctx).Info("removed containerd mirror configuration", "registry", registryURL.String(), "path", dp)
	}
	return multierr.Combine(errs...)
}

func hostsFileContent(registryURL url.URL, mirrorURL url.URL) string {
	server := registryURL.String()
	if isDockerHub(registryURL) {
		server = "https://registry-1.docker.io"
	}
	content := fmt.Sprintf(`server = "%[1]s"

[host."%[3]s"]
  capabilities = ["pull", "resolve"]
[host."%[3]s".header]
  %[4]s = ["%[2]s"]
  %[5]s = ["true"]`, server, registryURL.String(), mirrorURL.String(), registry.RegistryHeader, registry.MirrorHeader)
	return content
}

func isDockerHub(registryURL url.URL) bool {
	return registryURL.String() == "https://docker.io"
}

func validate(urls []url.URL) error {
	for _, u := range urls {
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("invalid registry url scheme must be http or https")
		}
		if u.Path != "" {
			return fmt.Errorf("invalid registry url path has to be empty")
		}
		if len(u.Query()) != 0 {
			return fmt.Errorf("invalid registry url query has to be empty")
		}
		if u.User != nil {
			return fmt.Errorf("invalid registry url user has to be empty")
		}
	}
	return nil
}
