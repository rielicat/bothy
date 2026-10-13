package channel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const defaultUploadMaxMB = 20

// fetchAttachments downloads a message's attachments into the principal's
// workspace under .bothy/in/<msgid>/ and returns them as image/file parts,
// plus notes for the ones it skipped.
func (h *Hub) fetchAttachments(ctx context.Context, a *adapter, in core.Inbound, p *core.Principal) ([]core.Part, []string) {
	if len(in.Attachments) == 0 {
		return nil, nil
	}
	maxMB := h.cfg().Server.UploadMaxMB
	if maxMB <= 0 {
		maxMB = defaultUploadMaxMB
	}
	limit := int64(maxMB) << 20
	root, dir, err := h.inbox(p, in.MsgID)
	if err != nil {
		h.log.Error("channel: preparing attachment folder failed", "principal", p.ID, "err", err)
		return nil, []string{"attachments could not be saved"}
	}
	defer root.Close()

	var parts []core.Part
	var notes []string
	used := map[string]bool{}
	for _, att := range in.Attachments {
		label := att.Name
		if label == "" {
			label = "attachment"
		}
		if att.Size > limit {
			notes = append(notes, fmt.Sprintf("%s skipped: %d MB is over the %d MB limit", label, (att.Size+(1<<20)-1)>>20, maxMB))
			continue
		}
		name := uniqueName(sanitizeName(att.Name, att.MIME), used)
		n, err := h.saveAttachment(ctx, a, att, root, path.Join(dir, name), limit)
		switch {
		case errors.Is(err, errTooLarge):
			notes = append(notes, fmt.Sprintf("%s skipped: over the %d MB limit", label, maxMB))
			continue
		case err != nil:
			h.log.Warn("channel: attachment download failed", "channel", a.name, "err", err)
			notes = append(notes, label+" could not be downloaded")
			continue
		}
		mt := att.MIME
		if mt == "" {
			mt = mime.TypeByExtension(path.Ext(name))
		}
		if mt == "" {
			mt = "application/octet-stream"
		}
		typ := core.PartFile
		if strings.HasPrefix(mt, "image/") {
			typ = core.PartImage
		}
		parts = append(parts, core.Part{Type: typ, Blob: &core.Blob{MIME: mt, Name: name, Ref: path.Join(dir, name), Size: n}})
	}
	return parts, notes
}

var errTooLarge = errors.New("attachment over the upload limit")

// inbox opens the principal's workspace as an os.Root, so nothing written
// below it can escape through symlinks, and creates the message's folder.
func (h *Hub) inbox(p *core.Principal, msgID string) (*os.Root, string, error) {
	if h.d.WorkDir == "" {
		return nil, "", errors.New("no work directory configured")
	}
	if !core.ValidID(core.PrefixWorkspace, string(p.Workspace)) {
		return nil, "", fmt.Errorf("principal %s has no valid workspace id: %w", p.ID, core.ErrInvalid)
	}
	ws := filepath.Join(h.d.WorkDir, "ws", string(p.Workspace))
	// Workspaces are shared with the tool boxes' group 10001 (SPEC §2.3).
	if err := os.MkdirAll(ws, 0o770); err != nil { //nolint:gosec // G301: group-writable by design
		return nil, "", err
	}
	root, err := os.OpenRoot(ws)
	if err != nil {
		return nil, "", err
	}
	dir := path.Join(".bothy", "in", sanitizeSegment(msgID))
	if err := root.MkdirAll(dir, 0o770); err != nil {
		root.Close()
		return nil, "", err
	}
	return root, dir, nil
}

func (h *Hub) saveAttachment(ctx context.Context, a *adapter, att core.Attachment, root *os.Root, rel string, limit int64) (int64, error) {
	rc, err := a.ch.Fetch(ctx, att, limit)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o660)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(rc, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = errTooLarge
	}
	if err != nil {
		_ = root.Remove(rel)
		return 0, err
	}
	return n, nil
}

// sanitizeSegment keeps a path segment to [A-Za-z0-9._-]; Telegram message
// ids such as "-100123:45" become "-100123_45".
func sanitizeSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)), r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "msg"
	}
	return truncateRunes(out, 96)
}

// sanitizeName reduces an attachment's platform-supplied name to a safe base
// name; hidden names get a prefix so the agent's tools see them.
func sanitizeName(name, mimeType string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r), strings.ContainsRune("-_.+ ()", r):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." || out == "/" {
		out = "file"
		if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
			out += exts[0]
		}
	}
	if strings.HasPrefix(out, ".") {
		out = "_" + out
	}
	if len(out) > 128 {
		ext := path.Ext(out)
		if len(ext) > 16 {
			ext = ""
		}
		cut := 128 - len(ext)
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + ext
	}
	return out
}

func uniqueName(name string, used map[string]bool) string {
	cand := name
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; used[cand]; i++ {
		cand = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
	used[cand] = true
	return cand
}
