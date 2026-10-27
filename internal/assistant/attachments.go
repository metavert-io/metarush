package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif" // picture sizes
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	_ "golang.org/x/image/webp"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Attachments (D166, docs/keeping.md): the panel's message box takes pictures
// and PDFs (dropped, pasted or picked with the paperclip). Each is stored in
// the media store by its SHA-256 (assistant.attach), goes with the message
// (AssistantMessageIn.Attachments), is copied into the conversation's
// proposals folder on the machine that runs Claude so Claude can Read it, and
// becomes a "Keep this?" card at the end of the turn. Nothing is added to a
// project until the person keeps it.

const (
	maxAttachBytes = 20 << 20
	maxAttachments = 10
)

// AttachIn is one file the person attaches (before sending the message).
type AttachIn struct {
	Name string `json:"name" jsonschema:"minLength=1,maxLength=200,description=The file's name."`
	// Data is the file, base64-encoded (20 MB at most).
	Data string `json:"data" jsonschema:"description=The file\\, base64-encoded."`
}

// AttachOut is the stored file, to send with the message.
type AttachOut struct {
	File    models.KeepFile `json:"file"`
	Summary string          `json:"summary"`
}

// attachKind sniffs what a file is: a picture, an SVG or a PDF ("" otherwise).
func attachKind(b []byte) string {
	head := b
	if len(head) > 512 {
		head = head[:512]
	}
	switch ct := http.DetectContentType(head); ct {
	case "image/png", "image/jpeg", "image/gif", "application/pdf":
		return ct
	}
	if len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return "image/webp"
	}
	t := strings.ToLower(strings.TrimSpace(string(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")))))
	if strings.HasPrefix(t, "<svg") || (strings.HasPrefix(t, "<?xml") && strings.Contains(t, "<svg")) || (strings.HasPrefix(t, "<!doctype svg")) {
		return "image/svg+xml"
	}
	return ""
}

// cleanFileName keeps a file's name readable and harmless.
func cleanFileName(n string) string {
	n = filepath.Base(strings.ReplaceAll(strings.TrimSpace(n), "\\", "/"))
	var b strings.Builder
	for _, r := range n {
		if r < 0x20 || r == 0x7f || r == '/' || r == ':' {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		out = "attachment"
	}
	for utf8.RuneCountInString(out) > 120 {
		out = string([]rune(out)[:120])
	}
	return out
}

// Attach stores a file the person attaches, by its content.
func (m *Manager) Attach(ctx context.Context, in AttachIn) (*AttachOut, error) {
	name := cleanFileName(in.Name)
	if len(in.Data) > base64.StdEncoding.EncodedLen(maxAttachBytes)+8 {
		return nil, apperr.New(apperr.InvalidInput, "%s is larger than 20 MB; attach a smaller file.", name)
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.Data))
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "MetaRush couldn't read %s. Attach it again.", name)
	}
	if len(b) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "%s is empty.", name)
	}
	mime := attachKind(b)
	if mime == "" {
		return nil, apperr.New(apperr.InvalidInput, "%s isn't a picture or a PDF. A conversation takes pictures (PNG, JPEG, WebP, GIF, SVG) and PDFs.", name)
	}
	f := models.KeepFile{Name: name, Size: int64(len(b)), MIME: mime}
	if strings.HasPrefix(mime, "image/") && mime != "image/svg+xml" {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(b)); err == nil {
			f.Width, f.Height = cfg.Width, cfg.Height
		}
	}
	tmp, err := os.CreateTemp(m.App.Cfg.Scratch.Dir, "attach-*")
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't keep %s.", name)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't keep %s.", name)
	}
	tmp.Close()
	if f.SHA256, err = media.HashFile(tmp.Name()); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't keep %s.", name)
	}
	key := media.ObjectKey(m.App.WorkspaceID(), f.SHA256)
	if info, err := m.App.Media.Stat(ctx, key); err != nil || info.Size != f.Size {
		if _, err := m.App.Media.PutFile(ctx, key, tmp.Name(), media.PutOptions{ContentType: mime, SHA256: f.SHA256}, media.Resume{}, nil); err != nil {
			return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't store %s.", name)
		}
	}
	m.auditAs(ctx, "assistant.attach", "attachment", f.SHA256, "", map[string]any{"name": name, "size": f.Size, "mime": mime})
	return &AttachOut{File: f, Summary: "Attached " + theirs(name) + "."}, nil
}

