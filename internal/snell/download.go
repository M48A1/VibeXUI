// Package snell manages the official standalone Snell server independently of Xray.
package snell

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const Version = "5.0.1"

func Supported() bool {
	return runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64")
}
func install(ctx context.Context, dir string) (string, error) {
	if !Supported() {
		return "", fmt.Errorf("Snell 仅支持 Linux amd64/arm64")
	}
	path := filepath.Join(dir, "snell-server-v"+Version)
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
		return path, nil
	}
	arch, hash := "amd64", "9bea1c2b9e35b73b31634856c04d18c393072b9e5dcde6a32781d8b8f908c539"
	if runtime.GOARCH == "arm64" {
		arch = "aarch64"
		hash = "2f178bf5ac468ce1a130454efa40a0603fbbe4e47ecc4880a989f4abc7f824cf"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://dl.nssurge.com/snell/snell-server-v"+Version+"-linux-"+arch+".zip", nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Snell 官方下载失败")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("Snell 下载返回 HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return "", err
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != hash {
		return "", fmt.Errorf("Snell 安装包 SHA256 不匹配")
	}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", err
	}
	for _, f := range archive.File {
		if f.Name != "snell-server" {
			continue
		}
		if f.UncompressedSize64 > 32<<20 {
			return "", fmt.Errorf("Snell 文件过大")
		}
		src, err := f.Open()
		if err != nil {
			return "", err
		}
		data, err := io.ReadAll(io.LimitReader(src, 32<<20))
		src.Close()
		if err != nil {
			return "", err
		}
		if err = writeFile(path, data, 0700); err != nil {
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("安装包缺少 Snell 服务端")
}
func writeFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".snell-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
