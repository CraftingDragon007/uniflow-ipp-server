package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"time"
)

type FinishingOptions struct {
	CopyCount   int32  `json:"CopyCount"`
	Staple      string `json:"Staple"`
	EcoStaple   string `json:"EcoStaple"`
	Duplex      string `json:"Duplex"`
	HolePunch   string `json:"HolePunch"`
	Color       string `json:"Color"`
	MediaSource string `json:"MediaSource"`
}

func DefaultFinishings() FinishingOptions {
	return FinishingOptions{
		CopyCount: 1, Staple: "NoStapling", EcoStaple: "default",
		Duplex: "Simplex", HolePunch: "NoHolePunch", Color: "Color", MediaSource: "Auto",
	}
}

type Job struct {
	Name       string
	PDFPath    string
	Finishings FinishingOptions
}

type TokenSource interface {
	AccessToken(context.Context) (string, error)
}

type Client struct {
	SubmissionURL string
	Tokens        TokenSource
	HTTPClient    *http.Client
}

func NewClient(submissionURL string, tokens TokenSource) *Client {
	return &Client{
		SubmissionURL: submissionURL, Tokens: tokens,
		HTTPClient: &http.Client{
			Timeout: 120 * time.Second,
			// Upload redirects could leak documents or silently change POST to GET.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (c *Client) Submit(ctx context.Context, job Job) error {
	token, err := c.Tokens.AccessToken(ctx)
	if err != nil {
		return err
	}
	f, err := os.Open(job.PDFPath)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	var envelope bytes.Buffer
	writer := multipart.NewWriter(&envelope)
	for _, part := range []struct{ name, value string }{
		{"metadata", "{}"},
		{"finishings", ""},
	} {
		value := part.value
		if part.name == "finishings" {
			data, err := json.Marshal(job.Finishings)
			if err != nil {
				return err
			}
			value = string(data)
		}
		header := textproto.MIMEHeader{
			"Content-Disposition": {mime.FormatMediaType("form-data", map[string]string{"name": part.name})},
			"Content-Type":        {"application/json; charset=utf-8"},
		}
		w, err := writer.CreatePart(header)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(w, value); err != nil {
			return err
		}
	}
	filename := strings.ReplaceAll(url.QueryEscape(job.Name), "+", "%20")
	if _, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {mime.FormatMediaType("form-data", map[string]string{"name": "filecontent", "filename": filename})},
		"Content-Type":        {"application/pdf"},
	}); err != nil {
		return err
	}
	offset := envelope.Len()
	if err := writer.Close(); err != nil {
		return err
	}
	// Only multipart framing is buffered; the PDF streams from its spool file.
	data := envelope.Bytes()
	body := io.MultiReader(bytes.NewReader(data[:offset]), f, bytes.NewReader(data[offset:]))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.SubmissionURL, body)
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(data)) + stat.Size()
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-ClientType", "SmartClientForMac")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("submit print job: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloud submission returned HTTP %d; local PDF retained", resp.StatusCode)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("read submission response: %w", err)
	}
	return nil
}
