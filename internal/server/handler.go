package server

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/uniflow-ipp-server/internal/ipp"
)

type JobInfo struct {
	ID          int32
	Name        string
	User        string
	PrinterURI  string
	CreatedAt   time.Time
	PDFPath     string
	Copies      int32
	Duplex      string
	ColorMode   string
}

type Config struct {
	QueueName  string
	SpoolDir   string
	PrinterURI string
}

type Handler struct {
	cfg       Config
	mu        sync.Mutex
	nextJobID int32
	jobs      []*JobInfo
}

func NewHandler(cfg Config) *Handler {
	return &Handler{
		cfg:       cfg,
		nextJobID: 1,
		jobs:      make([]*JobInfo, 0),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.URL.Path != "/ipp" && r.URL.Path != "/ipp/" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	msg, err := ipp.DecodeMessage(r.Body)
	if err != nil {
		log.Printf("ipp decode error: %v", err)
		resp := ipp.NewResponse(1, ipp.StatusClientErrorBadRequest)
		h.writeResponse(w, resp)
		return
	}

	log.Printf("received IPP operation: %s (0x%04X)", ipp.OperationName(msg.Code), msg.Code)

	resp := h.handleOperation(msg)
	log.Printf("responding with status: 0x%04X", resp.Code)
	h.writeResponse(w, resp)
}

func (h *Handler) handleOperation(msg *ipp.Message) *ipp.Message {
	switch msg.Code {
	case ipp.OpPrintJob:
		return h.handlePrintJob(msg)
	case ipp.OpValidateJob:
		return h.handleValidateJob(msg)
	case ipp.OpGetPrinterAttributes:
		return h.handleGetPrinterAttributes(msg)
	case ipp.OpGetJobs:
		return h.handleGetJobs(msg)
	case ipp.OpGetJobAttributes:
		return h.handleGetJobAttributes(msg)
	case ipp.OpIdentifyPrinter:
		return h.handleIdentifyPrinter(msg)
	case ipp.OpCUPSGetPrinters:
		return h.handleCUPSGetPrinters(msg)
	default:
		log.Printf("unsupported operation: %s (0x%04X)", ipp.OperationName(msg.Code), msg.Code)
		resp := ipp.NewResponse(msg.RequestID, ipp.StatusClientErrorNotSupported)
		resp.OperationGroups = ipp.PrinterDescription(h.cfg.PrinterURI)
		resp.OperationGroups = append(resp.OperationGroups, ipp.UnsupportedAttrGroup(
			ipp.KeywordAttr("operations-supported", ipp.OperationName(msg.Code)),
		))
		return resp
	}
}

func (h *Handler) handlePrintJob(msg *ipp.Message) *ipp.Message {
	jobName := "Untitled"
	userName := "unknown"

	if attr := msg.FindAttribute(ipp.OperationAttributesGroup, "job-name"); attr != nil {
		jobName = attr.StringValue()
	}
	if attr := msg.FindAttribute(ipp.OperationAttributesGroup, "requesting-user-name"); attr != nil {
		userName = attr.StringValue()
	}

	log.Printf("Print-Job: name=%q user=%q data_len=%d", jobName, userName, len(msg.Data))

	h.mu.Lock()
	jobID := h.nextJobID
	h.nextJobID++
	now := time.Now()

	spoolDir := h.cfg.SpoolDir
	if spoolDir == "" {
		spoolDir = "/var/spool/uniflow-ipp"
	}
	os.MkdirAll(spoolDir, 0755)

	safeName := sanitizeFilename(jobName)
	pdfPath := filepath.Join(spoolDir, fmt.Sprintf("job-%04d-%s-%s.pdf", jobID, now.Format("20060102-150405"), safeName))

	if err := os.WriteFile(pdfPath, msg.Data, 0644); err != nil {
		log.Printf("failed to save PDF: %v", err)
		resp := ipp.NewResponse(msg.RequestID, ipp.StatusServerErrorInternal)
		resp.OperationGroups = h.baseOpGroup()
		return resp
	}

	job := &JobInfo{
		ID:         jobID,
		Name:       jobName,
		User:       userName,
		PrinterURI: h.cfg.PrinterURI,
		CreatedAt:  now,
		PDFPath:    pdfPath,
	}
	h.jobs = append(h.jobs, job)
	h.mu.Unlock()

	log.Printf("saved job %d to %s", jobID, pdfPath)

	resp := ipp.NewResponseOK(msg.RequestID)
	resp.OperationGroups = []*ipp.AttributeGroup{
		ipp.OpAttrGroup(
			ipp.CharsetAttr("attributes-charset", "utf-8"),
			ipp.NaturalLanguageAttr("attributes-natural-language", "en-us"),
		),
		ipp.JobAttrGroup(
			ipp.IntAttr("job-id", jobID),
			ipp.URIAttr("job-uri", fmt.Sprintf("ipp://localhost/jobs/%d", jobID)),
			ipp.EnumAttr("job-state", 3),
			ipp.KeywordAttr("job-state-reasons", "none"),
			ipp.StringAttr("job-name", jobName),
			ipp.IntAttr("job-originating-user-name", 0),
		),
	}
	return resp
}

func (h *Handler) handleValidateJob(msg *ipp.Message) *ipp.Message {
	resp := ipp.NewResponseOK(msg.RequestID)
	resp.OperationGroups = h.baseOpGroup()
	return resp
}

func (h *Handler) handleGetPrinterAttributes(msg *ipp.Message) *ipp.Message {
	resp := ipp.NewResponseOK(msg.RequestID)
	resp.OperationGroups = append(
		[]*ipp.AttributeGroup{ipp.OpAttrGroup(
			ipp.CharsetAttr("attributes-charset", "utf-8"),
			ipp.NaturalLanguageAttr("attributes-natural-language", "en-us"),
		)},
		ipp.PrinterDescription(h.cfg.PrinterURI)...,
	)
	return resp
}

func (h *Handler) handleGetJobs(msg *ipp.Message) *ipp.Message {
	h.mu.Lock()
	defer h.mu.Unlock()

	resp := ipp.NewResponseOK(msg.RequestID)
	resp.OperationGroups = []*ipp.AttributeGroup{
		ipp.OpAttrGroup(
			ipp.CharsetAttr("attributes-charset", "utf-8"),
			ipp.NaturalLanguageAttr("attributes-natural-language", "en-us"),
		),
	}

	for _, job := range h.jobs {
		jobGroup := ipp.JobAttrGroup(
			ipp.IntAttr("job-id", job.ID),
			ipp.URIAttr("job-uri", fmt.Sprintf("ipp://localhost/jobs/%d", job.ID)),
			ipp.EnumAttr("job-state", 9),
			ipp.StringAttr("job-name", job.Name),
			ipp.StringAttr("job-originating-user-name", job.User),
		)
		resp.OperationGroups = append(resp.OperationGroups, jobGroup)
	}

	return resp
}

func (h *Handler) handleGetJobAttributes(msg *ipp.Message) *ipp.Message {
	var jobID int32
	if attr := msg.FindAttribute(ipp.OperationAttributesGroup, "job-id"); attr != nil {
		if v, err := attr.IntValue(); err == nil {
			jobID = v
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for _, job := range h.jobs {
		if job.ID == jobID {
			resp := ipp.NewResponseOK(msg.RequestID)
			resp.OperationGroups = []*ipp.AttributeGroup{
				ipp.OpAttrGroup(
					ipp.CharsetAttr("attributes-charset", "utf-8"),
					ipp.NaturalLanguageAttr("attributes-natural-language", "en-us"),
				),
				ipp.JobAttrGroup(
					ipp.IntAttr("job-id", job.ID),
					ipp.URIAttr("job-uri", fmt.Sprintf("ipp://localhost/jobs/%d", job.ID)),
					ipp.EnumAttr("job-state", 9),
					ipp.KeywordAttr("job-state-reasons", "none"),
					ipp.StringAttr("job-name", job.Name),
					ipp.StringAttr("job-originating-user-name", job.User),
					ipp.IntAttr("job-media-sheets-completed", 1),
				),
			}
			return resp
		}
	}

	resp := ipp.NewResponse(msg.RequestID, ipp.StatusClientErrorNotPossible)
	resp.OperationGroups = h.baseOpGroup()
	return resp
}

func (h *Handler) handleIdentifyPrinter(msg *ipp.Message) *ipp.Message {
	resp := ipp.NewResponseOK(msg.RequestID)
	resp.OperationGroups = []*ipp.AttributeGroup{
		ipp.OpAttrGroup(
			ipp.CharsetAttr("attributes-charset", "utf-8"),
			ipp.NaturalLanguageAttr("attributes-natural-language", "en-us"),
		),
		ipp.PrinterAttrGroup(
			ipp.KeywordAttr("identify-actions-supported", "display"),
		),
	}
	return resp
}

func (h *Handler) handleCUPSGetPrinters(msg *ipp.Message) *ipp.Message {
	resp := ipp.NewResponseOK(msg.RequestID)
	resp.OperationGroups = append(
		[]*ipp.AttributeGroup{ipp.OpAttrGroup(
			ipp.CharsetAttr("attributes-charset", "utf-8"),
			ipp.NaturalLanguageAttr("attributes-natural-language", "en-us"),
		)},
		ipp.PrinterDescription(h.cfg.PrinterURI)...,
	)
	return resp
}

func (h *Handler) baseOpGroup() []*ipp.AttributeGroup {
	return []*ipp.AttributeGroup{
		ipp.OpAttrGroup(
			ipp.CharsetAttr("attributes-charset", "utf-8"),
			ipp.NaturalLanguageAttr("attributes-natural-language", "en-us"),
		),
	}
}

func (h *Handler) writeResponse(w http.ResponseWriter, msg *ipp.Message) {
	body, err := ipp.EncodeMessage(msg)
	if err != nil {
		log.Printf("encode error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/ipp")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func sanitizeFilename(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			out = append(out, c)
		} else if c == ' ' {
			out = append(out, '_')
		}
	}
	if len(out) > 100 {
		out = out[:100]
	}
	if len(out) == 0 {
		return "untitled"
	}
	return string(out)
}