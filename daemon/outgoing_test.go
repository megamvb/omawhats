package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func writePicture(t *testing.T, path string, w, h int, asPNG bool) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	var buf bytes.Buffer
	if asPNG {
		png.Encode(&buf, img)
	} else {
		jpeg.Encode(&buf, img, nil)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeUpload records what was uploaded as what.
type fakeUpload struct{ kinds []whatsmeow.MediaType }

func (f *fakeUpload) upload(path string, kind whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	f.kinds = append(f.kinds, kind)
	st, err := os.Stat(path)
	if err != nil {
		return whatsmeow.UploadResponse{}, err
	}
	return whatsmeow.UploadResponse{URL: "https://mmg.example/x", DirectPath: "/v/x", MediaKey: []byte{1},
		FileSHA256: []byte{2}, FileEncSHA256: []byte{3}, FileLength: uint64(st.Size())}, nil
}

func TestSendPhoto(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shot.png")
	writePicture(t, p, 640, 320, true)
	up := &fakeUpload{}
	ci := &waE2E.ContextInfo{StanzaID: proto.String("Q1")}
	wm, err := buildFileMessage(outgoing{path: p, caption: " look "}, ci, up.upload)
	if err != nil {
		t.Fatal(err)
	}
	im := wm.GetImageMessage()
	if im == nil || up.kinds[0] != whatsmeow.MediaImage {
		t.Fatalf("want a photo, got %v", wm)
	}
	if im.GetMimetype() != "image/png" || im.GetWidth() != 640 || im.GetHeight() != 320 || im.GetCaption() != "look" {
		t.Errorf("photo fields: %v", im)
	}
	if im.GetContextInfo().GetStanzaID() != "Q1" || im.GetFileLength() == 0 {
		t.Errorf("reply or length missing: %v", im)
	}
	th, err := jpeg.DecodeConfig(bytes.NewReader(im.GetJPEGThumbnail()))
	if err != nil || th.Width != thumbSide || th.Height != thumbSide/2 {
		t.Errorf("thumbnail %v %v", th, err)
	}
	if text, kind := describe(wm); text != "📷 Photo · look" || kind != "image" {
		t.Errorf("describe %q %q", text, kind)
	}
}

func TestSendAsDocument(t *testing.T) {
	dir := t.TempDir()
	jpg := filepath.Join(dir, "IMG 1.jpg")
	writePicture(t, jpg, 50, 80, false)
	pdf := filepath.Join(dir, "report.pdf")
	os.WriteFile(pdf, []byte("%PDF-1.4\n%fake\n"), 0o600)
	video := filepath.Join(dir, "clip.mp4")
	os.WriteFile(video, append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}, make([]byte, 64)...), 0o600)
	webp := filepath.Join(dir, "pic.webp")
	os.WriteFile(webp, []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), 0o600)
	noext := filepath.Join(dir, "notes")
	os.WriteFile(noext, []byte("plain words\n"), 0o600)

	cases := []struct {
		job              outgoing
		mime, name, text string
	}{
		{outgoing{path: jpg, asDocument: true}, "image/jpeg", "IMG 1.jpg", "📄 IMG 1.jpg"},
		{outgoing{path: pdf}, "application/pdf", "report.pdf", "📄 report.pdf"},
		{outgoing{path: video, caption: "the clip"}, "video/mp4", "clip.mp4", "📄 clip.mp4 · the clip"},
		{outgoing{path: webp}, "image/webp", "pic.webp", "📄 pic.webp"},
		{outgoing{path: noext}, "text/plain", "notes", "📄 notes"},
	}
	for _, c := range cases {
		up := &fakeUpload{}
		wm, err := buildFileMessage(c.job, nil, up.upload)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if up.kinds[0] != whatsmeow.MediaDocument {
			t.Errorf("%s uploaded as %v", c.name, up.kinds[0])
		}
		md, holder, _ := mediaOf(wm)
		if md == nil || md.Type != "document" || md.Mime != c.mime || md.FileName != c.name || holder.GetDocumentMessage() == nil {
			t.Errorf("%s: media %+v", c.name, md)
		}
		if text, _ := describe(wm); text != c.text {
			t.Errorf("%s: describe %q", c.name, text)
		}
		if (c.job.caption != "") != (wm.GetDocumentWithCaptionMessage() != nil) {
			t.Errorf("%s: caption wrapping wrong", c.name)
		}
	}
}

func TestSendRefusals(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.txt")
	os.WriteFile(empty, nil, 0o600)
	up := &fakeUpload{}
	for _, p := range []string{empty, dir, filepath.Join(dir, "missing.pdf")} {
		if _, err := buildFileMessage(outgoing{path: p}, nil, up.upload); err == nil {
			t.Errorf("%s: want an error", p)
		}
	}
	if len(up.kinds) != 0 {
		t.Errorf("nothing should have been uploaded")
	}
}

// A picture with too many pixels to decode safely goes as a document, whole:
// bytes alone are no limit, since a small PNG can hold a huge picture.
func TestHugePhotoGoesAsDocument(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "huge.png")
	writePicture(t, big, 40, 40, true)
	small := filepath.Join(dir, "small.png")
	writePicture(t, small, 20, 20, true)

	old := maxPhotoPixels
	t.Cleanup(func() { maxPhotoPixels = old })
	maxPhotoPixels = 20 * 20 // 40×40 is over it, 20×20 is right on it

	if !photoFits(small) || photoFits(big) {
		t.Fatalf("photoFits: small %v, big %v", photoFits(small), photoFits(big))
	}
	up := &fakeUpload{}
	wm, err := buildFileMessage(outgoing{path: big}, nil, up.upload)
	if err != nil {
		t.Fatal(err)
	}
	if wm.GetImageMessage() != nil || wm.GetDocumentMessage() == nil || up.kinds[0] != whatsmeow.MediaDocument {
		t.Errorf("the big picture should have gone as a document: %v", wm)
	}
	up = &fakeUpload{}
	wm, err = buildFileMessage(outgoing{path: small}, nil, up.upload)
	if err != nil {
		t.Fatal(err)
	}
	if wm.GetImageMessage() == nil {
		t.Errorf("the small picture should still go as a photo: %v", wm)
	}
	// A file that is not a picture at all must not be mistaken for one.
	notAPicture := filepath.Join(dir, "broken.png")
	os.WriteFile(notAPicture, []byte("\x89PNG\r\n\x1a\nnot really"), 0o600)
	if photoFits(notAPicture) {
		t.Errorf("a broken picture should not fit")
	}
}

func TestThumbnailTransparent(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 10, 10)) // all transparent
	th := thumbnail(img, thumbSide)
	dec, err := jpeg.Decode(bytes.NewReader(th))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := dec.At(5, 5).RGBA()
	if r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Errorf("transparent should become white, got %d %d %d", r>>8, g>>8, b>>8)
	}
}
