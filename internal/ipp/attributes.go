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

func NameAttr(name, value string) *Attribute {
	return MakeAttribute(NameWithoutLanguageTag, name, []byte(value))
}

func MultiKeywordAttr(name string, values ...string) *Attribute {
	attr := KeywordAttr(name, values[0])
	for _, value := range values[1:] {
		attr.Values = append(attr.Values, []byte(value))
	}
	return attr
}

func MultiEnumAttr(name string, values ...int32) *Attribute {
	attr := EnumAttr(name, values[0])
	for _, value := range values[1:] {
		attr.Values = append(attr.Values, EnumAttr(name, value).Values[0])
	}
	return attr
}

func CollectionAttr(name string, members ...*Attribute) *Attribute {
	return &Attribute{Tag: 0x34, Name: name, Collections: [][]*Attribute{members}}
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
		Tag:  0x13,
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
			URIAttr("printer-uri-supported", printerURI),
			URIAttr("printer-uuid", "urn:uuid:be79f650-41fb-4a59-bc43-1a12800c8c05"),
			KeywordAttr("uri-authentication-supported", "none"),
			KeywordAttr("uri-security-supported", "none"),
			KeywordAttr("printer-state-reasons", "none"),
			StringAttr("printer-info", "uniFLOW Secure Print"),
			StringAttr("printer-location", "Local Linux client"),
			URIAttr("printer-more-info", printerURI),
			StringAttr("printer-make-and-model", "uniFLOW Online IPP Server"),
			NameAttr("printer-name", "uniflow"),
			BoolAttr("printer-is-accepting-jobs", true),
			MultiEnumAttr("operations-supported", OpPrintJob, OpValidateJob, OpGetPrinterAttributes, OpGetJobs, OpGetJobAttributes, OpIdentifyPrinter, OpCUPSGetPrinters),
			IntAttr("copies-default", 1),
			MakeAttribute(0x33, "copies-supported", append(IntAttr("", 1).Values[0], IntAttr("", 9999).Values[0]...)),
			KeywordAttr("sides-default", "one-sided"),
			MultiKeywordAttr("sides-supported", "one-sided", "two-sided-long-edge", "two-sided-short-edge"),
			KeywordAttr("print-color-mode-default", "color"),
			MultiKeywordAttr("print-color-mode-supported", "auto", "color", "monochrome"),
			EnumAttr("print-quality-default", 4),
			EnumAttr("print-quality-supported", 4),
			EnumAttr("printer-state", PrinterStateIdle),
			KeywordAttr("media-default", "iso_a4_210x297mm"),
			CollectionAttr("media-col-default",
				CollectionAttr("media-size", IntAttr("x-dimension", 21000), IntAttr("y-dimension", 29700)),
				IntAttr("media-bottom-margin", 423), IntAttr("media-top-margin", 423),
				IntAttr("media-left-margin", 423), IntAttr("media-right-margin", 423),
			),
			MultiKeywordAttr("media-supported", "iso_a4_210x297mm", "na_letter_8.5x11in", "na_legal_8.5x14in", "iso_a3_297x420mm", "iso_a5_148x210mm"),
			EnumAttr("finishings-default", 3),
			MultiEnumAttr("finishings-supported", 3, 4, 5, 8, 20, 21, 22, 23, 28, 29, 74, 78, 82),
			MakeAttribute(0x32, "printer-resolution-default", []byte{0, 0, 1, 44, 0, 0, 1, 44, 3}),
			MakeAttribute(0x32, "printer-resolution-supported", []byte{0, 0, 1, 44, 0, 0, 1, 44, 3}),
			KeywordAttr("output-bin-default", "face-down"),
			KeywordAttr("output-bin-supported", "face-down"),
			MultiKeywordAttr("ipp-versions-supported", "1.1", "2.0"),
		),
	}
}
