package ui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed assets/*
var assetFS embed.FS

var (
	assets     = mustAssetFS()
	indexHTML  = mustAsset("index.html")
	assetETags = buildAssetETags()
	htmlHeader = map[string]string{
		"cache-control":          noStore,
		"x-content-type-options": "nosniff",
		"x-frame-options":        "DENY",
		"referrer-policy":        "no-referrer",
		"content-security-policy": "default-src 'self'; img-src 'self' data:; " +
			"style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; " +
			"frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
	}
)

func mustAssetFS() fs.FS {
	sub, err := fs.Sub(assetFS, "assets")
	if err != nil {
		panic("llm-proxy: embedded Web UI assets unavailable: " + err.Error())
	}
	return sub
}

func mustAsset(name string) []byte {
	data, err := fs.ReadFile(assets, name)
	if err != nil {
		panic("llm-proxy: embedded Web UI asset missing: " + name)
	}
	return data
}

// buildAssetETags fingerprints the embedded bundle once at startup. Without a
// validator the browser has to re-download every asset each time the console
// loads, because embed.FS files carry no modification time.
func buildAssetETags() map[string]string {
	tags := make(map[string]string)
	err := fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, readErr := fs.ReadFile(assets, name)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		tags[name] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	if err != nil {
		panic("llm-proxy: embedded Web UI assets unreadable: " + err.Error())
	}
	return tags
}

func assetHandler() http.Handler {
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag, ok := assetETags[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			w.Header().Set("etag", tag)
		}
		w.Header().Set("x-content-type-options", "nosniff")
		w.Header().Set("cache-control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

func (h *Handler) handlePage(w http.ResponseWriter, r *http.Request) {
	for key, value := range htmlHeader {
		w.Header().Set(key, value)
	}
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(indexHTML)
}
