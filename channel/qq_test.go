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
