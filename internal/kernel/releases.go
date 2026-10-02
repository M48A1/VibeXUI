// Package kernel retrieves verified binaries exclusively from XTLS/Xray-core.
package kernel

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var tagPattern = regexp.MustCompile(`^v[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$`)
var hashPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{64}\b`)

func ValidVersion(v string) bool { return tagPattern.MatchString(v) }
func AssetName(arch string) string {
	switch arch {
	case "amd64":
		return "Xray-linux-64.zip"
	case "arm64":
		return "Xray-linux-arm64-v8a.zip"
	}
	return ""
}

type Release struct {
	Version    string `json:"version"`
	Prerelease bool   `json:"prerelease"`
	Published  string `json:"published"`
}
type asset struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}
type githubRelease struct {
	Tag        string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	Published  string  `json:"published_at"`
	Assets     []asset `json:"assets"`
}
type Catalog struct {
	Client   *http.Client
	mu       sync.Mutex
	cached   []Release
	cachedAt time.Time
}

func NewCatalog() *Catalog {
	return &Catalog{Client: &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || r.URL.Scheme != "https" || r.URL.User != nil {
			return errors.New("不允许的下载重定向")
		}
		host := r.URL.Hostname()
		if host != "github.com" && host != "api.github.com" && !strings.HasSuffix(host, ".githubusercontent.com") {
			return errors.New("下载重定向不是 GitHub 官方地址")
		}
		return nil
	}}}
}
func (c *Catalog) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "VibeXUI")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, errors.New("无法连接 GitHub，请检查服务器网络")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub 返回 HTTP %d（可能触发限流或版本不存在）", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(b)) > limit {
		return nil, errors.New("下载文件超过大小限制")
	}
	return b, err
}
func (c *Catalog) List(ctx context.Context) ([]Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.cachedAt) < 5*time.Minute {
		return append([]Release(nil), c.cached...), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	raw, err := c.get(ctx, "https://api.github.com/repos/XTLS/Xray-core/releases?per_page=30", 4<<20)
	if err != nil {
		return nil, err
	}
	var releases []githubRelease
	if err = json.Unmarshal(raw, &releases); err != nil {
		return nil, errors.New("无法解析版本列表")
	}
	out := []Release{}
	for _, r := range releases {
		if !r.Draft && ValidVersion(r.Tag) {
			out = append(out, Release{r.Tag, r.Prerelease, r.Published})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("官方版本列表为空，请稍后重试")
	}
	c.cached = out
	c.cachedAt = time.Now()
	return append([]Release(nil), out...), nil
}
func (c *Catalog) metadata(ctx context.Context, version, arch string) (string, error) {
	if !ValidVersion(version) || AssetName(arch) == "" {
		return "", errors.New("版本或架构无效")
	}
	raw, err := c.get(ctx, "https://api.github.com/repos/XTLS/Xray-core/releases/tags/"+version, 1<<20)
	if err != nil {
		return "", err
	}
	var r githubRelease
	if json.Unmarshal(raw, &r) != nil || r.Draft || r.Tag != version {
		return "", errors.New("官方版本信息无效")
	}
	name := AssetName(arch)
	found := false
	digest := ""
	hasChecksum := false
	for _, a := range r.Assets {
		if a.Name == name {
			found = true
			digest = a.Digest
		}
		if a.Name == name+".dgst" {
			hasChecksum = true
		}
	}
	if !found {
		return "", errors.New("该版本没有目标架构的 Linux 内核")
	}
	if strings.HasPrefix(digest, "sha256:") {
		h := strings.TrimPrefix(digest, "sha256:")
		if len(h) == 64 && hashPattern.MatchString(h) {
			return strings.ToLower(h), nil
		}
	}
	if !hasChecksum {
		return "", errors.New("官方版本缺少 SHA256 校验值，拒绝安装")
	}
	raw, err = c.get(ctx, "https://github.com/XTLS/Xray-core/releases/download/"+version+"/"+name+".dgst", 16384)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "SHA256") || strings.Contains(upper, "SHA2-256") {
			if h := hashPattern.FindString(line); h != "" {
				return strings.ToLower(h), nil
			}
		}
	}
	return "", errors.New("无法解析官方 SHA256 校验值")
}
func (c *Catalog) Check(ctx context.Context, version, arch string) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	_, err := c.metadata(ctx, version, arch)
	return err
}

// Download stages a verified binary in the Agent's writable directory, without
// touching the currently running binary. The caller owns the returned directory.
func (c *Catalog) Download(ctx context.Context, version, arch, directory string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	expected, err := c.metadata(ctx, version, arch)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(directory, "kernels")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(dir, "kernel-")
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(work)
		}
	}()
	// A bounded archive prevents unbounded memory or disk consumption.
	archive := filepath.Join(work, "download.zip")
	req, err := http.NewRequestWithContext(ctx, "GET", "https://github.com/XTLS/Xray-core/releases/download/"+version+"/"+AssetName(arch), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "VibeXUI")
	resp, err := c.Client.Do(req)
	if err != nil {
		return "", errors.New("内核下载失败，请检查网络")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("内核下载返回 HTTP %d", resp.StatusCode)
	}
	output, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(resp.Body, (128<<20)+1))
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || written > 128<<20 {
		return "", errors.New("内核下载不完整或超过大小限制")
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return "", errors.New("内核下载 SHA256 校验失败")
	}
	z, err := zip.OpenReader(archive)
	if err != nil {
		return "", errors.New("内核压缩包无效")
	}
	defer z.Close()
	var executable *zip.File
	for _, f := range z.File {
		if f.Name == "xray" {
			if executable != nil {
				return "", errors.New("内核压缩包含重复文件")
			}
			executable = f
		}
	}
	if executable == nil || !executable.Mode().IsRegular() || executable.UncompressedSize64 > 256<<20 {
		return "", errors.New("压缩包缺少有效的 xray 可执行文件")
	}
	src, err := executable.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()
	path := filepath.Join(work, "xray")
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return "", err
	}
	n, copyErr := io.Copy(dst, io.LimitReader(src, (256<<20)+1))
	syncErr = dst.Sync()
	closeErr = dst.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || n > 256<<20 {
		return "", errors.New("写入内核文件失败")
	}
	if err = z.Close(); err != nil {
		return "", err
	}
	_ = os.Remove(archive)
	keep = true
	return path, nil
}
