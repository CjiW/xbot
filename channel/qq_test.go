package channel

import (
	"testing"
)

func TestFormatAttachments_Empty(t *testing.T) {
	result := formatAttachments(nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}

	result = formatAttachments([]qqAttachment{})
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestFormatAttachments_Image(t *testing.T) {
	atts := []qqAttachment{
		{
			ContentType: "image/jpeg",
			Filename:    "photo.jpg",
			Width:       800,
			Height:      600,
			URL:         "https://example.com/photo.jpg",
		},
	}
	result := formatAttachments(atts)
	expected := `<image url="https://example.com/photo.jpg" filename="photo.jpg" width="800" height="600" />`
	if result != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, result)
	}
}

func TestFormatAttachments_ImageNoScheme(t *testing.T) {
	atts := []qqAttachment{
		{
			ContentType: "image/png",
			Filename:    "pic.png",
			URL:         "multimedia.nt.qq.com/download?xxx",
		},
	}
	result := formatAttachments(atts)
	expected := `<image url="https://multimedia.nt.qq.com/download?xxx" filename="pic.png" />`
	if result != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, result)
	}
}

func TestFormatAttachments_File(t *testing.T) {
	atts := []qqAttachment{
		{
			ContentType: "file",
			Filename:    "report.pdf",
			Size:        102400,
			URL:         "https://example.com/report.pdf",
		},
	}
	result := formatAttachments(atts)
	expected := `<file url="https://example.com/report.pdf" filename="report.pdf" size="102400" />`
	if result != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, result)
	}
}

func TestFormatAttachments_Video(t *testing.T) {
	atts := []qqAttachment{
		{
			ContentType: "video/mp4",
			Filename:    "clip.mp4",
			URL:         "https://example.com/clip.mp4",
		},
	}
	result := formatAttachments(atts)
	expected := `<video url="https://example.com/clip.mp4" filename="clip.mp4" />`
	if result != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, result)
	}
}

func TestFormatAttachments_Voice(t *testing.T) {
	atts := []qqAttachment{
		{
			ContentType: "voice",
			Filename:    "voice.amr",
			URL:         "https://example.com/voice.amr",
			VoiceWavURL: "https://example.com/voice.wav",
			ASRText:     "hello world",
		},
	}
	result := formatAttachments(atts)
	expected := `<audio url="https://example.com/voice.amr" filename="voice.amr" wav_url="https://example.com/voice.wav" asr_text="hello world" />`
	if result != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, result)
	}
}

func TestFormatAttachments_Multiple(t *testing.T) {
	atts := []qqAttachment{
		{
			ContentType: "image/jpeg",
			Filename:    "a.jpg",
			URL:         "https://example.com/a.jpg",
		},
		{
			ContentType: "file",
			Filename:    "b.txt",
			URL:         "https://example.com/b.txt",
		},
	}
	result := formatAttachments(atts)
	expected := "<image url=\"https://example.com/a.jpg\" filename=\"a.jpg\" />\n<file url=\"https://example.com/b.txt\" filename=\"b.txt\" />"
	if result != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, result)
	}
}

func TestFormatAttachments_EmptyURL(t *testing.T) {
	atts := []qqAttachment{
		{
			ContentType: "image/jpeg",
			Filename:    "no-url.jpg",
			URL:         "",
		},
	}
	result := formatAttachments(atts)
	if result != "" {
		t.Errorf("expected empty string for attachment with no URL, got %q", result)
	}
}

func TestFormatAttachments_VoiceNoScheme(t *testing.T) {
	atts := []qqAttachment{
		{
			ContentType: "voice",
			Filename:    "voice.amr",
			URL:         "multimedia.nt.qq.com/voice.amr",
			VoiceWavURL: "multimedia.nt.qq.com/voice.wav",
		},
	}
	result := formatAttachments(atts)
	expected := `<audio url="https://multimedia.nt.qq.com/voice.amr" filename="voice.amr" wav_url="https://multimedia.nt.qq.com/voice.wav" />`
	if result != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, result)
	}
}

func TestExtractAndSendLocalImages_SkipsURLs(t *testing.T) {
	q := &QQChannel{
		msgSeqMap: make(map[string]int),
	}
	content := "Check this: ![photo](https://example.com/photo.jpg) and ![img](http://example.com/img.png)"
	result := q.extractAndSendLocalImages("target", "c2c", content, nil)
	if result != content {
		t.Errorf("expected URLs to be preserved, got %q", result)
	}
}

