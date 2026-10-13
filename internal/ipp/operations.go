package ipp

const (
	OpPrintJob               int32 = 0x0002
	OpPrintURI               int32 = 0x0003
	OpValidateJob            int32 = 0x0004
	OpCreateJob              int32 = 0x0005
	OpGetJobs                int32 = 0x000A
	OpGetPrinterAttributes   int32 = 0x000B
	OpGetJobAttributes       int32 = 0x0009
	OpIdentifyPrinter        int32 = 0x001C
	OpCUPSGetPrinters        int32 = 0x4002
	OpCUPSGetClasses         int32 = 0x4005
	OpCUPSGetDefault         int32 = 0x4001

	StatusOK                      int32 = 0x0000
	StatusClientErrorBadRequest   int32 = 0x0400
	StatusClientErrorForbidden    int32 = 0x0401
	StatusClientErrorNotPossible  int32 = 0x0404
	StatusClientErrorNotSupported int32 = 0x0510
	StatusServerErrorInternal     int32 = 0x0500

	PrinterStateIdle    = 3
	PrinterStateStopped = 5
)

var DefaultVersion = [2]byte{2, 0}

func NewResponse(requestID int32, statusCode int32) *Message {
	return &Message{
		Version:   DefaultVersion,
		Code:      statusCode,
		RequestID: requestID,
	}
}

func NewResponseOK(requestID int32) *Message {
	return NewResponse(requestID, StatusOK)
}

func OperationName(code int32) string {
	switch code {
	case OpPrintJob:
		return "Print-Job"
	case OpPrintURI:
		return "Print-URI"
	case OpValidateJob:
		return "Validate-Job"
	case OpCreateJob:
		return "Create-Job"
	case OpGetJobs:
		return "Get-Jobs"
	case OpGetPrinterAttributes:
		return "Get-Printer-Attributes"
	case OpGetJobAttributes:
		return "Get-Job-Attributes"
	case OpIdentifyPrinter:
		return "Identify-Printer"
	case OpCUPSGetPrinters:
		return "CUPS-Get-Printers"
	default:
		return "Unknown"
	}
}