package cqreply

import (
	"reflect"
	"testing"
)

func TestParseBuildsOrderedTextAndHTTPSImageParts(t *testing.T) {
	answer := "before[CQ:image,file=https://fallback.example.com/old.png,url=https://cdn.example.com/map.png?x=1&amp;y=2]after"

	got := Parse(answer)

	wantParts := []Part{
		{Type: PartText, Value: "before"},
		{Type: PartImage, Value: "https://cdn.example.com/map.png?x=1&y=2"},
		{Type: PartText, Value: "after"},
	}
	if !reflect.DeepEqual(got.Parts, wantParts) {
		t.Fatalf("parts = %#v, want %#v", got.Parts, wantParts)
	}
	if got.PlainText != "beforeafter" {
		t.Fatalf("plain text = %q, want %q", got.PlainText, "beforeafter")
	}
	if got.ImageCount != 1 || got.RejectedImageCount != 0 {
		t.Fatalf("image counts = %d valid, %d rejected", got.ImageCount, got.RejectedImageCount)
	}
}

func TestParseUsesHTTPSFileWhenURLIsMissing(t *testing.T) {
	got := Parse("[CQ:image,file=https://cdn.example.com/map.png]")

	want := []Part{{Type: PartImage, Value: "https://cdn.example.com/map.png"}}
	if !reflect.DeepEqual(got.Parts, want) {
		t.Fatalf("parts = %#v, want %#v", got.Parts, want)
	}
	if got.ImageCount != 1 {
		t.Fatalf("image count = %d, want 1", got.ImageCount)
	}
}

func TestParseAllowsHTTPURLAndPrefersURLOverFile(t *testing.T) {
	answer := "[CQ:image,file=http://fallback.example.com/map.png,url=http://cdn.example.com/map.png]"

	got := Parse(answer)

	want := []Part{{Type: PartImage, Value: "http://cdn.example.com/map.png"}}
	if !reflect.DeepEqual(got.Parts, want) {
		t.Fatalf("parts = %#v, want %#v", got.Parts, want)
	}
	if got.ImageCount != 1 || got.RejectedImageCount != 0 {
		t.Fatalf("image counts = %d valid, %d rejected", got.ImageCount, got.RejectedImageCount)
	}
}

func TestParseMapsRelativeFileToFixedNapCatMediaMount(t *testing.T) {
	tests := []struct {
		name string
		file string
		want string
	}{
		{name: "nested path", file: "maps/campus.png", want: "file:///app/jxh-media/maps/campus.png"},
		{name: "unicode and spaces", file: "地图/校 区.png", want: "file:///app/jxh-media/%E5%9C%B0%E5%9B%BE/%E6%A0%A1%20%E5%8C%BA.png"},
		{name: "cache-like filename", file: "cache.image", want: "file:///app/jxh-media/cache.image"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse("[CQ:image,file=" + tt.file + "]")
			want := []Part{{Type: PartImage, Value: tt.want}}
			if !reflect.DeepEqual(got.Parts, want) {
				t.Fatalf("parts = %#v, want %#v", got.Parts, want)
			}
		})
	}
}

func TestParseFallsBackFromInvalidURLToLocalFile(t *testing.T) {
	got := Parse("[CQ:image,url=ftp://cdn.example.com/map.png,file=maps/campus.png]")

	want := []Part{{Type: PartImage, Value: "file:///app/jxh-media/maps/campus.png"}}
	if !reflect.DeepEqual(got.Parts, want) {
		t.Fatalf("parts = %#v, want %#v", got.Parts, want)
	}
}

func TestParseRejectsUnsafeLocalImagePaths(t *testing.T) {
	tests := []string{
		"/tmp/map.png",
		"../map.png",
		"maps/../../map.png",
		`maps\campus.png`,
		"C:/maps/campus.png",
		"file:///tmp/map.png",
		"base64://payload",
		".",
		"maps/campus.png?version=1",
		"maps/campus.png#preview",
	}
	for _, file := range tests {
		t.Run(file, func(t *testing.T) {
			got := Parse("before[CQ:image,file=" + file + "]after")
			want := []Part{{Type: PartText, Value: "beforeafter"}}
			if !reflect.DeepEqual(got.Parts, want) {
				t.Fatalf("parts = %#v, want %#v", got.Parts, want)
			}
			if got.ImageCount != 0 || got.RejectedImageCount != 1 {
				t.Fatalf("image counts = %d valid, %d rejected", got.ImageCount, got.RejectedImageCount)
			}
		})
	}
}

func TestParsePreservesMultipleImageOrderAndPlainText(t *testing.T) {
	answer := "a[CQ:image,file=https://cdn.example.com/one.png][CQ:image,file=https://cdn.example.com/two.png]b"

	got := Parse(answer)

	want := []Part{
		{Type: PartText, Value: "a"},
		{Type: PartImage, Value: "https://cdn.example.com/one.png"},
		{Type: PartImage, Value: "https://cdn.example.com/two.png"},
		{Type: PartText, Value: "b"},
	}
	if !reflect.DeepEqual(got.Parts, want) {
		t.Fatalf("parts = %#v, want %#v", got.Parts, want)
	}
	if got.PlainText != "ab" {
		t.Fatalf("plain text = %q, want %q", got.PlainText, "ab")
	}
}

func TestParseLeavesOrdinaryTextByteForByteUnchanged(t *testing.T) {
	answer := "literal [CQ:at,qq=123] & ordinary text"

	got := Parse(answer)

	if got.PlainText != answer {
		t.Fatalf("plain text = %q, want %q", got.PlainText, answer)
	}
	if !reflect.DeepEqual(got.Parts, []Part{{Type: PartText, Value: answer}}) {
		t.Fatalf("parts = %#v, want one unchanged text part", got.Parts)
	}
}

func TestParseLeavesUnsupportedAndUnterminatedCQAsText(t *testing.T) {
	answer := "a[CQ:at,qq=123]b[CQ:image,file=https://cdn.example.com/map.png"

	got := Parse(answer)

	want := []Part{{Type: PartText, Value: answer}}
	if !reflect.DeepEqual(got.Parts, want) {
		t.Fatalf("parts = %#v, want %#v", got.Parts, want)
	}
	if got.PlainText != answer {
		t.Fatalf("plain text = %q, want %q", got.PlainText, answer)
	}
}
