package release

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const defaultAPI = "https://api.github.com/repos/actions/runner/releases"

type Artifact struct {
	Version string
	URL     string
	SHA256  string
}

type Resolver struct {
	Client  *http.Client
	APIBase string
	Token   string
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Digest             string `json:"digest"`
	} `json:"assets"`
}

func (r Resolver) Resolve(ctx context.Context, version, arch string) (Artifact, error) {
	base := strings.TrimRight(r.APIBase, "/")
	if base == "" {
		base = defaultAPI
	}
	endpoint := base + "/latest"
	if version != "latest" {
		endpoint = base + "/tags/v" + strings.TrimPrefix(version, "v")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Artifact{}, fmt.Errorf("create runner release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Artifact{}, fmt.Errorf("resolve Actions runner release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Artifact{}, fmt.Errorf("resolve Actions runner release: GitHub returned %s", resp.Status)
	}
	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return Artifact{}, fmt.Errorf("decode Actions runner release: %w", err)
	}
	resolvedVersion := strings.TrimPrefix(release.TagName, "v")
	wanted := fmt.Sprintf("actions-runner-linux-%s-%s.tar.gz", arch, resolvedVersion)
	for _, asset := range release.Assets {
		if asset.Name != wanted {
			continue
		}
		digest := strings.TrimPrefix(asset.Digest, "sha256:")
		if digest == "" {
			return Artifact{}, errors.New("Actions runner release asset does not publish a SHA-256 digest")
		}
		if decoded, err := hex.DecodeString(digest); err != nil || len(decoded) != 32 {
			return Artifact{}, errors.New("Actions runner release asset publishes an invalid SHA-256 digest")
		}
		return Artifact{Version: resolvedVersion, URL: asset.BrowserDownloadURL, SHA256: digest}, nil
	}
	return Artifact{}, fmt.Errorf("Actions runner release has no asset named %q", wanted)
}
