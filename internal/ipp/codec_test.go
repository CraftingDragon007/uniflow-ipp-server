package ipp

import (
	"bytes"
	"errors"
	"testing"
)

func TestCodecRoundTrip(t *testing.T) {
	msg := &Message{Version: DefaultVersion, Code: OpPrintJob, RequestID: 99,
		OperationGroups: []*AttributeGroup{
			OpAttrGroup(CharsetAttr("attributes-charset", "utf-8"), MimeAttr("document-format", "application/pdf")),
			JobAttrGroup(MultiEnumAttr("finishings", 20, 74), NoValueAttr("empty")),
		}, Data: []byte("%PDF-test")}
	wire, err := EncodeMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMessage(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.RequestID != 99 || decoded.Code != OpPrintJob || !bytes.Equal(decoded.Data, msg.Data) {
		t.Fatalf("round trip: %+v", decoded)
	}
	attr := decoded.FindAttribute(JobAttributesGroup, "finishings")
	if len(attr.Values) != 2 || attr.Tag != EnumValueTag {
		t.Fatalf("multi-values: %+v", attr)
	}
}

func TestMalformedMessages(t *testing.T) {
	for _, wire := range [][]byte{
		nil,
		{2, 0, 0, 2, 0, 0, 0, 1, 0x21, 0, 1, 'a', 0, 4, 0, 0, 0, 1, 3},
		{2, 0, 0, 2, 0, 0, 0, 1, 1, 0x21, 0, 0, 0, 4, 0, 0, 0, 1, 3},
		{2, 0, 0, 2, 0, 0, 0, 1, 1, 0x21, 0xff, 0xff},
	} {
		if _, err := DecodeMessage(bytes.NewReader(wire)); err == nil {
			t.Fatalf("malformed wire accepted: %x", wire)
		}
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("test body read failure") }

func TestPayloadReadErrorPropagates(t *testing.T) {
	wire := []byte{2, 0, 0, 2, 0, 0, 0, 1, 3}
	if _, err := DecodeMessage(&combinedReader{first: bytes.NewReader(wire)}); err == nil {
		t.Fatal("payload read error swallowed")
	}
}

type combinedReader struct{ first *bytes.Reader }

func (r *combinedReader) Read(p []byte) (int, error) {
	if r.first.Len() > 0 {
		return r.first.Read(p)
	}
	return failedReader{}.Read(p)
}

func TestPrinterDescriptionTypes(t *testing.T) {
	msg := &Message{Version: DefaultVersion, OperationGroups: PrinterDescription("ipp://127.0.0.1:8631/ipp")}
	for name, tag := range map[string]ValueTag{
		"operations-supported": EnumValueTag, "printer-state": EnumValueTag,
		"copies-supported": 0x33, "finishings-supported": EnumValueTag,
		"printer-resolution-supported": 0x32, "document-format-supported": MimeMediaTypeTag,
	} {
		attr := msg.FindAttribute(PrinterAttributesGroup, name)
		if attr == nil || attr.Tag != tag {
			t.Errorf("%s: %+v, want tag %x", name, attr, tag)
		}
	}
	seen := map[string]bool{}
	for _, attr := range msg.OperationGroups[0].Attributes {
		if seen[attr.Name] {
			t.Errorf("duplicate printer attribute %s", attr.Name)
		}
		seen[attr.Name] = true
	}
}

func TestCollectionRoundTrip(t *testing.T) {
	msg := &Message{Version: DefaultVersion, Code: OpPrintJob, RequestID: 1,
		OperationGroups: []*AttributeGroup{JobAttrGroup(
			CollectionAttr("media-col",
				CollectionAttr("media-size", IntAttr("x-dimension", 21000), IntAttr("y-dimension", 29700)),
				KeywordAttr("media-source", "tray-2"),
			),
		)},
	}
	wire, err := EncodeMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeMessage(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	collection := got.FindAttribute(JobAttributesGroup, "media-col")
	if collection == nil || len(collection.Collections) != 1 || len(collection.Collections[0]) != 2 {
		t.Fatalf("collection shape: %+v", collection)
	}
	dimension := collection.Collections[0][0].Collections[0][0]
	n, err := dimension.IntValue()
	if err != nil || n != 21000 {
		t.Fatalf("nested dimension: %d, %v", n, err)
	}
}

func FuzzDecodeMessage(f *testing.F) {
	f.Add([]byte{2, 0, 0, 2, 0, 0, 0, 1, 3})
	f.Add([]byte{2, 0, 0, 2, 0, 0, 0, 1, 0x21})
	f.Fuzz(func(t *testing.T, wire []byte) {
		DecodeMessage(bytes.NewReader(wire))
	})
}
