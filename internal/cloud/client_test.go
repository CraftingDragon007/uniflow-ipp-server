package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fixedToken struct{ err error }

func (f fixedToken) AccessToken(context.Context) (string, error) { return "test-token", f.err }

type handlerTransport struct{ handler http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

type responseTransport func(*http.Request) (*http.Response, error)

func (t responseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return t(r) }

type unreadableBody struct{}

func (unreadableBody) Read([]byte) (int, error) { return 0, errors.New("unreadable response body") }
func (unreadableBody) Close() error             { return nil }

func TestSubmissionWireFormat(t *testing.T) {
	pdf := bytes.Repeat([]byte("%PDF-test\n"), 2048)
	path := filepath.Join(t.TempDir(), "test.pdf")
	if err := os.WriteFile(path, pdf, 0600); err != nil {
		t.Fatal(err)
	}
	opts := DefaultFinishings()
	opts.CopyCount = 2
	opts.Duplex = "DuplexLongEdge"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/smartclient/printjob/1234567890abcdef1234567890abcdef" ||
			r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-token" ||
			r.Header.Get("X-ClientType") != "SmartClientForMac" || r.Header.Get("Accept") != "application/json; charset=utf-8" ||
			r.ContentLength <= int64(len(pdf)) {
			t.Errorf("incorrect request: %s %s, headers=%v", r.Method, r.URL.Path, r.Header)
		}
		mr, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		seen := map[string]bool{}
		var order []string
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Error(err)
				break
			}
			data, err := io.ReadAll(part)
			if err != nil {
				t.Error(err)
			}
			seen[part.FormName()] = true
			order = append(order, part.FormName())
			switch part.FormName() {
			case "metadata":
				if part.Header.Get("Content-Disposition") != `form-data; name="metadata"` {
					t.Error("metadata disposition does not match the native quoted form")
				}
				if string(data) != "{}" {
					t.Errorf("metadata = %s", data)
				}
			case "finishings":
				if part.Header.Get("Content-Disposition") != `form-data; name="finishings"` {
					t.Error("finishing disposition does not match the native quoted form")
				}
				var got FinishingOptions
				if err := json.Unmarshal(data, &got); err != nil || got != opts {
					t.Errorf("finishings = %+v, %v", got, err)
				}
			case "filecontent":
				if part.Header.Get("Content-Disposition") != `form-data; name="filecontent"; filename="Test%20%2B%20%C3%BC.pdf"` {
					t.Error("file disposition does not match the native quoted form")
				}
				if !bytes.Equal(data, pdf) || part.FileName() != "Test%20%2B%20%C3%BC.pdf" || part.Header.Get("Content-Type") != "application/pdf" {
					t.Errorf("wrong file part: %v", part.Header)
				}
			default:
				t.Errorf("unexpected part %q", part.FormName())
			}
		}
		if len(seen) != 3 {
			t.Errorf("missing parts: %v", seen)
		}
		if !reflect.DeepEqual(order, []string{"metadata", "filecontent", "finishings"}) {
			t.Errorf("part order = %v", order)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("{}"))
	})
	client := NewClient("http://example.invalid/api/v1/smartclient/printjob/1234567890abcdef1234567890abcdef", fixedToken{})
	client.HTTPClient.Transport = handlerTransport{handler}
	if err := client.Submit(context.Background(), Job{Name: "Test + ü.pdf", PDFPath: path, Finishings: opts}); err != nil {
		t.Fatal(err)
	}
}

func TestSubmissionFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.pdf")
	if err := os.WriteFile(path, []byte("%PDF-test"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestEntityTooLarge, http.StatusInternalServerError, http.StatusFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(status)
				w.Write([]byte("test-private-response"))
			})
			client := NewClient("http://example.invalid", fixedToken{})
			client.HTTPClient.Transport = handlerTransport{handler}
			err := client.Submit(context.Background(), Job{PDFPath: path})
			if err == nil || calls != 1 {
				t.Fatalf("submission result: %v, calls=%d", err, calls)
			}
			if strings.Contains(err.Error(), "test-private-response") {
				t.Fatal("submission error leaked the response body")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("failed submission removed document")
			}
		})
	}

	client := NewClient("http://unused.invalid", fixedToken{err: fmt.Errorf("no token")})
	if err := client.Submit(context.Background(), Job{PDFPath: path}); err == nil {
		t.Fatal("token failure ignored")
	}
}

func TestSubmissionSuccessDependsOnHTTPStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.pdf")
	if err := os.WriteFile(path, []byte("%PDF-test"), 0600); err != nil {
		t.Fatal(err)
	}
	client := NewClient("http://example.invalid", fixedToken{})
	client.HTTPClient.Transport = responseTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusCreated, Body: unreadableBody{}, Header: make(http.Header), Request: r}, nil
	})
	if err := client.Submit(context.Background(), Job{PDFPath: path}); err != nil {
		t.Fatalf("HTTP 201 must count as accepted: %v", err)
	}
}

func TestSubmissionErrorDiagnostics(t *testing.T) {
	for _, body := range []string{
		`{"Code":210138,"ErrorCode":"private-id","Description":"private-response"}`,
		"\ufeff" + `<error code="210138" message="private-response" />`,
	} {
		resp := &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(body))}
		err := submissionError(resp)
		if err.Error() != "cloud submission returned HTTP 400 (cloud code 210138); local PDF retained" {
			t.Fatalf("unexpected numeric diagnostic: %v", err)
		}
	}
	for _, body := range []string{
		`{"Code":"private-response"}`, `<error code="private-response" />`,
		`{"Code":-1}`, `{"Code":4294967296}`, `invalid private-response`,
		strings.Repeat("private-response", 2000),
	} {
		resp := &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(body))}
		err := submissionError(resp)
		if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "cloud code") {
			t.Fatalf("invalid service code leaked into diagnostic: %v", err)
		}
	}
}
