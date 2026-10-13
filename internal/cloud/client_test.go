package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type fixedToken struct{ err error }

func (f fixedToken) AccessToken(context.Context) (string, error) { return "test-token", f.err }

func TestSubmissionWireFormat(t *testing.T) {
	pdf := bytes.Repeat([]byte("%PDF-test\n"), 2048)
	path := filepath.Join(t.TempDir(), "test.pdf")
	if err := os.WriteFile(path, pdf, 0600); err != nil {
		t.Fatal(err)
	}
	opts := DefaultFinishings()
	opts.CopyCount = 2
	opts.Duplex = "DuplexLongEdge"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/smartclient/printjob/1234567890abcdef1234567890abcdef" ||
			r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-token" ||
			r.Header.Get("X-ClientType") != "SmartClientForMac" || r.ContentLength <= int64(len(pdf)) {
			t.Errorf("incorrect request: %s %s, headers=%v", r.Method, r.URL.Path, r.Header)
		}
		mr, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		seen := map[string]bool{}
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
			switch part.FormName() {
			case "metadata":
				if string(data) != "{}" {
					t.Errorf("metadata = %s", data)
				}
			case "finishings":
				var got FinishingOptions
				if err := json.Unmarshal(data, &got); err != nil || got != opts {
					t.Errorf("finishings = %+v, %v", got, err)
				}
			case "filecontent":
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
		w.Write([]byte("{}"))
	}))
	defer srv.Close()
	client := NewClient(srv.URL+"/api/v1/smartclient/printjob/1234567890abcdef1234567890abcdef", fixedToken{})
	if err := client.Submit(context.Background(), Job{Name: "Test + ü.pdf", PDFPath: path, Finishings: opts}); err != nil {
		t.Fatal(err)
	}
}

func TestSubmissionFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.pdf")
	if err := os.WriteFile(path, []byte("%PDF-test"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestEntityTooLarge, http.StatusInternalServerError, http.StatusFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(status)
				w.Write([]byte("test-private-response"))
			}))
			defer srv.Close()
			client := NewClient(srv.URL, fixedToken{})
			err := client.Submit(context.Background(), Job{PDFPath: path})
			if err == nil || calls != 1 {
				t.Fatalf("submission result: %v, calls=%d", err, calls)
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
