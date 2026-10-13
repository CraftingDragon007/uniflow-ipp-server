package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/uniflow-ipp-server/internal/cloud"
	"github.com/uniflow-ipp-server/internal/ipp"
)

type JobInfo struct {
	ID         int32
	Name       string
	User       string
	PrinterURI string
	CreatedAt  time.Time
	PDFPath    string
	Copies     int32
	Duplex     string
	ColorMode  string
	State      int32
	Reason     string
	Finishings cloud.FinishingOptions
}

type Submitter interface {
	Submit(context.Context, cloud.Job) error
}

type Config struct {
	QueueName  string
	SpoolDir   string
	PrinterURI string
	Submitter  Submitter
}

type Handler struct {
	cfg       Config
	mu        sync.Mutex
	nextJobID int32
	jobs      []*JobInfo
	startedAt time.Time
}

func NewHandler(cfg Config) *Handler {
	return &Handler{
		cfg:       cfg,
		nextJobID: 1,
		jobs:      make([]*JobInfo, 0),
		startedAt: time.Now(),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.URL.Path != "/ipp" && r.URL.Path != "/ipp/" && !strings.HasPrefix(r.URL.Path, "/ipp/jobs/") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)
	msg, err := ipp.DecodeMessage(r.Body)
	if err != nil {
		log.Printf("ipp decode error: %v", err)
		resp := ipp.NewResponse(1, ipp.StatusClientErrorBadRequest)
		h.writeResponse(w, resp)
		return
	}

	log.Printf("received IPP operation: %s (0x%04X)", ipp.OperationName(msg.Code), msg.Code)

	if msg.Version != [2]byte{1, 1} && msg.Version != [2]byte{2, 0} {
		resp := ipp.NewResponse(msg.RequestID, 0x0503)
		resp.OperationGroups = h.baseOpGroup()
		h.writeResponse(w, resp)
		return
	}
	resp := h.handleOperation(r.Context(), msg)
	resp.Version = msg.Version
	log.Printf("responding with status: 0x%04X", resp.Code)
	h.writeResponse(w, resp)
}