// checkAttachments verifies the files a message names are stored, as described.
func (m *Manager) checkAttachments(ctx context.Context, files []models.KeepFile) ([]models.KeepFile, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if len(files) > maxAttachments {
		return nil, apperr.New(apperr.InvalidInput, "A message takes %d files at most.", maxAttachments)
	}
	out := make([]models.KeepFile, 0, len(files))
	seen := map[string]bool{}
	for _, f := range files {
		f.Name = cleanFileName(f.Name)
		if len(f.SHA256) != 64 || strings.Trim(f.SHA256, "0123456789abcdef") != "" {
			return nil, apperr.New(apperr.InvalidInput, "MetaRush can't find %s. Attach it again.", f.Name)
		}
		switch f.MIME {
		case "image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml", "application/pdf":
		default:
			return nil, apperr.New(apperr.InvalidInput, "%s isn't a picture or a PDF.", f.Name)
		}
		info, err := m.App.Media.Stat(ctx, media.ObjectKey(m.App.WorkspaceID(), f.SHA256))
		if err != nil || info.Size != f.Size || f.Size > maxAttachBytes {
			return nil, apperr.New(apperr.InvalidInput, "MetaRush can't find %s. Attach it again.", f.Name)
		}
		if !seen[f.SHA256] {
			seen[f.SHA256] = true
			out = append(out, f)
		}
	}
	return out, nil
}

// attachmentPath is where a file is copied for Claude: inside the
// conversation's proposals folder (the only place outside the working folder
// it may Read besides the frames).
func (m *Manager) attachmentPath(convID string, f models.KeepFile) string {
	ext := strings.ToLower(filepath.Ext(f.Name))
	switch f.MIME {
	case "image/png":
		ext = ".png"
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	case "image/svg+xml":
		ext = ".svg"
	case "application/pdf":
		ext = ".pdf"
	}
	return filepath.Join(m.convDir(convID), "proposals", "attachments", f.SHA256[:16]+ext)
}

// attachedLines copies a message's files where Claude can Read them and
// says so in the message (one line each).
func (m *Manager) attachedLines(ctx context.Context, convID string, files []models.KeepFile) []string {
	var out []string
	for _, f := range files {
		dst := m.attachmentPath(convID, f)
		what := attachWords(f)
		if _, err := os.Stat(dst); err != nil {
			src, err := m.App.Cache.Ensure(ctx, m.App.Media, media.ObjectKey(m.App.WorkspaceID(), f.SHA256), f.SHA256, f.Size, nil)
			if err == nil {
				err = copyFile(src, dst)
			}
			if err != nil {
				out = append(out, fmt.Sprintf("- The person attached %q (%s), but MetaRush couldn't put it where you can read it.", f.Name, what))
				continue
			}
		}
		out = append(out, fmt.Sprintf("- The person attached %q (%s): %s — Read it to look at it. MetaRush asks them whether to keep it in the project's sources (a logo also as the brand kit's), so don't offer that yourself unless they ask for something else.", f.Name, what, realPath(dst)))
	}
	return out
}

// attachWords says what a file is, for Claude.
func attachWords(f models.KeepFile) string {
	switch {
	case f.MIME == "application/pdf":
		return "a PDF, " + sizeWords(f.Size)
	case f.MIME == "image/svg+xml":
		return "an SVG picture, " + sizeWords(f.Size)
	case f.Width > 0:
		return fmt.Sprintf("a picture, %d × %d, %s", f.Width, f.Height, sizeWords(f.Size))
	}
	return "a picture, " + sizeWords(f.Size)
}

