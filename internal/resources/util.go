package resources

import (
	"io"
	"os"

	"github.com/metavert-io/metarush/internal/app"
)

// readSmall reads a file of at most 32 MB (SVGs).
func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 32<<20))
}

func makeTemp(a *app.App) (string, error) {
	if err := os.MkdirAll(a.Cfg.Scratch.Dir, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(a.Cfg.Scratch.Dir, "resource-")
}

func removeAll(dir string) { _ = os.RemoveAll(dir) }
