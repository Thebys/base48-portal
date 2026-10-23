package handler

import (
	"net/http"
	"os"
)

// StaticFiles serves the files in dir, but no directory listings (pentest
// F-09): a directory answers 404, like a file that is not there.
func StaticFiles(dir string) http.Handler {
	return http.FileServer(noDirectories{http.Dir(dir)})
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
