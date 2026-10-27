package wsarchive

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// packTar writes an archive folder as one tar file (0600): manifest.json
// first, then media.jsonl, the collections and the media. It returns the
// file's size. The file appears at dest only once it is complete.
func packTar(dir, dest string, progress func(done, all int64)) (int64, error) {
	var files []string
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s isn't a plain file", p)
		}
		rel, _ := filepath.Rel(dir, p)
		files = append(files, filepath.ToSlash(rel))
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	rank := func(p string) int {
		switch {
		case p == ManifestName:
			return 0
		case p == MediaIndexName:
			return 1
		case strings.HasPrefix(p, CollectionsDir+"/"):
			return 2
		}
		return 3
	}
	sort.Slice(files, func(i, j int) bool {
		if ri, rj := rank(files[i]), rank(files[j]); ri != rj {
			return ri < rj
		}
		return files[i] < files[j]
	})
	tmp := dest + ".writing"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	fail := func(err error) (int64, error) {
		out.Close()
		os.Remove(tmp) // this export's own unfinished file
		return 0, err
	}
	tw := tar.NewWriter(out)
	var done int64
	dirs := map[string]bool{}
	for _, rel := range files {
		// Folders first, so an unpacking keeps them private too.
		for d := filepath.ToSlash(filepath.Dir(rel)); d != "." && !dirs[d]; d = filepath.ToSlash(filepath.Dir(d)) {
			dirs[d] = true
		}
	}
	var dirList []string
	for d := range dirs {
		dirList = append(dirList, d)
	}
	sort.Strings(dirList)
	for _, d := range dirList {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d + "/", Mode: 0o700, Format: tar.FormatPAX}); err != nil {
			return fail(err)
		}
	}
	for _, rel := range files {
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return fail(err)
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return fail(err)
		}
		// No owner names: an archive says nothing about this computer's accounts.
		hdr := &tar.Header{Typeflag: tar.TypeReg, Name: rel, Mode: 0o600, Size: st.Size(), ModTime: st.ModTime().UTC(), Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			f.Close()
			return fail(err)
		}
		n, err := io.Copy(tw, f)
		f.Close()
		if err != nil {
			return fail(err)
		}
		done += n
		if progress != nil {
			progress(done, total)
		}
	}
	if err := tw.Close(); err != nil {
		return fail(err)
	}
	if err := out.Sync(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	st, err := os.Stat(tmp)
	if err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// unpackTar unpacks a tar archive into an empty private folder: only plain
// files and folders with plain relative names (no links, nothing outside the
// folder, nothing twice).
func unpackTar(path, dir string, progress func(done, all int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var all int64
	if st, err := f.Stat(); err == nil {
		all = st.Size()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tr := tar.NewReader(f)
	seen := map[string]bool{}
	var done int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("the archive can't be read: %w", err)
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if !safeRel(name) {
			return fmt.Errorf("the archive holds a file outside itself: %q", hdr.Name)
		}
		if seen[name] {
			return fmt.Errorf("the archive holds %q twice", name)
		}
		seen[name] = true
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			n, err := io.Copy(out, tr)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			done += n
			if progress != nil {
				progress(done, all)
			}
		default:
			return fmt.Errorf("the archive holds something that isn't a plain file: %q", hdr.Name)
		}
	}
}
