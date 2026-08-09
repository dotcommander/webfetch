package webfetch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	urlCacheVersion       = 2
	urlCacheFileOverhead  = 256 << 10
	urlCacheDirectoryMode = 0o700
	urlCacheFileMode      = 0o600
)

type urlCache struct {
	dir      string
	ttl      time.Duration
	maxBytes int64
}

type urlCacheEnvelope struct {
	Version             int       `json:"version"`
	StoredAt            time.Time `json:"stored_at"`
	URL                 string    `json:"url"`
	Reader              string    `json:"reader"`
	Endpoint            string    `json:"endpoint"`
	Render              string    `json:"render"`
	RenderWait          string    `json:"render_wait"`
	RenderPolicyVersion int       `json:"render_policy_version"`
	Document            Document  `json:"document"`
}

func newURLCache(ttl time.Duration, dir string, maxBodyBytes int64) *urlCache {
	if ttl <= 0 {
		return nil
	}
	if strings.TrimSpace(dir) == "" {
		var err error
		dir, err = defaultURLCacheDir()
		if err != nil {
			return nil
		}
	}
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	return &urlCache{
		dir:      dir,
		ttl:      ttl,
		maxBytes: maxBodyBytes + urlCacheFileOverhead,
	}
}

func defaultURLCacheDir() (string, error) {
	if cacheHome := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); cacheHome != "" {
		return filepath.Join(cacheHome, "webfetch", "urls"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "webfetch", "urls"), nil
}

func (c *urlCache) load(rawURL, readerMode, endpoint string, renderArgs ...string) (Document, bool) {
	if c == nil || c.ttl <= 0 {
		return Document{}, false
	}
	renderMode, renderWait := cacheRenderIdentity(renderArgs)
	path := filepath.Join(c.dir, cacheFileName(rawURL, readerMode, endpoint, renderMode, renderWait))
	file, err := os.Open(path)
	if err != nil {
		return Document{}, false
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, c.maxBytes+1))
	if err != nil || int64(len(data)) > c.maxBytes {
		return Document{}, false
	}
	var envelope urlCacheEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Document{}, false
	}
	if envelope.Version != urlCacheVersion || envelope.URL != rawURL || envelope.Reader != readerMode || envelope.Endpoint != endpoint || envelope.Render != renderMode || envelope.RenderWait != renderWait || envelope.RenderPolicyVersion != renderPolicyVersion {
		return Document{}, false
	}
	if envelope.StoredAt.IsZero() || envelope.Document.Content == "" {
		return Document{}, false
	}
	age := time.Since(envelope.StoredAt)
	if age < 0 || age > c.ttl {
		return Document{}, false
	}
	return envelope.Document, true
}

func (c *urlCache) store(rawURL, readerMode, endpoint string, doc Document, renderArgs ...string) {
	if c == nil || c.ttl <= 0 || doc.Content == "" {
		return
	}
	renderMode, renderWait := cacheRenderIdentity(renderArgs)
	data, err := json.Marshal(urlCacheEnvelope{
		Version:             urlCacheVersion,
		StoredAt:            time.Now().UTC(),
		URL:                 rawURL,
		Reader:              readerMode,
		Endpoint:            endpoint,
		Render:              renderMode,
		RenderWait:          renderWait,
		RenderPolicyVersion: renderPolicyVersion,
		Document:            doc,
	})
	if err != nil || int64(len(data)) > c.maxBytes {
		return
	}
	if err := os.MkdirAll(c.dir, urlCacheDirectoryMode); err != nil {
		return
	}

	tmp, err := os.CreateTemp(c.dir, ".webfetch-cache-*")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Chmod(tmpPath, urlCacheFileMode); err != nil {
		return
	}
	if err := os.Rename(tmpPath, filepath.Join(c.dir, cacheFileName(rawURL, readerMode, endpoint, renderMode, renderWait))); err != nil {
		return
	}
	keep = true
}

func cacheRenderIdentity(renderArgs []string) (string, string) {
	renderMode := RenderModeNever
	renderWait := RenderWaitLoad
	if len(renderArgs) > 0 && strings.TrimSpace(renderArgs[0]) != "" {
		renderMode = strings.TrimSpace(renderArgs[0])
	}
	if len(renderArgs) > 1 && strings.TrimSpace(renderArgs[1]) != "" {
		renderWait = strings.TrimSpace(renderArgs[1])
	}
	return renderMode, renderWait
}

func cacheFileName(rawURL, readerMode, endpoint string, renderArgs ...string) string {
	renderMode, renderWait := cacheRenderIdentity(renderArgs)
	key := strings.Join([]string{
		strconv.Itoa(urlCacheVersion),
		rawURL,
		readerMode,
		endpoint,
		renderMode,
		renderWait,
		strconv.Itoa(renderPolicyVersion),
	}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:]) + ".json"
}