func sizeWords(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// attachmentImage is where the dashboard shows an attached picture (PDFs have none).
func (m *Manager) attachmentImage(convID string, f models.KeepFile) string {
	if !strings.HasPrefix(f.MIME, "image/") {
		return ""
	}
	return AttachmentURL(convID, f.SHA256)
}

// AttachmentURL is an attached file's same-origin address.
func AttachmentURL(convID, sha string) string {
	return "/api/assistant/conversations/" + convID + "/attachments/" + sha
}

// attachmentNamed finds a file attached in this conversation by its name or
// SHA-256 (Claude's `keep logo <name>`).
func (c *conv) attachmentNamed(ref string) *models.KeepFile {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.data.Turns) - 1; i >= 0; i-- {
		for _, f := range c.data.Turns[i].Attachments {
			if f.SHA256 == ref || strings.EqualFold(f.Name, ref) || strings.HasPrefix(f.SHA256, strings.ToLower(ref)) && len(ref) >= 12 ||
				strings.EqualFold(filepath.Base(ref), f.SHA256[:16]+filepath.Ext(ref)) {
				cp := f
				return &cp
			}
		}
	}
	return nil
}

// OpenAttachment opens a file attached to a conversation (the dashboard shows
// it): only files the conversation's messages name, read from this machine's
// cache of the media store. The caller closes the file.
func (m *Manager) OpenAttachment(ctx context.Context, convID, sha string) (*os.File, models.KeepFile, error) {
	var f models.KeepFile
	if len(sha) != 64 || strings.Trim(sha, "0123456789abcdef") != "" {
		return nil, f, apperr.New(apperr.NotFound, "MetaRush can't find that file.")
	}
	var d struct {
		Turns []struct {
			Attachments []models.KeepFile `bson:"attachments"`
		} `bson:"turns"`
		Queue []struct {
			Attachments []models.KeepFile `bson:"attachments"`
		} `bson:"queue"`
	}
	err := m.App.DB.C(store.CAssistantConversations).FindOne(ctx, bson.M{"_id": convID, "workspaceId": m.App.WorkspaceID()}).Decode(&d)
	if err != nil {
		return nil, f, apperr.New(apperr.NotFound, "MetaRush can't find that file.")
	}
	found := false
	for _, t := range d.Turns {
		for _, x := range t.Attachments {
			if x.SHA256 == sha {
				f, found = x, true
			}
		}
	}
	for _, q := range d.Queue {
		for _, x := range q.Attachments {
			if x.SHA256 == sha {
				f, found = x, true
			}
		}
	}
	if !found {
		return nil, f, apperr.New(apperr.NotFound, "MetaRush can't find that file.")
	}
	path, err := m.App.Cache.Ensure(ctx, m.App.Media, media.ObjectKey(m.App.WorkspaceID(), sha), sha, f.Size, nil)
	if err != nil {
		return nil, f, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get that file from its storage.")
	}
	fh, err := os.Open(path)
	if err != nil {
		return nil, f, apperr.New(apperr.NotFound, "MetaRush can't find that file.")
	}
	return fh, f, nil
}

func init() {
	// The file comes written out as text (base64), so the request is about a third larger than it.
	ops.Def("assistant.attach", "Attach a picture or a PDF to the next message of a conversation (stored by its content; MetaRush asks whether to keep it in the project)",
		ops.Flags{Mutating: true, HumanOnly: true, MaxInput: int64(base64.StdEncoding.EncodedLen(maxAttachBytes)) + 64<<10},
		func(ctx context.Context, a *app.App, in *AttachIn) (*AttachOut, error) {
			return For(a).Attach(AsPerson(ctx, a), *in)
		})
}
