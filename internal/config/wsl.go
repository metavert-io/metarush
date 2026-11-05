package config

import "github.com/metavert-io/metarush/internal/wsl"

// WindowsPlaces are the folders this installation uses that could be on the
// Windows side inside WSL (D521): its own folder, the storage folder, the
// watched folder and the linked folders kept in step here.
func (c *Config) WindowsPlaces() []wsl.Place {
	if c == nil {
		return nil
	}
	out := []wsl.Place{{What: wsl.PlaceHome, Path: c.Home()}}
	if c.Media.Provider == "" || c.Media.Provider == "filesystem" {
		out = append(out, wsl.Place{What: wsl.PlaceStorage, Path: c.Media.Filesystem.Root})
	}
	if w := c.Worker.Watch; w.Enabled && w.Dir != "" {
		out = append(out, wsl.Place{What: wsl.PlaceWatched, Path: w.Dir})
	}
	for _, f := range c.Resources.Folders {
		if !f.Off {
			out = append(out, wsl.Place{What: wsl.PlaceLinked, Path: f.Path})
		}
	}
	return out
}