func (h *Handler) handleOperation(ctx context.Context, msg *ipp.Message) *ipp.Message {
	switch msg.Code {
	case ipp.OpPrintJob:
		return h.handlePrintJob(ctx, msg)
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

func (h *Handler) handlePrintJob(ctx context.Context, msg *ipp.Message) *ipp.Message {
	finishings, invalid := parseFinishings(msg)
	if invalid != "" {
		return h.invalidJob(msg, invalid)
	}
	if !bytes.HasPrefix(msg.Data, []byte("%PDF-")) {
		log.Print("Print-Job rejected: only PDF payloads are supported")
		resp := ipp.NewResponse(msg.RequestID, ipp.StatusClientErrorDocumentFormat)
		resp.OperationGroups = h.baseOpGroup()
		return resp
	}
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
	h.mu.Unlock()
	now := time.Now()

	spoolDir := h.cfg.SpoolDir
	if spoolDir == "" {
		spoolDir = "/var/spool/uniflow-ipp"
	}
	if err := os.MkdirAll(spoolDir, 0700); err != nil {
		log.Printf("create spool directory: %v", err)
		return h.jobError(msg)
	}

	safeName := sanitizeFilename(jobName)
	file, err := os.CreateTemp(spoolDir, "job-*-"+safeName+".pdf")
	if err != nil {
		log.Printf("create PDF spool file: %v", err)
		return h.jobError(msg)
	}
	pdfPath := file.Name()
	if _, err := file.Write(msg.Data); err != nil {
		file.Close()
		os.Remove(pdfPath)
		log.Printf("failed to save PDF: %v", err)
		return h.jobError(msg)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(pdfPath)
		log.Printf("sync PDF spool file: %v", err)
		return h.jobError(msg)
	}
	if err := file.Close(); err != nil {
		os.Remove(pdfPath)
		log.Printf("close PDF spool file: %v", err)
		return h.jobError(msg)
	}

	job := &JobInfo{
		ID:         jobID,
		Name:       jobName,
		User:       userName,
		PrinterURI: h.cfg.PrinterURI,
		CreatedAt:  now,
		PDFPath:    pdfPath,
		Copies:     finishings.CopyCount,
		Finishings: finishings,
		State:      4,
		Reason:     "job-hold-until-specified",
	}
	h.mu.Lock()
	h.jobs = append(h.jobs, job)
	if h.cfg.Submitter != nil {
		job.State = 5
		job.Reason = "job-outgoing"
	}
	h.mu.Unlock()

	log.Printf("saved job %d to %s", jobID, pdfPath)
	if h.cfg.Submitter != nil {
		err := h.cfg.Submitter.Submit(ctx, cloud.Job{Name: jobName, PDFPath: pdfPath, Finishings: finishings})
		if err != nil {
			h.mu.Lock()
			job.State = 8
			job.Reason = "aborted-by-system"
			h.mu.Unlock()
			log.Printf("cloud submission failed for job %d; PDF retained at %s: %v", jobID, pdfPath, err)
			return h.jobError(msg)
		}
		// The cloud accepted the job. Cleanup errors must not make CUPS retry it.
		removeErr := os.Remove(pdfPath)
		h.mu.Lock()
		job.State = 9
		job.Reason = "job-completed-successfully"
		if removeErr == nil {
			job.PDFPath = ""
		}
		h.mu.Unlock()
		if removeErr != nil {
			log.Printf("cloud accepted job %d, but could not remove local PDF at %s: %v", jobID, pdfPath, removeErr)
		}
	}

	resp := ipp.NewResponseOK(msg.RequestID)
	h.mu.Lock()
	resp.OperationGroups = append(h.baseOpGroup(), h.jobGroup(job))
	h.mu.Unlock()
	return resp
}

func (h *Handler) handleValidateJob(msg *ipp.Message) *ipp.Message {
	if _, invalid := parseFinishings(msg); invalid != "" {
		return h.invalidJob(msg, invalid)
	}
	resp := ipp.NewResponseOK(msg.RequestID)
	resp.OperationGroups = h.baseOpGroup()
	return resp
}

func (h *Handler) invalidJob(msg *ipp.Message, name string) *ipp.Message {
	log.Printf("invalid or unsupported job attribute: %s", name)
	resp := ipp.NewResponse(msg.RequestID, ipp.StatusClientErrorAttributes)
	resp.OperationGroups = h.baseOpGroup()
	for _, tag := range []byte{ipp.OperationAttributesGroup, ipp.JobAttributesGroup} {
		if attr := msg.FindAttribute(tag, name); attr != nil {
			resp.OperationGroups = append(resp.OperationGroups, ipp.UnsupportedAttrGroup(attr))
			break
		}
	}
	return resp
}

func (h *Handler) jobError(msg *ipp.Message) *ipp.Message {
	resp := ipp.NewResponse(msg.RequestID, ipp.StatusServerErrorInternal)
	resp.OperationGroups = h.baseOpGroup()
	return resp
}

func parseFinishings(msg *ipp.Message) (cloud.FinishingOptions, string) {
	opts := cloud.DefaultFinishings()
	if attr := msg.FindAttribute(ipp.OperationAttributesGroup, "document-format"); attr != nil {
		if format := attr.StringValue(); format != "application/pdf" && format != "application/octet-stream" {
			return opts, "document-format"
		}
	}
	if attr := msg.FindAttribute(ipp.JobAttributesGroup, "copies"); attr != nil {
		n, err := attr.IntValue()
		if err != nil || n < 1 || n > 9999 {
			return opts, "copies"
		}
		opts.CopyCount = n
	}
	for _, mapping := range []struct {
		name   string
		values map[string]string
		dest   *string
	}{
		{"sides", map[string]string{"one-sided": "Simplex", "two-sided-long-edge": "DuplexLongEdge", "two-sided-short-edge": "DuplexShortEdge"}, &opts.Duplex},
		{"print-color-mode", map[string]string{"auto": "Automatic", "color": "Color", "monochrome": "BW"}, &opts.Color},
		{"media-source", map[string]string{"auto": "Auto", "tray-1": "Tray1", "tray-2": "Tray2", "tray-3": "Tray3", "tray-4": "Tray4", "tray-5": "Tray5", "by-pass-tray": "MultiPurposeTray"}, &opts.MediaSource},
	} {
		attr := msg.FindAttribute(ipp.JobAttributesGroup, mapping.name)
		if attr == nil && mapping.name == "media-source" {
			if col := msg.FindAttribute(ipp.JobAttributesGroup, "media-col"); col != nil && len(col.Collections) == 1 {
				for _, member := range col.Collections[0] {
					if member.Name == "media-source" {
						attr = member
						break
					}
				}
			}
		}
		if attr != nil {
			value, ok := mapping.values[attr.StringValue()]
			if !ok {
				return opts, mapping.name
			}
			*mapping.dest = value
		}
	}
	if attr := msg.FindAttribute(ipp.JobAttributesGroup, "finishings"); attr != nil {
		for _, value := range attr.Values {
			if len(value) != 4 {
				return opts, "finishings"
			}
			code := binary.BigEndian.Uint32(value)
			if staple, ok := map[uint32]string{
				4: "StaplingLeftUpper", 20: "StaplingLeftUpper", 21: "StaplingLeftLower",
				22: "StaplingRightUpper", 23: "StaplingRightLower", 8: "SaddleStitch",
				28: "StaplingDoubleLeft", 29: "StaplingDoubleTop",
			}[code]; ok {
				if opts.Staple != "NoStapling" && opts.Staple != staple {
					return opts, "finishings"
				}
				opts.Staple = staple
			} else if punch, ok := map[uint32]string{
				5: "ProfessionalPunch", 74: "TwoHolePunch", 78: "ThreeHolePunch", 82: "FourHolePunch",
			}[code]; ok {
				if opts.HolePunch != "NoHolePunch" && opts.HolePunch != punch {
					return opts, "finishings"
				}
				opts.HolePunch = punch
			} else if code != 3 {
				return opts, "finishings"
			}
		}
	}
	return opts, ""
}

func (h *Handler) jobGroup(job *JobInfo) *ipp.AttributeGroup {
	return ipp.JobAttrGroup(
		ipp.IntAttr("job-id", job.ID),
		ipp.URIAttr("job-uri", fmt.Sprintf("%s/jobs/%d", h.cfg.PrinterURI, job.ID)),
		ipp.EnumAttr("job-state", job.State),
		ipp.KeywordAttr("job-state-reasons", job.Reason),
		ipp.NameAttr("job-name", job.Name),
		ipp.NameAttr("job-originating-user-name", job.User),
	)
}

func (h *Handler) handleGetPrinterAttributes(msg *ipp.Message) *ipp.Message {
	resp := ipp.NewResponseOK(msg.RequestID)
	resp.OperationGroups = append(
		[]*ipp.AttributeGroup{ipp.OpAttrGroup(
			ipp.CharsetAttr("attributes-charset", "utf-8"),
			ipp.NaturalLanguageAttr("attributes-natural-language", "en-us"),
		)},
		h.printerDescription()...,
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
		jobGroup := h.jobGroup(job)
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
		uri := msg.FindAttribute(ipp.OperationAttributesGroup, "job-uri")
		if job.ID == jobID || (uri != nil && uri.StringValue() == fmt.Sprintf("%s/jobs/%d", h.cfg.PrinterURI, job.ID)) {
			resp := ipp.NewResponseOK(msg.RequestID)
			resp.OperationGroups = []*ipp.AttributeGroup{
				ipp.OpAttrGroup(
					ipp.CharsetAttr("attributes-charset", "utf-8"),
					ipp.NaturalLanguageAttr("attributes-natural-language", "en-us"),
				),
				h.jobGroup(job),
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
		h.printerDescription()...,
	)
	return resp
}

func (h *Handler) printerDescription() []*ipp.AttributeGroup {
	groups := ipp.PrinterDescription(h.cfg.PrinterURI)
	groups[0].Attributes = append(groups[0].Attributes,
		ipp.IntAttr("printer-up-time", int32(time.Since(h.startedAt).Seconds())),
	)
	return groups
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