func TestExtractAndSendLocalImages_SkipsImageKey(t *testing.T) {
	q := &QQChannel{
		msgSeqMap: make(map[string]int),
	}
	content := "![alt](img_v3_xxx)"
	result := q.extractAndSendLocalImages("target", "c2c", content, nil)
	if result != content {
		t.Errorf("expected image_key to be preserved, got %q", result)
	}
}

func TestExtractAndSendLocalImages_SkipsNonImageExt(t *testing.T) {
	q := &QQChannel{
		msgSeqMap: make(map[string]int),
	}
	content := "![doc](report.pdf)"
	result := q.extractAndSendLocalImages("target", "c2c", content, nil)
	if result != content {
		t.Errorf("expected non-image extension to be preserved, got %q", result)
	}
}

func TestExtractAndSendLocalImages_SkipsMissingFile(t *testing.T) {
	q := &QQChannel{
		msgSeqMap: make(map[string]int),
	}
	content := "![photo](/nonexistent/path/photo.jpg)"
	result := q.extractAndSendLocalImages("target", "c2c", content, nil)
	if result != content {
		t.Errorf("expected missing file to be preserved, got %q", result)
	}
}

func TestExtractAndSendLocalFiles_SkipsURLs(t *testing.T) {
	q := &QQChannel{
		msgSeqMap: make(map[string]int),
	}
	content := "See [report](https://example.com/report.pdf)"
	result := q.extractAndSendLocalFiles("target", "c2c", content, nil)
	if result != content {
		t.Errorf("expected URLs to be preserved, got %q", result)
	}
}

func TestExtractAndSendLocalFiles_SkipsImageExtensions(t *testing.T) {
	q := &QQChannel{
		msgSeqMap: make(map[string]int),
	}
	content := "See [photo](photo.jpg)"
	result := q.extractAndSendLocalFiles("target", "c2c", content, nil)
	if result != content {
		t.Errorf("expected image extension to be preserved, got %q", result)
	}
}

func TestExtractAndSendLocalFiles_SkipsMissingFile(t *testing.T) {
	q := &QQChannel{
		msgSeqMap: make(map[string]int),
	}
	content := "See [report](/nonexistent/report.pdf)"
	result := q.extractAndSendLocalFiles("target", "c2c", content, nil)
	if result != content {
		t.Errorf("expected missing file to be preserved, got %q", result)
	}
}

func TestQQMdImageRe(t *testing.T) {
	tests := []struct {
		input   string
		matches [][]string
	}{
		{"![alt](path.jpg)", [][]string{{"![alt](path.jpg)", "alt", "path.jpg"}}},
		{"![](path.png)", [][]string{{"![](path.png)", "", "path.png"}}},
		{"text ![img](a.jpg) more", [][]string{{"![img](a.jpg)", "img", "a.jpg"}}},
		{"no image here", nil},
		{"[link](path.pdf)", nil}, // not an image
	}

	for _, tt := range tests {
		matches := qqMdImageRe.FindAllStringSubmatch(tt.input, -1)
		if len(matches) != len(tt.matches) {
			t.Errorf("input %q: expected %d matches, got %d", tt.input, len(tt.matches), len(matches))
			continue
		}
		for i, m := range matches {
			for j, s := range m {
				if j < len(tt.matches[i]) && s != tt.matches[i][j] {
					t.Errorf("input %q: match[%d][%d] expected %q, got %q", tt.input, i, j, tt.matches[i][j], s)
				}
			}
		}
	}
}

func TestQQMdLinkRe(t *testing.T) {
	tests := []struct {
		input     string
		wantMatch bool
	}{
		{"[report](file.pdf)", true},
		{" [report](file.pdf)", true},
		{"![img](file.jpg)", false}, // image, not link
		{"no link here", false},
	}

	for _, tt := range tests {
		matches := qqMdLinkRe.FindAllString(tt.input, -1)
		gotMatch := len(matches) > 0
		if gotMatch != tt.wantMatch {
			t.Errorf("input %q: expected match=%v, got match=%v (matches: %v)", tt.input, tt.wantMatch, gotMatch, matches)
		}
	}
}

func TestSendMediaMessage_UnsupportedChatType(t *testing.T) {
	q := &QQChannel{
		msgSeqMap: make(map[string]int),
	}
	_, err := q.sendMediaMessage("target", "guild", "file_info", nil)
	if err == nil {
		t.Error("expected error for unsupported chat type")
	}
}

func TestUploadFileToQQ_UnsupportedChatType(t *testing.T) {
	q := &QQChannel{
		msgSeqMap: make(map[string]int),
	}
	_, err := q.uploadFileToQQ("target", "guild", qqFileTypeImage, "base64data")
	if err == nil {
		t.Error("expected error for unsupported chat type")
	}
}
