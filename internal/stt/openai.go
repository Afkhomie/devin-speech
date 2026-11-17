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

// TranscribeRemote POSTs a wav file to an OpenAI-compatible
// /audio/transcriptions endpoint (OpenAI, Groq, etc.) and returns
// the trimmed transcript text.
func TranscribeRemote(ctx context.Context, baseURL, apiKey, model, wavPath string) (string, error) {
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
	mw.WriteField("model", model)
	mw.WriteField("response_format", "json")
	mw.Close()

	url := strings.TrimRight(baseURL, "/") + "/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("remote stt %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.Text), nil
}
