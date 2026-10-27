package remotion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
)

// Google Fonts (D134): a project that loads fonts with @remotion/google-fonts
// would fetch them from fonts.gstatic.com while it renders, which the jail
// refuses. MetaRush downloads the files its font modules name once (outside
// the jail, from that one host, over HTTPS) and keeps them in the media
// store; the bundle serves them from loopback.

const fontsHost = "https://fonts.gstatic.com/"

var fontURL = regexp.MustCompile(`https://fonts\.gstatic\.com/([A-Za-z0-9._/-]{1,300}\.(?:woff2|woff|ttf|otf))`)

// fontsBase is where fonts are downloaded from (tests point it at a local
// server with METARUSH_FONTS_BASE; never set in normal use).
func fontsBase() string {
	if b := strings.TrimSpace(os.Getenv("METARUSH_FONTS_BASE")); b != "" {
		return strings.TrimRight(b, "/") + "/"
	}
	return fontsHost
}

// fontModule is one @remotion/google-fonts module a version imports.
type fontModule struct {
	Module string `json:"module"`
	Family string `json:"family"`
}

// ensureFonts makes sure the media store has every file the version's font
// modules name, reusing what an earlier version already has.
func ensureFonts(ctx context.Context, a *app.App, env string, mods []fontModule, prev []models.RemotionFont, say func(string)) ([]models.RemotionFont, error) {
	if len(mods) == 0 {
		return nil, nil
	}
	have := map[string]models.RemotionFontFile{}
	for _, f := range prev {
		for _, x := range f.Files {
			have[x.Path] = x
		}
	}
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var out []models.RemotionFont
	for _, m := range mods {
		if !regexp.MustCompile(`^[A-Za-z0-9]{1,80}$`).MatchString(m.Family) {
			continue
		}
		src, err := os.ReadFile(filepath.Join(env, "node_modules", "@remotion", "google-fonts", "dist", "esm", m.Family+".mjs"))
		if err != nil {
			return nil, apperr.New(apperr.Validation, "The project loads the Google Font “%s”, which its version of Remotion's font package doesn't have.", m.Family).
				WithHint("Check the font's name in the project's code.")
		}
		paths := map[string]bool{}
		for _, mm := range fontURL.FindAllSubmatch(src, -1) {
			paths[string(mm[1])] = true
		}
		var list []string
		for p := range paths {
			list = append(list, p)
		}
		sort.Strings(list)
		font := models.RemotionFont{Family: m.Family, Module: m.Module}
		for i, p := range list {
			if f, ok := have[p]; ok {
				font.Files = append(font.Files, f)
				continue
			}
			if i == 0 {
				say("getting the Google Font " + m.Family)
			}
			f, err := fetchFont(ctx, a, client, p)
			if err != nil {
				return nil, err
			}
			have[p] = f
			font.Files = append(font.Files, f)
		}
		out = append(out, font)
	}
	return out, nil
}

// fetchFont downloads one font file and keeps it in the media store.
func fetchFont(ctx context.Context, a *app.App, client *http.Client, p string) (models.RemotionFontFile, error) {
	if strings.Contains(p, "..") {
		return models.RemotionFontFile{}, fmt.Errorf("an unusual font address")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fontsBase()+p, nil)
	if err != nil {
		return models.RemotionFontFile{}, err
	}
	req.Header.Set("User-Agent", "MetaRush (fonts for a Remotion project)")
	resp, err := client.Do(req)
	if err != nil {
		return models.RemotionFontFile{}, apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't download a Google Font the project uses.").WithHint("Check the internet connection and try again.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return models.RemotionFontFile{}, apperr.New(apperr.ToolFailed, "MetaRush couldn't download a Google Font the project uses (the font server said %d).", resp.StatusCode).WithHint("Try again in a moment.")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if err != nil {
		return models.RemotionFontFile{}, err
	}
	if len(data) > 8<<20 || len(data) < 4 || !(bytes.HasPrefix(data, []byte("wOF2")) || bytes.HasPrefix(data, []byte("wOFF")) || bytes.HasPrefix(data, []byte{0, 1, 0, 0}) || bytes.HasPrefix(data, []byte("OTTO"))) {
		return models.RemotionFontFile{}, apperr.New(apperr.ToolFailed, "A Google Font file the project uses didn't download as a font.").WithHint("Try again in a moment.")
	}
	obj, err := putBytes(ctx, a, data, "font/woff2")
	if err != nil {
		return models.RemotionFontFile{}, err
	}
	return models.RemotionFontFile{Path: p, SHA256: obj.SHA256, Size: obj.Size}, nil
}

// fontFiles maps each font path to a local copy (for the bundle).
func fontFiles(ctx context.Context, a *app.App, fonts []models.RemotionFont) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range fonts {
		for _, x := range f.Files {
			p, err := localObject(ctx, a, x.SHA256, x.Size)
			if err != nil {
				return nil, err
			}
			out[x.Path] = p
		}
	}
	return out, nil
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
