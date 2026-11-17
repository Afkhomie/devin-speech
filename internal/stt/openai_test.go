package stt

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTranscribeRemoteParsesText(t *testing.T) {
	var gotModel, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
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
			if part.FormName() == "model" {
				b, _ := io.ReadAll(part)
				gotModel = string(b)
			}
			part.Close()
		}
		json.NewEncoder(w).Encode(map[string]string{"text": "  namaste devin  "})
	}))
	defer srv.Close()

	dir := t.TempDir()
	wav := filepath.Join(dir, "a.wav")
	os.WriteFile(wav, []byte("RIFFfake"), 0o644)

	text, err := TranscribeRemote(context.Background(), srv.URL, "sk-test", "whisper-1", wav)
	if err != nil {
		t.Fatal(err)
	}
	if text != "namaste devin" {
		t.Fatalf("text = %q", text)
	}
	if gotModel != "whisper-1" {
		t.Fatalf("model = %q", gotModel)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("auth = %q", gotAuth)
	}
}

func TestTranscribeRemoteErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
	}))
	defer srv.Close()
	dir := t.TempDir()
	wav := filepath.Join(dir, "a.wav")
	os.WriteFile(wav, []byte("x"), 0o644)
	if _, err := TranscribeRemote(context.Background(), srv.URL, "k", "m", wav); err == nil {
		t.Fatal("expected error on 429")
	}
}
