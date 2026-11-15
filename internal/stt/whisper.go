package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Transcribe POSTs a wav file to whisper-server /inference and returns
// the trimmed transcript text ("" on silence/empty).
func Transcribe(ctx context.Context, serverURL, wavPath, prompt, language string) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filepath.Base(wavPath))
	if err != nil {
		return "", err
	}
	f, err := os.Open(wavPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(fw, f); err != nil {
		return "", err
	}
	mw.WriteField("response_format", "json")
	if prompt != "" {
		mw.WriteField("prompt", prompt)
	}
	mw.WriteField("language", language)
	mw.Close()

	url := strings.TrimRight(serverURL, "/") + "/inference"
	req, err := http.NewRequestWithContext(ctx, "POST", url, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("whisper-server %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.Text), nil
}
