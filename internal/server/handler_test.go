package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/uniflow-ipp-server/internal/cloud"
	"github.com/uniflow-ipp-server/internal/ipp"
)

type submitFunc func(context.Context, cloud.Job) error

func (f submitFunc) Submit(ctx context.Context, job cloud.Job) error { return f(ctx, job) }

func request(t *testing.T, h *Handler, msg *ipp.Message) *ipp.Message {
	t.Helper()
	data, err := ipp.EncodeMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/ipp", bytes.NewReader(data))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/ipp" {
		t.Fatalf("HTTP response: %d %s", w.Code, w.Body.String())
	}
	resp, err := ipp.DecodeMessage(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func printRequest() *ipp.Message {
	return &ipp.Message{
		Version: ipp.DefaultVersion, Code: ipp.OpPrintJob, RequestID: 42,
		OperationGroups: []*ipp.AttributeGroup{
			ipp.OpAttrGroup(
				ipp.CharsetAttr("attributes-charset", "utf-8"),
				ipp.NaturalLanguageAttr("attributes-natural-language", "en"),
				ipp.NameAttr("job-name", "../../test"),
				ipp.NameAttr("requesting-user-name", "test-user"),
				ipp.MimeAttr("document-format", "application/pdf"),
			),
			ipp.JobAttrGroup(
				ipp.IntAttr("copies", 2),
				ipp.KeywordAttr("sides", "two-sided-short-edge"),
				ipp.KeywordAttr("print-color-mode", "monochrome"),
				ipp.MultiEnumAttr("finishings", 20, 74),
				ipp.KeywordAttr("media-source", "tray-2"),
			),
		},
		Data: []byte("%PDF-1.7\n%%EOF\n"),
	}
}

func TestLocalCaptureIsHeld(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler(Config{SpoolDir: dir, PrinterURI: "ipp://127.0.0.1:8631/ipp"})
	resp := request(t, h, printRequest())
	if resp.Code != ipp.StatusOK || resp.RequestID != 42 {
		t.Fatalf("response: %+v", resp)
	}
	state, _ := resp.FindAttribute(ipp.JobAttributesGroup, "job-state").IntValue()
	if state != 4 {
		t.Fatalf("local job state = %d, want held (4)", state)
	}
	job := h.jobs[0]
	if filepath.Dir(job.PDFPath) != dir {
		t.Fatal("job escaped spool directory")
	}
	info, err := os.Stat(job.PDFPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("spool permissions: %v, %v", info, err)
	}
	got, err := os.ReadFile(job.PDFPath)
	if err != nil || !bytes.Equal(got, printRequest().Data) {
		t.Fatalf("saved PDF: %q, %v", got, err)
	}
	if job.Finishings != (cloud.FinishingOptions{
		CopyCount: 2, Staple: "StaplingLeftUpper", EcoStaple: "default", Duplex: "DuplexShortEdge",
		HolePunch: "TwoHolePunch", Color: "BW", MediaSource: "Tray2",
	}) {
		t.Fatalf("finishing mapping: %+v", job.Finishings)
	}
	query := &ipp.Message{Version: ipp.DefaultVersion, Code: ipp.OpGetJobAttributes, RequestID: 43,
		OperationGroups: []*ipp.AttributeGroup{ipp.OpAttrGroup(ipp.IntAttr("job-id", job.ID))}}
	resp = request(t, h, query)
	state, _ = resp.FindAttribute(ipp.JobAttributesGroup, "job-state").IntValue()
	if state != 4 {
		t.Fatal("query reported local capture as completed")
	}
}

func TestCloudSuccessAndFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			calls := 0
			h := NewHandler(Config{SpoolDir: t.TempDir(), Submitter: submitFunc(func(ctx context.Context, job cloud.Job) error {
				calls++
				if _, err := os.Stat(job.PDFPath); err != nil {
					t.Error(err)
				}
				if job.Finishings.CopyCount != 2 {
					t.Error("missing finishing options")
				}
				if failed {
					return errors.New("test upload failure")
				}
				return nil
			})})
			resp := request(t, h, printRequest())
			wantCode, wantState := ipp.StatusOK, int32(9)
			if failed {
				wantCode, wantState = ipp.StatusServerErrorInternal, 8
			}
			if calls != 1 || resp.Code != wantCode || h.jobs[0].State != wantState {
				t.Fatalf("cloud outcome: calls=%d code=%x state=%d", calls, resp.Code, h.jobs[0].State)
			}
			if _, err := os.Stat(h.jobs[0].PDFPath); err != nil {
				t.Fatal("PDF not retained")
			}
		})
	}
}

func TestInvalidJobAndSpoolFailureDoNotDeadlock(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Config{SpoolDir: blocked})
	for i := 0; i < 2; i++ {
		done := make(chan struct{})
		go func() {
			defer close(done)
			if resp := request(t, h, printRequest()); resp.Code != ipp.StatusServerErrorInternal {
				t.Errorf("spool failure code = %x", resp.Code)
			}
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("spool failure leaked handler mutex")
		}
	}
	h = NewHandler(Config{SpoolDir: dir})
	msg := printRequest()
	msg.Data = []byte("not a PDF")
	if resp := request(t, h, msg); resp.Code != ipp.StatusClientErrorDocumentFormat {
		t.Fatalf("invalid document accepted: %x", resp.Code)
	}
	msg = printRequest()
	msg.FindAttribute(ipp.JobAttributesGroup, "copies").Values = ipp.IntAttr("", 0).Values
	if resp := request(t, h, msg); resp.Code != ipp.StatusClientErrorAttributes || len(h.jobs) != 0 {
		t.Fatal("invalid copies accepted")
	}
	msg.Code = ipp.OpValidateJob
	if resp := request(t, h, msg); resp.Code != ipp.StatusClientErrorAttributes {
		t.Fatal("Validate-Job disagrees with Print-Job")
	}
}

func TestConcurrentSubmissionsAndUniqueSpoolFiles(t *testing.T) {
	h := NewHandler(Config{SpoolDir: t.TempDir()})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if resp := request(t, h, printRequest()); resp.Code != ipp.StatusOK {
				t.Errorf("print code: %x", resp.Code)
			}

		}()
	}
	wg.Wait()
	paths := map[string]bool{}
	for _, job := range h.jobs {
		if paths[job.PDFPath] {
			t.Fatal("spool file collision")
		}
		paths[job.PDFPath] = true
	}
	if len(paths) != 16 {
		t.Fatalf("retained jobs = %d", len(paths))
	}
}

func TestMediaSourceInCollection(t *testing.T) {
	msg := printRequest()
	msg.OperationGroups[1].Attributes = []*ipp.Attribute{
		ipp.CollectionAttr("media-col", ipp.KeywordAttr("media-source", "tray-3")),
	}
	opts, invalid := parseFinishings(msg)
	if invalid != "" || opts.MediaSource != "Tray3" {
		t.Fatalf("media-col mapping: %+v, %s", opts, invalid)
	}
}

func TestResponseUsesRequestVersion(t *testing.T) {
	h := NewHandler(Config{SpoolDir: t.TempDir()})
	msg := printRequest()
	msg.Version = [2]byte{1, 1}
	resp := request(t, h, msg)
	if resp.Version != msg.Version || resp.Code != ipp.StatusOK {
		t.Fatalf("IPP 1.1 response: %+v", resp)
	}
	msg.Version = [2]byte{3, 0}
	if resp := request(t, h, msg); resp.Code != 0x0503 {
		t.Fatal("unsupported IPP version accepted")
	}
}
