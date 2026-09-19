package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Sending files. JPEG and PNG pictures go as photos (with a thumbnail and
// their size, so the other side shows them in the chat); everything else —
// PDFs, videos, archives, other pictures — goes as a document, which arrives
// exactly as it was, under its own name.

const (
	maxPhotoBytes    = 16 << 20   // bigger pictures go as documents
	maxDocumentBytes = 2000 << 20 // WhatsApp's limit is 2 GB
	thumbSide        = 96
)

// maxPhotoPixels caps what is decoded for a thumbnail. Bytes are not enough of
// a limit: a few megabytes of PNG can hold hundreds of megapixels, which would
// be gigabytes of memory once decoded. A picture above this goes as a document,
// whole and untouched. A variable so the tests can lower it.
var maxPhotoPixels int64 = 50 << 20 // ~52 megapixels

// outgoing is one queued file. Files are sent one at a time, in the order
// they were asked for, so a batch arrives in order.
type outgoing struct {
	c          *conn
	req, chat  string
	path       string
	caption    string
	quote      string
	asDocument bool
}

func (d *Daemon) outboxLoop() {
	for {
		select {
		case <-d.ctx.Done():
			return
		case job := <-d.outbox:
			m, err := d.sendFile(job)
			reply := map[string]any{"type": "sent", "req": job.req, "chat": job.chat, "ok": err == nil}
			if err != nil {
				reply["error"] = err.Error()
			} else {
				reply["id"] = m.ID
			}
			job.c.send(reply)
		}
	}
}

// sniff reads the start of a file, which is enough to tell what it is.
func sniff(path string) ([]byte, os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, nil, errors.New("not a file")
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	return head[:n], st, nil
}

// photoMime is the type of a picture that can go as a photo, judged by its
// content (not its name), or "".
func photoMime(head []byte) string {
	switch ct := http.DetectContentType(head); ct {
	case "image/jpeg", "image/png":
		return ct
	}
	return ""
}

// documentMime names a document's type by its extension, then its content.
func documentMime(path string, head []byte) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); t != "" {
		return strings.TrimSpace(strings.Split(t, ";")[0])
	}
	if ct := strings.TrimSpace(strings.Split(http.DetectContentType(head), ";")[0]); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// thumbnail scales a picture down to at most side×side and encodes it as a
// small JPEG; transparency becomes white, as WhatsApp shows it.
func thumbnail(img image.Image, side int) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	tw, th := w, h
	if w >= h && w > side {
		tw, th = side, h*side/w
	} else if h > w && h > side {
		tw, th = w*side/h, side
	}
	tw, th = max(tw, 1), max(th, 1)
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		y0, y1 := b.Min.Y+y*h/th, b.Min.Y+(y+1)*h/th
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < tw; x++ {
			x0, x1 := b.Min.X+x*w/tw, b.Min.X+(x+1)*w/tw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			// Average a few samples of the block: enough for a thumbnail,
			// and cheap on a 50-megapixel picture.
			sx, sy := max((x1-x0)/4, 1), max((y1-y0)/4, 1)
			var r, g, bl, n uint64
			for yy := y0; yy < y1; yy += sy {
				for xx := x0; xx < x1; xx += sx {
					cr, cg, cb, ca := img.At(xx, yy).RGBA()
					white := uint64(0xffff - ca)
					r += uint64(cr) + white
					g += uint64(cg) + white
					bl += uint64(cb) + white
					n++
				}
			}
			dst.Set(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), 0xff})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 70}); err != nil {
		return nil
	}
	return out.Bytes()
}

