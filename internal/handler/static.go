package handler

import (
	"net/http"
	"os"
)

// StaticFiles serves the files in dir, but no directory listings (pentest
// F-09): a directory answers 404, like a file that is not there.
//
// Browsers must ask again before reusing a file: the links carry no version,
// and without Cache-Control a browser guesses how long a file stays fresh
// from its age, days for a stylesheet untouched for weeks. A release that
// adds to theme.css would meet pages still on the old one. Asking costs a
// 304 when nothing changed.
func StaticFiles(dir string) http.Handler {
	files := http.FileServer(noDirectories{http.Dir(dir)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
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
