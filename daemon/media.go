package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waMmsRetry"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// Media describes the attachment of a message. Thumb is written as soon as the
// message is stored (WhatsApp embeds a small JPEG in the message itself); File
// only exists once the client asked for it and the download succeeded.
type Media struct {
	Type     string `json:"type"` // image|sticker|gif|video|audio|document
	Caption  string `json:"caption,omitempty"`
	Thumb    string `json:"thumb,omitempty"`
	File     string `json:"file,omitempty"`
	Anim     string `json:"anim,omitempty"` // gif: the mp4 converted to animated webp
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Mime     string `json:"mime,omitempty"`
	Seconds  int    `json:"seconds,omitempty"`
	FileName string `json:"fileName,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Animated bool   `json:"animated,omitempty"`
	Voice    bool   `json:"voice,omitempty"`
}

type LinkPreview struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Thumb       string `json:"thumb,omitempty"`
}

func cacheDir() string {
	if v := os.Getenv("OMAWHATS_CACHE"); v != "" {
		return v
	}
	return filepath.Join(xdgDir("XDG_CACHE_HOME", ".cache"), appName, "media")
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func mediaPath(id, suffix string) string {
	return filepath.Join(cacheDir(), unsafeName.ReplaceAllString(id, "_")+suffix)
}

func writeThumb(id, suffix string, data []byte) string {
	if len(data) == 0 {
		return ""
	}
	path := mediaPath(id, suffix)
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if err := os.MkdirAll(cacheDir(), 0o700); err != nil {
		return ""
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return ""
	}
	return path
}

// mediaOf finds the attachment in a message. It returns the metadata, a
// minimal message holding only the attachment (what gets stored, so it can be
// downloaded later), and the embedded thumbnail bytes.
func mediaOf(m *waE2E.Message) (*Media, *waE2E.Message, []byte) {
	if m == nil {
		return nil, nil, nil
	}
	switch {
	case m.GetImageMessage() != nil:
		x := m.GetImageMessage()
		return &Media{Type: "image", Caption: x.GetCaption(), Width: int(x.GetWidth()), Height: int(x.GetHeight()),
				Mime: x.GetMimetype(), Size: int64(x.GetFileLength())},
			&waE2E.Message{ImageMessage: x}, x.GetJPEGThumbnail()
	case m.GetStickerMessage() != nil:
		x := m.GetStickerMessage()
		return &Media{Type: "sticker", Width: int(x.GetWidth()), Height: int(x.GetHeight()), Mime: x.GetMimetype(),
				Animated: x.GetIsAnimated(), Size: int64(x.GetFileLength())},
			&waE2E.Message{StickerMessage: x}, x.GetPngThumbnail()
	case m.GetVideoMessage() != nil || m.GetPtvMessage() != nil:
		x := m.GetVideoMessage()
		wrap := &waE2E.Message{VideoMessage: x}
		if x == nil {
			x = m.GetPtvMessage()
			wrap = &waE2E.Message{PtvMessage: x}
		}
		kind := "video"
		if x.GetGifPlayback() {
			kind = "gif"
		}
		return &Media{Type: kind, Caption: x.GetCaption(), Width: int(x.GetWidth()), Height: int(x.GetHeight()),
				Mime: x.GetMimetype(), Seconds: int(x.GetSeconds()), Size: int64(x.GetFileLength())},
			wrap, x.GetJPEGThumbnail()
	case m.GetAudioMessage() != nil:
		x := m.GetAudioMessage()
		return &Media{Type: "audio", Mime: x.GetMimetype(), Seconds: int(x.GetSeconds()), Voice: x.GetPTT(),
			Size: int64(x.GetFileLength())}, &waE2E.Message{AudioMessage: x}, nil
	case m.GetDocumentMessage() != nil:
		x := m.GetDocumentMessage()
		name := x.GetFileName()
		if name == "" {
			name = x.GetTitle()
		}
		return &Media{Type: "document", Caption: x.GetCaption(), FileName: name, Mime: x.GetMimetype(),
				Size: int64(x.GetFileLength())},
			&waE2E.Message{DocumentMessage: x}, x.GetJPEGThumbnail()
	case m.GetDocumentWithCaptionMessage() != nil:
		return mediaOf(m.GetDocumentWithCaptionMessage().GetMessage())
	}
	return nil, nil, nil
}

func downloadableOf(m *waE2E.Message) whatsmeow.DownloadableMessage {
	switch {
	case m.GetImageMessage() != nil:
		return m.GetImageMessage()
	case m.GetStickerMessage() != nil:
		return m.GetStickerMessage()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage()
	case m.GetPtvMessage() != nil:
		return m.GetPtvMessage()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage()
	}
	return nil
}

// setDirectPath points the stored attachment at the path a media retry
// returned.
func setDirectPath(m *waE2E.Message, path string) {
	switch {
	case m.GetImageMessage() != nil:
		m.ImageMessage.DirectPath = proto.String(path)
	case m.GetStickerMessage() != nil:
		m.StickerMessage.DirectPath = proto.String(path)
	case m.GetVideoMessage() != nil:
		m.VideoMessage.DirectPath = proto.String(path)
	case m.GetPtvMessage() != nil:
		m.PtvMessage.DirectPath = proto.String(path)
	case m.GetAudioMessage() != nil:
		m.AudioMessage.DirectPath = proto.String(path)
	case m.GetDocumentMessage() != nil:
		m.DocumentMessage.DirectPath = proto.String(path)
	}
}

func linkOf(m *waE2E.Message) (*LinkPreview, []byte) {
	x := m.GetExtendedTextMessage()
	if x == nil || x.GetMatchedText() == "" || (x.GetTitle() == "" && len(x.GetJPEGThumbnail()) == 0) {
		return nil, nil
	}
	return &LinkPreview{URL: x.GetMatchedText(), Title: x.GetTitle(), Description: x.GetDescription()}, x.GetJPEGThumbnail()
}

// attach fills m.Media/m.Link from the whatsmeow message and writes the
// embedded thumbnails; the returned bytes are what to store for a later
// download.
func attach(m *Msg, wm *waE2E.Message) []byte {
	var raw []byte
	if media, holder, thumb := mediaOf(wm); media != nil {
		media.Thumb = writeThumb(m.ID, ".thumb.jpg", thumb)
		if b, err := proto.Marshal(holder); err == nil {
			raw = b
		}
		m.Media = media
	}
	if link, thumb := linkOf(wm); link != nil {
		link.Thumb = writeThumb(m.ID, ".link.jpg", thumb)
		m.Link = link
	}
	return raw
}

func extFor(md *Media) string {
	if md.FileName != "" {
		if ext := filepath.Ext(md.FileName); ext != "" && len(ext) <= 8 {
			return strings.ToLower(unsafeName.ReplaceAllString(ext[1:], ""))
		}
	}
	base := strings.TrimSpace(strings.Split(md.Mime, ";")[0])
	switch base {
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/png":
		return "png"
	case "video/mp4":
		return "mp4"
	case "audio/ogg":
		return "ogg"
	case "audio/mpeg":
		return "mp3"
	case "audio/mp4":
		return "m4a"
	}
	if exts, _ := mime.ExtensionsByType(base); len(exts) > 0 {
		return strings.TrimPrefix(exts[0], ".")
	}
	switch md.Type {
	case "image":
		return "jpg"
	case "sticker":
		return "webp"
	case "video", "gif":
		return "mp4"
	case "audio":
		return "ogg"
	}
	return "bin"
}

// ---- storage

func loadMedia(ctx context.Context, db *sql.DB, chat, id string) (*Media, *waE2E.Message, error) {
	var mediaJSON string
	var raw []byte
	err := db.QueryRowContext(ctx, `SELECT media, media_proto FROM owa_message WHERE chat = ? AND id = ?`, chat, id).Scan(&mediaJSON, &raw)
	if err != nil {
		return nil, nil, err
	}
	if mediaJSON == "" || len(raw) == 0 {
		return nil, nil, errors.New("this message has no downloadable media")
	}
	var md Media
	if err := json.Unmarshal([]byte(mediaJSON), &md); err != nil {
		return nil, nil, err
	}
	var wm waE2E.Message
	if err := proto.Unmarshal(raw, &wm); err != nil {
		return nil, nil, err
	}
	return &md, &wm, nil
}

func saveMedia(ctx context.Context, db *sql.DB, chat, id string, md *Media, wm *waE2E.Message) {
	data, _ := json.Marshal(md)
	if wm != nil {
		if raw, err := proto.Marshal(wm); err == nil {
			db.ExecContext(ctx, `UPDATE owa_message SET media = ?, media_proto = ? WHERE chat = ? AND id = ?`, string(data), raw, chat, id)
			return
		}
	}
	db.ExecContext(ctx, `UPDATE owa_message SET media = ? WHERE chat = ? AND id = ?`, string(data), chat, id)
}

// ---- downloads

type mediaJob struct {
	force    bool   // the copy on disk is no good: drop it and fetch another
	stem     string // what the file is named after: the message, or a forced fetch's own
	chat, id string
	waiters  []func(*Media, error)
}

type downloader struct {
	d       *Daemon
	mu      sync.Mutex
	jobs    map[string]*mediaJob // by message id
	retries map[string]*mediaJob // waiting for the phone to re-upload
	slots   chan struct{}
}

func newDownloader(d *Daemon) *downloader {
	return &downloader{d: d, jobs: map[string]*mediaJob{}, retries: map[string]*mediaJob{}, slots: make(chan struct{}, 3)}
}

// fetchStem is what a fetched file is named after: the message it belongs to,
// or — when the copy under that name is the one being replaced — a name of its
// own. Written back over the same path, a client that already holds the
// picture under that name would go on showing the one it could not use.
func fetchStem(id string, force bool, now time.Time) string {
	if !force {
		return id
	}
	return id + "-" + strconv.FormatInt(now.Unix(), 36)
}

// dropCached removes a downloaded copy. Thumbnails never come this way: they
// are written once from the message itself and cannot be fetched again.
func dropCached(path string) {
	if path == "" || strings.HasSuffix(path, ".thumb.jpg") {
		return
	}
	os.Remove(path)
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && st.Size() > 0
}

// fetch downloads a message's attachment (once, however many ask) and calls
// done with the result. Already-cached files answer immediately — unless the
// asker says force, which is how a client says the copy it has is no good:
// then the file goes and another is fetched in its place.
func (dl *downloader) fetch(chat, id string, force bool, done func(*Media, error)) {
	d := dl.d
	md, _, err := loadMedia(d.ctx, d.db, chat, id)
	if err != nil {
		log.Printf("media %s: %v", id, err)
		done(nil, err)
		return
	}
	if !force && fileExists(md.File) && (md.Type != "gif" || fileExists(md.Anim)) {
		done(md, nil)
		return
	}
	dl.mu.Lock()
	if job, ok := dl.jobs[id]; ok {
		job.waiters = append(job.waiters, done)
		dl.mu.Unlock()
		return
	}
	if job, ok := dl.retries[id]; ok {
		job.waiters = append(job.waiters, done)
		dl.mu.Unlock()
		return
	}
	job := &mediaJob{chat: chat, id: id, force: force, stem: fetchStem(id, force, time.Now()),
		waiters: []func(*Media, error){done}}
	dl.jobs[id] = job
	dl.mu.Unlock()
	go dl.run(job, false)
}

func (dl *downloader) finish(job *mediaJob, md *Media, err error) {
	dl.mu.Lock()
	delete(dl.jobs, job.id)
	delete(dl.retries, job.id)
	waiters := job.waiters
	dl.mu.Unlock()
	// Nothing else writes down why a download did not work, and "it just does
	// not open" is no help to whoever has to find out why.
	if err != nil {
		log.Printf("media %s: %v", job.id, err)
	}
	for _, w := range waiters {
		w(md, err)
	}
	if err == nil && md != nil {
		dl.d.broadcastMessage(job.chat, job.id)
	}
}

func (dl *downloader) run(job *mediaJob, afterRetry bool) {
	d := dl.d
	dl.slots <- struct{}{}
	defer func() { <-dl.slots }()

	md, wm, err := loadMedia(d.ctx, d.db, job.chat, job.id)
	if err != nil {
		dl.finish(job, nil, err)
		return
	}
	cli := d.client()
	if cli == nil || !cli.IsConnected() {
		dl.finish(job, nil, errors.New("offline — turn OmaWhats on to download"))
		return
	}
	target := downloadableOf(wm)
	if target == nil {
		dl.finish(job, nil, errors.New("unsupported media type"))
		return
	}
	if err := os.MkdirAll(cacheDir(), 0o700); err != nil {
		dl.finish(job, nil, err)
		return
	}
	// What is here now, to be dropped once something better has landed.
	oldFile, oldAnim := md.File, md.Anim
	stem := job.stem
	if stem == "" {
		stem = job.id
	}
	path := mediaPath(stem, "."+extFor(md))
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		dl.finish(job, nil, err)
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, 5*time.Minute)
	err = cli.DownloadToFile(ctx, target, f)
	cancel()
	f.Close()
	if err != nil {
		os.Remove(tmp)
		expired := errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410)
		if expired && !afterRetry {
			if rerr := dl.requestRetry(job, wm); rerr == nil {
				return // finished by the MediaRetry event, or by the timeout
			}
		}
		if expired {
			err = errors.New("the media expired and the phone no longer has it")
		}
		dl.finish(job, nil, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		dl.finish(job, nil, err)
		return
	}
	md.File = path
	if md.Type == "gif" {
		md.Anim = gifToWebp(path, mediaPath(stem, ".gif.webp"))
	}
	saveMedia(d.ctx, d.db, job.chat, job.id, md, nil)
	// The copy that could not be shown goes only now that another one is
	// here: a forced fetch that fails must not leave the person with nothing.
	if job.force {
		if oldFile != md.File {
			dropCached(oldFile)
		}
		if oldAnim != md.Anim {
			dropCached(oldAnim)
		}
	}
	dl.finish(job, md, nil)
}

// requestRetry asks the phone to upload old media again; the answer arrives
// as an events.MediaRetry.
func (dl *downloader) requestRetry(job *mediaJob, wm *waE2E.Message) error {
	d := dl.d
	cli := d.client()
	var rawChat, rawSender string
	var fromMe int
	if err := d.db.QueryRowContext(d.ctx, `SELECT raw_chat, raw_sender, from_me FROM owa_message WHERE chat = ? AND id = ?`,
		job.chat, job.id).Scan(&rawChat, &rawSender, &fromMe); err != nil {
		return err
	}
	chat, err1 := types.ParseJID(rawChat)
	sender, err2 := types.ParseJID(rawSender)
	if err1 != nil || err2 != nil {
		return errors.New("invalid address")
	}
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: fromMe == 1, IsGroup: chat.Server == types.GroupServer},
		ID:            job.id,
	}
	target := downloadableOf(wm)
	dl.mu.Lock()
	delete(dl.jobs, job.id)
	dl.retries[job.id] = job
	dl.mu.Unlock()
	if err := cli.SendMediaRetryReceipt(d.ctx, info, target.GetMediaKey()); err != nil {
		dl.mu.Lock()
		delete(dl.retries, job.id)
		dl.jobs[job.id] = job
		dl.mu.Unlock()
		return err
	}
	log.Printf("media retry requested for %s", job.id)
	time.AfterFunc(45*time.Second, func() {
		dl.mu.Lock()
		_, pending := dl.retries[job.id]
		dl.mu.Unlock()
		if pending {
			dl.finish(job, nil, errors.New("the phone did not re-upload the media (is it online?)"))
		}
	})
	return nil
}

func (dl *downloader) handleRetry(evt *events.MediaRetry) {
	dl.mu.Lock()
	job, ok := dl.retries[evt.MessageID]
	dl.mu.Unlock()
	if !ok {
		return
	}
	d := dl.d
	md, wm, err := loadMedia(d.ctx, d.db, job.chat, job.id)
	if err != nil {
		dl.finish(job, nil, err)
		return
	}
	target := downloadableOf(wm)
	if target == nil {
		// The stored attachment is not one that can be downloaded (an older
		// version stored something else under it).
		dl.finish(job, nil, errors.New("unsupported media type"))
		return
	}
	notif, err := whatsmeow.DecryptMediaRetryNotification(evt, target.GetMediaKey())
	if err != nil {
		if errors.Is(err, whatsmeow.ErrMediaNotAvailableOnPhone) {
			err = errors.New("the media is no longer on the phone")
		}
		dl.finish(job, nil, err)
		return
	}
	if notif.GetResult() != waMmsRetry.MediaRetryNotification_SUCCESS || notif.GetDirectPath() == "" {
		dl.finish(job, nil, fmt.Errorf("the phone refused to re-upload it (%s)", notif.GetResult()))
		return
	}
	setDirectPath(wm, notif.GetDirectPath())
	saveMedia(d.ctx, d.db, job.chat, job.id, md, wm)
	dl.mu.Lock()
	delete(dl.retries, job.id)
	dl.jobs[job.id] = job
	dl.mu.Unlock()
	go dl.run(job, true)
}

// ---- keeping the cache from growing without end

// A downloaded file is only a copy: it can be fetched again from the message
// it belongs to. A thumbnail cannot — it is written once, from the message
// itself — so thumbnails stay however old they are, and they are tiny.
var (
	cacheMaxAge   = 30 * 24 * time.Hour
	cacheMaxBytes = int64(1) << 30 // 1 GB of downloaded files
	partMaxAge    = time.Hour      // leftovers from an interrupted transfer
	// A file nothing points at is given a day before it goes: a picture just
	// pasted into the message box lives here and belongs to no message yet.
	orphanMinAge = 24 * time.Hour
)

// cacheRef is the message a cached file belongs to, and which of its fields
// points at the file.
type cacheRef struct{ chat, id, field string }

// cacheReferences lists every file the stored messages point at: the ones that
// can be dropped and asked for again (file, anim), and the thumbnails that are
// kept — a message's own, a quote's, and a link preview's.
func cacheReferences(ctx context.Context, db *sql.DB) (thumbs map[string]bool, files map[string]cacheRef, err error) {
	thumbs, files = map[string]bool{}, map[string]cacheRef{}
	rows, err := db.QueryContext(ctx, `SELECT chat, id, media, link, quote FROM owa_message
		WHERE media <> '' OR link <> '' OR quote <> ''`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var chat, id, mediaJSON, linkJSON, quoteJSON string
		if err := rows.Scan(&chat, &id, &mediaJSON, &linkJSON, &quoteJSON); err != nil {
			return nil, nil, err
		}
		if mediaJSON != "" {
			var md Media
			if json.Unmarshal([]byte(mediaJSON), &md) == nil {
				if md.Thumb != "" {
					thumbs[md.Thumb] = true
				}
				if md.File != "" {
					files[md.File] = cacheRef{chat, id, "file"}
				}
				if md.Anim != "" {
					files[md.Anim] = cacheRef{chat, id, "anim"}
				}
			}
		}
		if linkJSON != "" {
			var lp LinkPreview
			if json.Unmarshal([]byte(linkJSON), &lp) == nil && lp.Thumb != "" {
				thumbs[lp.Thumb] = true
			}
		}
		if quoteJSON != "" {
			var q Quote
			if json.Unmarshal([]byte(quoteJSON), &q) == nil && q.Thumb != "" {
				thumbs[q.Thumb] = true
			}
		}
	}
	return thumbs, files, rows.Err()
}

// forgetCachedFile takes the path out of the stored message, so the client
// shows the thumbnail again and downloads the file if it is asked for.
func forgetCachedFile(ctx context.Context, db *sql.DB, ref cacheRef) {
	var mediaJSON string
	if db.QueryRowContext(ctx, `SELECT media FROM owa_message WHERE chat = ? AND id = ?`,
		ref.chat, ref.id).Scan(&mediaJSON) != nil || mediaJSON == "" {
		return
	}
	var md Media
	if json.Unmarshal([]byte(mediaJSON), &md) != nil {
		return
	}
	switch ref.field {
	case "file":
		md.File = ""
	case "anim":
		md.Anim = ""
	default:
		return
	}
	data, err := json.Marshal(&md)
	if err != nil {
		return
	}
	db.ExecContext(ctx, `UPDATE owa_message SET media = ? WHERE chat = ? AND id = ?`, string(data), ref.chat, ref.id)
}

// pruneCache clears out the media cache: leftovers of an interrupted transfer,
// files no stored message points at any more (deleted messages, or an older
// version's naming), and then downloaded files past the age or the size limit,
// oldest first. Thumbnails are always kept.
func (d *Daemon) pruneCache() {
	dir := cacheDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	thumbs, files, err := cacheReferences(d.ctx, d.db)
	if err != nil {
		log.Printf("prune cache: %v", err)
		return
	}

	type candidate struct {
		path string
		size int64
		mod  time.Time
		ref  cacheRef
	}
	var candidates []candidate
	var total int64
	removed, freed := 0, int64(0)
	drop := func(path string, size int64) {
		if os.Remove(path) == nil {
			removed++
			freed += size
		}
	}

	now := time.Now()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		ref, referenced := files[path]
		switch {
		case strings.HasSuffix(path, ".part"):
			if now.Sub(info.ModTime()) > partMaxAge {
				drop(path, info.Size())
			}
		case thumbs[path]:
			// Kept: small, and there is no way to write it again.
		case referenced:
			candidates = append(candidates, candidate{path, info.Size(), info.ModTime(), ref})
			total += info.Size()
		default:
			if now.Sub(info.ModTime()) > orphanMinAge {
				drop(path, info.Size())
			}
		}
	}

	// Oldest first, so what goes is what has not been looked at in longest.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mod.Before(candidates[j].mod) })
	for _, c := range candidates {
		if now.Sub(c.mod) <= cacheMaxAge && total <= cacheMaxBytes {
			break
		}
		drop(c.path, c.size)
		forgetCachedFile(d.ctx, d.db, c.ref)
		total -= c.size
	}
	if removed > 0 {
		log.Printf("media cache: removed %d files, %.1f MB", removed, float64(freed)/(1<<20))
	}
}

// cacheLoop prunes at start and once a day after that.
func (d *Daemon) cacheLoop() {
	for {
		d.pruneCache()
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}

// gifToWebp turns WhatsApp's "GIF" (a silent looping mp4) into an animated
// webp that QML's AnimatedImage can play. Returns "" if ffmpeg is missing or
// fails; the client then shows the thumbnail and opens the mp4 on click.
func gifToWebp(src, dst string) string {
	if fileExists(dst) {
		return dst
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-loglevel", "error", "-i", src,
		"-vf", "fps=15,scale='min(360,iw)':-2", "-loop", "0", "-an", "-c:v", "libwebp_anim", "-quality", "70", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("gif → webp: %v %s", err, out)
		os.Remove(dst)
		return ""
	}
	return dst
}