// upload encrypts and uploads a file, streaming it through a temporary file
// next to the media cache (not /tmp, which is memory, and a video can be big).
func upload(ctx context.Context, cli *whatsmeow.Client, path string, kind whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	f, err := os.Open(path)
	if err != nil {
		return whatsmeow.UploadResponse{}, err
	}
	defer f.Close()
	if err := os.MkdirAll(cacheDir(), 0o700); err != nil {
		return whatsmeow.UploadResponse{}, err
	}
	tmp, err := os.CreateTemp(cacheDir(), "upload-*.part")
	if err != nil {
		return whatsmeow.UploadResponse{}, err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	return cli.UploadReader(ctx, f, tmp, kind)
}

// uploader uploads one file (a function so tests need no connection).
type uploader func(path string, kind whatsmeow.MediaType) (whatsmeow.UploadResponse, error)

// buildFileMessage uploads the file and returns the message that carries it.
// A picture that is not a JPEG or PNG, or too big for a photo, goes as a
// document.
func buildFileMessage(job outgoing, ci *waE2E.ContextInfo, send uploader) (*waE2E.Message, error) {
	path := job.path
	head, st, err := sniff(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", filepath.Base(path), err)
	}
	if st.Size() == 0 {
		return nil, fmt.Errorf("%s is empty", filepath.Base(path))
	}
	if st.Size() > maxDocumentBytes {
		return nil, fmt.Errorf("%s is bigger than WhatsApp's 2 GB limit", filepath.Base(path))
	}
	caption := strings.TrimSpace(job.caption)

	if pm := photoMime(head); pm != "" && !job.asDocument && st.Size() <= maxPhotoBytes && photoFits(path) {
		if img, err := decodeFile(path); err == nil {
			up, err := send(path, whatsmeow.MediaImage)
			if err != nil {
				return nil, fmt.Errorf("upload failed: %w", err)
			}
			b := img.Bounds()
			im := &waE2E.ImageMessage{
				URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
				Mimetype: proto.String(pm), FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
				FileLength: proto.Uint64(up.FileLength), Width: proto.Uint32(uint32(b.Dx())), Height: proto.Uint32(uint32(b.Dy())),
				JPEGThumbnail: thumbnail(img, thumbSide), ContextInfo: ci,
			}
			if caption != "" {
				im.Caption = proto.String(caption)
			}
			return &waE2E.Message{ImageMessage: im}, nil
		}
		// Undecodable: send it as it is.
	}

	up, err := send(path, whatsmeow.MediaDocument)
	if err != nil {
		return nil, fmt.Errorf("upload failed: %w", err)
	}
	name := filepath.Base(path)
	dm := &waE2E.DocumentMessage{
		URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
		Mimetype: proto.String(documentMime(path, head)), FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
		FileLength: proto.Uint64(up.FileLength), FileName: proto.String(name), Title: proto.String(name),
		ContextInfo: ci,
	}
	if caption == "" {
		return &waE2E.Message{DocumentMessage: dm}, nil
	}
	// A document's caption only shows when it is wrapped like this.
	dm.Caption = proto.String(caption)
	return &waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{
		Message: &waE2E.Message{DocumentMessage: dm},
	}}, nil
}

// photoFits reads only the header, so the size in pixels is known before
// anything is decoded.
func photoFits(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return false
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return false
	}
	return int64(cfg.Width)*int64(cfg.Height) <= maxPhotoPixels
}

func decodeFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// sendFile uploads and sends one file, then stores and shows it like any
// message of ours. The stored copy points at the file itself, so it opens
// without downloading it back.
func (d *Daemon) sendFile(job outgoing) (*Msg, error) {
	cli := d.client()
	if cli == nil || !cli.IsLoggedIn() || !cli.IsConnected() {
		return nil, errors.New("not connected")
	}
	to, err := types.ParseJID(job.chat)
	if err != nil {
		return nil, fmt.Errorf("invalid chat: %w", err)
	}
	if !filepath.IsAbs(job.path) {
		return nil, errors.New("the file path must be absolute")
	}
	var ci *waE2E.ContextInfo
	var quote *Quote
	if job.quote != "" {
		if ci, quote, err = d.replyContext(job.chat, job.quote); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(d.ctx, 30*time.Minute)
	defer cancel()
	wm, err := buildFileMessage(job, ci, func(path string, kind whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
		return upload(ctx, cli, path, kind)
	})
	if err != nil {
		return nil, err
	}
	sendCtx, sendCancel := context.WithTimeout(d.ctx, 60*time.Second)
	defer sendCancel()
	resp, err := cli.SendMessage(sendCtx, to, wm)
	if err != nil {
		return nil, err
	}
	text, kind := describe(wm)
	m := d.sentMsg(job.chat, resp, text, kind, quote)
	m.mediaRaw = attach(m, wm)
	if m.Media != nil {
		m.Media.File = job.path
	}
	d.storeSent(to, m, job.req)
	return m, nil
}

// sentMsg is a message of ours as it is stored and shown.
func (d *Daemon) sentMsg(chat string, resp whatsmeow.SendResponse, text, kind string, quote *Quote) *Msg {
	me := d.meJID().String()
	return &Msg{
		Chat: chat, ID: resp.ID, Sender: me, SenderName: "You", FromMe: true,
		TS: resp.Timestamp.Unix(), Text: text, Kind: kind, Status: "sent", Quote: quote,
		rawChat: chat, rawSender: me,
	}
}

// storeSent keeps a message we sent and shows it to every client; req lets
// the client that sent it swap its placeholder for it.
func (d *Daemon) storeSent(to types.JID, m *Msg, req string) {
	ensureChat(d.ctx, d.db, m.Chat, to.Server == types.GroupServer, "")
	insertMessage(d.ctx, d.db, m, true)
	refreshPreview(d.ctx, d.db, m.Chat)
	setUnread(d.ctx, d.db, m.Chat, 0)
	d.srv.broadcast(map[string]any{"type": "message", "chat": m.Chat, "message": m, "req": req})
	d.markChatsDirty()
}
