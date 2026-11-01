package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"
)

// StaticFiles serves the files in dir, but no directory listings (pentest
// F-09): a directory answers 404, like a file that is not there.
//
// A link from assetURL carries a fingerprint of the file, so browsers may
// keep it for a year without asking: a new release changes the address. Any
// other request (an image, a link without one) is kept but asked about
// first. Without Cache-Control at all a browser guesses how long a file
// stays fresh from its age, days for a stylesheet untouched for weeks, and a
// release met pages still on the old theme.css.
func StaticFiles(dir string) http.Handler {
	files := http.FileServer(noDirectories{http.Dir(dir)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

type noDirectories struct{ http.FileSystem }

func (fs noDirectories) Open(name string) (http.File, error) {
	f, err := fs.FileSystem.Open(name)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err != nil || info.IsDir() {
		f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}

// assets remembers the fingerprints of static files. A file is read again
// only when its size or time changes, which in development is on every save.
type assets struct {
	mu   sync.Mutex
	seen map[string]asset
}

type asset struct {
	size    int64
	modTime time.Time
	sum     string
}

// assetURL is the address of a file under web/static with a fingerprint of
// its content, /static/css/theme.css?v=1a2b3c4d5e6f. Templates call it as
// {{asset "css/theme.css"}}; a file that is not there fails the page.
func (h *Handler) assetURL(name string) (string, error) {
	file := filepath.Join(h.webRoot, "static", filepath.FromSlash(path.Clean("/"+name)))
	info, err := os.Stat(file)
	if err != nil {
		return "", fmt.Errorf("asset %s: %w", name, err)
	}

	h.assets.mu.Lock()
	defer h.assets.mu.Unlock()
	if a, ok := h.assets.seen[name]; ok && a.size == info.Size() && a.modTime.Equal(info.ModTime()) {
		return "/static/" + name + "?v=" + a.sum, nil
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("asset %s: %w", name, err)
	}
	sum := sha256.Sum256(content)
	a := asset{size: info.Size(), modTime: info.ModTime(), sum: hex.EncodeToString(sum[:6])}
	if h.assets.seen == nil {
		h.assets.seen = make(map[string]asset)
	}
	h.assets.seen[name] = a
	return "/static/" + name + "?v=" + a.sum, nil
}
