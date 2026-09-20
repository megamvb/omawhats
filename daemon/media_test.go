package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// A retry answer for a message whose stored attachment cannot be downloaded
// must fail the job, not take the daemon down with it.
func TestMediaRetryWithoutDownloadable(t *testing.T) {
	d := testDaemon(t)
	dl := newDownloader(d)
	d.dl = dl

	raw, err := proto.Marshal(&waE2E.Message{Conversation: proto.String("no attachment here")})
	if err != nil {
		t.Fatal(err)
	}
	m := &Msg{Chat: testChat, ID: "M1", Sender: testChat, SenderName: "Ana", TS: 1000, Text: "📷 Photo", Kind: "image",
		Media: &Media{Type: "image"}, mediaRaw: raw, rawChat: testChat, rawSender: testChat}
	if _, err := insertMessage(d.ctx, d.db, m, true); err != nil {
		t.Fatal(err)
	}

	answered := make(chan error, 1)
	job := &mediaJob{chat: testChat, id: "M1", waiters: []func(*Media, error){
		func(md *Media, err error) { answered <- err },
	}}
	dl.retries["M1"] = job

	dl.handleRetry(&events.MediaRetry{MessageID: "M1"})

	select {
	case err := <-answered:
		if err == nil {
			t.Fatal("want an error, got a download")
		}
	default:
		t.Fatal("the waiter was never called")
	}
	if len(dl.retries) != 0 || len(dl.jobs) != 0 {
		t.Errorf("job left behind: retries %d, jobs %d", len(dl.retries), len(dl.jobs))
	}
}

// An unknown message id is simply not ours to answer.
func TestMediaRetryUnknownID(t *testing.T) {
	d := testDaemon(t)
	dl := newDownloader(d)
	dl.handleRetry(&events.MediaRetry{MessageID: "nope"})
}

// ---- cache pruning

// touch writes a file of n bytes into the cache, aged by age.
func touch(t *testing.T, name string, n int, age time.Duration) string {
	t.Helper()
	dir := cacheDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, n), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func storeMedia(t *testing.T, d *Daemon, id string, md *Media, link *LinkPreview, quote *Quote) {
	t.Helper()
	raw, _ := proto.Marshal(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{}})
	m := &Msg{Chat: testChat, ID: id, Sender: testChat, SenderName: "Ana", TS: 1000, Text: "📷 Photo", Kind: "image",
		Media: md, Link: link, Quote: quote, mediaRaw: raw, rawChat: testChat, rawSender: testChat}
	if _, err := insertMessage(d.ctx, d.db, m, true); err != nil {
		t.Fatal(err)
	}
}

