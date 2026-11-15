package stt

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranscribeParsesText(t *testing.T) {
	var gotFile, gotPrompt, gotLang string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference" {
			t.Errorf("path = %s", r.URL.Path)
		}
		mr, err := r.MultipartReader()
		if err != nil {
			t.Fatal(err)
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			switch part.FormName() {
			case "file":
				gotFile = part.FileName()
			case "prompt":
				b, _ := io.ReadAll(part)
				gotPrompt = string(b)
			case "language":
				b, _ := io.ReadAll(part)
				gotLang = string(b)
			}
			part.Close()
		}
		json.NewEncoder(w).Encode(map[string]string{"text": "  hello world  "})
	}))
	defer srv.Close()

	dir := t.TempDir()
	wav := filepath.Join(dir, "a.wav")
	os.WriteFile(wav, []byte("RIFFfake"), 0o644)

	text, err := Transcribe(context.Background(), srv.URL, wav, "accent hint", "en")
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello world" {
		t.Fatalf("text = %q", text)
	}
	if gotFile != "a.wav" {
		t.Fatalf("file field = %q", gotFile)
	}
	if gotPrompt != "accent hint" {
		t.Fatalf("prompt = %q", gotPrompt)
	}
	if gotLang != "en" {
		t.Fatalf("language = %q", gotLang)
	}
}

func TestTranscribeErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	dir := t.TempDir()
	wav := filepath.Join(dir, "a.wav")
	os.WriteFile(wav, []byte("x"), 0o644)
	if _, err := Transcribe(context.Background(), srv.URL, wav, "", "en"); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestTranscribeEmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"text": "   "})
	}))
	defer srv.Close()
	dir := t.TempDir()
	wav := filepath.Join(dir, "a.wav")
	os.WriteFile(wav, []byte("x"), 0o644)
	text, err := Transcribe(context.Background(), srv.URL, wav, "", "en")
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		t.Fatalf("expected empty, got %q", text)
	}
	if strings.TrimSpace(text) != "" {
		t.Fatal("untrimmed")
	}
}
