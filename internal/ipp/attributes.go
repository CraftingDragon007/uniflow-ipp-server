package ipp

func MakeAttribute(tag ValueTag, name string, values ...[]byte) *Attribute {
	return &Attribute{
		Tag:    tag,
		Name:   name,
		Values: values,
	}
}

func StringAttr(name, value string) *Attribute {
	return MakeAttribute(TextWithoutLanguageTag, name, []byte(value))
}

func KeywordAttr(name, value string) *Attribute {
	return MakeAttribute(KeywordValueTag, name, []byte(value))
}

func URIAttr(name, value string) *Attribute {
	return MakeAttribute(URITag, name, []byte(value))
}

func IntAttr(name string, value int32) *Attribute {
	buf := make([]byte, 4)
	buf[0] = byte(value >> 24)
	buf[1] = byte(value >> 16)
	buf[2] = byte(value >> 8)
	buf[3] = byte(value)
	return MakeAttribute(IntegerValueTag, name, buf)
}

func BoolAttr(name string, value bool) *Attribute {
	var v byte
	if value {
		v = 1
	}
	return MakeAttribute(BooleanValueTag, name, []byte{v})
}

func EnumAttr(name string, value int32) *Attribute {
	buf := make([]byte, 4)
	buf[0] = byte(value >> 24)
	buf[1] = byte(value >> 16)
	buf[2] = byte(value >> 8)
	buf[3] = byte(value)
	return MakeAttribute(EnumValueTag, name, buf)
}

func MimeAttr(name, value string) *Attribute {
	return MakeAttribute(MimeMediaTypeTag, name, []byte(value))
}

func CharsetAttr(name, value string) *Attribute {
	return MakeAttribute(CharsetValueTag, name, []byte(value))
}

func NaturalLanguageAttr(name, value string) *Attribute {
	return MakeAttribute(NaturalLanguageValueTag, name, []byte(value))
}

func NoValueAttr(name string) *Attribute {
	return &Attribute{
		Tag:  KeywordValueTag,
		Name: name,
	}
}

func OpAttrGroup(attrs ...*Attribute) *AttributeGroup {
	return &AttributeGroup{
		GroupTag:   OperationAttributesGroup,
		Attributes: attrs,
	}
}

func PrinterAttrGroup(attrs ...*Attribute) *AttributeGroup {
	return &AttributeGroup{
		GroupTag:   PrinterAttributesGroup,
		Attributes: attrs,
	}
}

func JobAttrGroup(attrs ...*Attribute) *AttributeGroup {
	return &AttributeGroup{
		GroupTag:   JobAttributesGroup,
		Attributes: attrs,
	}
}

func UnsupportedAttrGroup(attrs ...*Attribute) *AttributeGroup {
	return &AttributeGroup{
		GroupTag:   UnsupportedAttributesGroup,
		Attributes: attrs,
	}
}

func PrinterDescription(printerURI string) []*AttributeGroup {
	return []*AttributeGroup{
		PrinterAttrGroup(
			CharsetAttr("charset-configured", "utf-8"),
			CharsetAttr("charset-supported", "utf-8"),
			NaturalLanguageAttr("natural-language-configured", "en-us"),
			NaturalLanguageAttr("generated-natural-language-supported", "en-us"),
			KeywordAttr("compression-supported", "none"),
			MimeAttr("document-format-default", "application/pdf"),
			MimeAttr("document-format-supported", "application/pdf"),
			MimeAttr("document-format-supported", "application/octet-stream"),
			MimeAttr("document-format-supported", "image/urf"),
			URIAttr("printer-uri-supported", printerURI),
			URIAttr("printer-uuid", "urn:uuid:uniflow-ipp-server"),
			KeywordAttr("uri-authentication-supported", "none"),
			KeywordAttr("uri-security-supported", "none"),
			IntAttr("printer-state", PrinterStateIdle),
			KeywordAttr("printer-state-reasons", "none"),
			StringAttr("printer-info", "uniFLOW Secure Print"),
			StringAttr("printer-make-and-model", "uniFLOW Online IPP Server"),
			StringAttr("printer-name", "uniflow"),
			BoolAttr("printer-is-accepting-jobs", true),
			KeywordAttr("operations-supported", ""),
			KeywordAttr("operations-supported", "Print-Job"),
			KeywordAttr("operations-supported", "Validate-Job"),
			KeywordAttr("operations-supported", "Get-Printer-Attributes"),
			KeywordAttr("operations-supported", "Get-Jobs"),
			KeywordAttr("operations-supported", "Get-Job-Attributes"),
			IntAttr("copies-default", 1),
			IntAttr("copies-supported", 9999),
			KeywordAttr("sides-default", "one-sided"),
			KeywordAttr("sides-supported", "one-sided"),
			KeywordAttr("sides-supported", "two-sided-long-edge"),
			KeywordAttr("sides-supported", "two-sided-short-edge"),
			KeywordAttr("print-color-mode-default", "color"),
			KeywordAttr("print-color-mode-supported", "auto"),
			KeywordAttr("print-color-mode-supported", "color"),
			KeywordAttr("print-color-mode-supported", "monochrome"),
			KeywordAttr("print-quality-default", "normal"),
			EnumAttr("printer-state", PrinterStateIdle),
			KeywordAttr("media-default", "iso_a4_210x297mm"),
			KeywordAttr("media-supported", "iso_a4_210x297mm"),
			KeywordAttr("media-supported", "na_letter_8.5x11in"),
			KeywordAttr("media-supported", "na_legal_8.5x14in"),
			KeywordAttr("media-supported", "iso_a3_297x420mm"),
			KeywordAttr("media-supported", "iso_a5_148x210mm"),
			KeywordAttr("finishings-default", "none"),
			KeywordAttr("finishings-supported", "none"),
			KeywordAttr("finishings-supported", "staple-top-left"),
			KeywordAttr("finishings-supported", "staple-bottom-left"),
			KeywordAttr("finishings-supported", "staple-top-right"),
			KeywordAttr("finishings-supported", "staple-bottom-right"),
			KeywordAttr("finishings-supported", "staple-dual-left"),
			KeywordAttr("finishings-supported", "punch-dual-left"),
			KeywordAttr("finishings-supported", "punch-triple-left"),
			KeywordAttr("finishings-supported", "punch-quad-left"),
			KeywordAttr("printer-resolution-default", "300dpi"),
			KeywordAttr("printer-resolution-supported", "300dpi"),
			KeywordAttr("output-bin-default", "face-down"),
			KeywordAttr("output-bin-supported", "face-down"),
			KeywordAttr("ipp-versions-supported", "2.0"),
			KeywordAttr("ipp-features-supported", "document-object"),
			KeywordAttr("ipp-features-supported", "job-save"),
		),
	}
}