func mediaOfStored(t *testing.T, d *Daemon, id string) Media {
	t.Helper()
	var s string
	if err := d.db.QueryRowContext(d.ctx, `SELECT media FROM owa_message WHERE chat = ? AND id = ?`, testChat, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	var md Media
	if s != "" {
		json.Unmarshal([]byte(s), &md)
	}
	return md
}

func TestPruneCacheByAge(t *testing.T) {
	d := testDaemon(t)
	oldAge, oldBytes := cacheMaxAge, cacheMaxBytes
	t.Cleanup(func() { cacheMaxAge, cacheMaxBytes = oldAge, oldBytes })
	cacheMaxAge, cacheMaxBytes = 24*time.Hour, 1<<30

	oldThumb := touch(t, "A1.thumb.jpg", 500, 90*24*time.Hour)
	oldFile := touch(t, "A1.jpg", 4000, 90*24*time.Hour)
	freshFile := touch(t, "A2.jpg", 4000, time.Hour)
	freshThumb := touch(t, "A2.thumb.jpg", 500, time.Hour)
	oldAnim := touch(t, "A3.gif.webp", 3000, 90*24*time.Hour)
	oldAnimSrc := touch(t, "A3.mp4", 3000, 90*24*time.Hour)
	linkThumb := touch(t, "A4.link.jpg", 300, 365*24*time.Hour)
	quoteThumb := touch(t, "A0.thumb.jpg", 300, 365*24*time.Hour)
	orphan := touch(t, "Z9.jpg", 9000, 90*24*time.Hour)
	// A picture pasted into the message box a moment ago belongs to no message
	// yet, and must not be taken away under the person's hands.
	justPasted := touch(t, "Pasted 2026-09-19 18.30.05.png", 2000, time.Minute)
	oldPart := touch(t, "upload-123.part", 100, 4*time.Hour)
	freshPart := touch(t, "A2.jpg.part", 100, time.Minute)

	storeMedia(t, d, "A1", &Media{Type: "image", Thumb: oldThumb, File: oldFile}, nil, nil)
	storeMedia(t, d, "A2", &Media{Type: "image", Thumb: freshThumb, File: freshFile}, nil, nil)
	storeMedia(t, d, "A3", &Media{Type: "gif", File: oldAnimSrc, Anim: oldAnim}, nil, nil)
	storeMedia(t, d, "A4", &Media{Type: "image"}, &LinkPreview{URL: "https://x", Thumb: linkThumb},
		&Quote{ID: "A0", Text: "older", Thumb: quoteThumb})

	d.pruneCache()

	for _, keep := range []string{oldThumb, freshThumb, freshFile, linkThumb, quoteThumb, freshPart, justPasted} {
		if !exists(keep) {
			t.Errorf("%s should have been kept", filepath.Base(keep))
		}
	}
	for _, gone := range []string{oldFile, oldAnim, oldAnimSrc, orphan, oldPart} {
		if exists(gone) {
			t.Errorf("%s should have been removed", filepath.Base(gone))
		}
	}
	// The message keeps its thumbnail and forgets the file it no longer has.
	if md := mediaOfStored(t, d, "A1"); md.File != "" || md.Thumb != oldThumb {
		t.Errorf("A1 media after pruning: %+v", md)
	}
	if md := mediaOfStored(t, d, "A3"); md.File != "" || md.Anim != "" {
		t.Errorf("A3 media after pruning: %+v", md)
	}
	if md := mediaOfStored(t, d, "A2"); md.File != freshFile {
		t.Errorf("A2 should still have its file: %+v", md)
	}
}

func TestPruneCacheBySize(t *testing.T) {
	d := testDaemon(t)
	oldAge, oldBytes := cacheMaxAge, cacheMaxBytes
	t.Cleanup(func() { cacheMaxAge, cacheMaxBytes = oldAge, oldBytes })
	cacheMaxAge, cacheMaxBytes = 365*24*time.Hour, 10000 // everything is young; only the cap bites

	oldest := touch(t, "B1.jpg", 6000, 72*time.Hour)
	middle := touch(t, "B2.jpg", 6000, 48*time.Hour)
	newest := touch(t, "B3.jpg", 6000, time.Hour)
	storeMedia(t, d, "B1", &Media{Type: "image", File: oldest}, nil, nil)
	storeMedia(t, d, "B2", &Media{Type: "image", File: middle}, nil, nil)
	storeMedia(t, d, "B3", &Media{Type: "image", File: newest}, nil, nil)

	d.pruneCache()

	if exists(oldest) || exists(middle) {
		t.Errorf("18000 bytes over a 10000 cap: the two oldest should have gone")
	}
	if !exists(newest) {
		t.Errorf("the newest file should have been kept")
	}
	if md := mediaOfStored(t, d, "B1"); md.File != "" {
		t.Errorf("B1 should have forgotten its file: %+v", md)
	}
}

// ---- fetching again

// A copy already in the cache answers on the spot, and asking with force says
// that copy is no good — which, with nowhere to fetch another from, must leave
// the one that is there where it is.
func TestMediaFetchForceKeepsTheCopyWhenOffline(t *testing.T) {
	d := testDaemon(t)
	dl := newDownloader(d)
	d.dl = dl

	file := touch(t, "F1.jpg", 4000, time.Minute)
	storeMedia(t, d, "F1", &Media{Type: "image", File: file}, nil, nil)

	answered := make(chan error, 1)
	dl.fetch(testChat, "F1", false, func(md *Media, err error) { answered <- err })
	select {
	case err := <-answered:
		if err != nil {
			t.Fatalf("the cached copy should answer at once: %v", err)
		}
	default:
		t.Fatal("the waiter was never called")
	}

	dl.fetch(testChat, "F1", true, func(md *Media, err error) { answered <- err })
	select {
	case err := <-answered:
		if err == nil {
			t.Fatal("want an error with no client to download from")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the forced fetch never answered")
	}
	if !exists(file) {
		t.Error("the copy was thrown away with nothing to put in its place")
	}
	if got := mediaOfStored(t, d, "F1").File; got != file {
		t.Errorf("the stored message lost its file: %q", got)
	}
}

// A forced fetch writes under a name of its own, so a client holding the
// picture under the old name is not handed the same one back.
func TestForcedFetchTakesANewName(t *testing.T) {
	now := time.Unix(1789916976, 0)
	if got := fetchStem("F2", false, now); got != "F2" {
		t.Errorf("an ordinary fetch keeps the message's name, got %q", got)
	}
	got := fetchStem("F2", true, now)
	if got == "F2" || !strings.HasPrefix(got, "F2-") {
		t.Errorf("a forced fetch wants a name of its own, got %q", got)
	}
	if later := fetchStem("F2", true, now.Add(time.Minute)); later == got {
		t.Errorf("two forced fetches took the same name: %q", got)
	}
}

// Thumbnails are written once from the message itself and cannot be fetched
// again, so nothing may drop one.
func TestDropCachedLeavesThumbnails(t *testing.T) {
	thumb := touch(t, "F3.thumb.jpg", 300, time.Minute)
	file := touch(t, "F3.jpg", 3000, time.Minute)
	dropCached(thumb)
	dropCached(file)
	dropCached("")
	if !exists(thumb) {
		t.Error("the thumbnail was dropped")
	}
	if exists(file) {
		t.Error("the downloaded copy is still there")
	}
}
