package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
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

// A managed credential source can hold renewal until an in-flight upload ends.
type submissionTokenSource interface {
	BeginSubmission(context.Context) (string, func(), error)
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
	var token string
	var release func()
	var err error
	if source, ok := c.Tokens.(submissionTokenSource); ok {
		token, release, err = source.BeginSubmission(ctx)
		if release != nil {
			defer release()
		}
	} else {
		token, err = c.Tokens.AccessToken(ctx)
	}
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
	writeJSONPart := func(name, value string) error {
		header := textproto.MIMEHeader{
			"Content-Disposition": {fmt.Sprintf("form-data; name=\"%s\"", name)},
			"Content-Type":        {"application/json; charset=utf-8"},
		}
		w, err := writer.CreatePart(header)
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, value)
		return err
	}
	if err := writeJSONPart("metadata", "{}"); err != nil {
		return err
	}
	filename := strings.ReplaceAll(url.QueryEscape(job.Name), "+", "%20")
	if _, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {fmt.Sprintf("form-data; name=\"filecontent\"; filename=\"%s\"", filename)},
		"Content-Type":        {"application/pdf"},
	}); err != nil {
		return err
	}
	offset := envelope.Len()
	finishings, err := json.Marshal(job.Finishings)
	if err != nil {
		return err
	}
	if err := writeJSONPart("finishings", string(finishings)); err != nil {
		return err
	}
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
	req.Header.Set("Accept", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-ClientType", "SmartClientForMac")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("submit print job: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return submissionError(resp)
	}
	return nil
}

func submissionError(resp *http.Response) error {
	// Only a numeric service code is retained; messages can contain tenant data.
	const limit = 16 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	detail := ""
	if err != nil {
		detail = " (response diagnostics could not be read)"
	} else if len(body) > limit {
		detail = " (response diagnostics exceed the size limit)"
	} else {
		body = bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})
		var jsonError struct {
			Code json.RawMessage `json:"Code"`
		}
		var xmlError struct {
			XMLName xml.Name `xml:"error"`
			Code    string   `xml:"code,attr"`
		}
		var code string
		if json.Unmarshal(body, &jsonError) == nil {
			code = strings.Trim(string(jsonError.Code), "\"")
		} else if xml.Unmarshal(body, &xmlError) == nil {
			code = xmlError.Code
		}
		if numeric, err := strconv.ParseUint(code, 10, 32); err == nil {
			detail = fmt.Sprintf(" (cloud code %d)", numeric)
		}
	}
	return fmt.Errorf("cloud submission returned HTTP %d%s; local PDF retained", resp.StatusCode, detail)
}
