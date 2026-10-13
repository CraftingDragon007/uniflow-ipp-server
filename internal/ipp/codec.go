package ipp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	DelimiterTag            = 0x01
	IntegerValueTag         = 0x21
	BooleanValueTag         = 0x22
	EnumValueTag            = 0x23
	KeywordValueTag         = 0x44
	URITag                  = 0x45
	NameWithoutLanguageTag  = 0x42
	NameWithLanguageTag     = 0x36
	CharsetValueTag         = 0x47
	NaturalLanguageValueTag = 0x48
	MimeMediaTypeTag        = 0x49
	OctetStringValueTag     = 0x30
	TextWithoutLanguageTag  = 0x41
	TextWithLanguageTag     = 0x35

	EndOfAttributesTag = 0x03

	OperationAttributesGroup   = 0x01
	JobAttributesGroup         = 0x02
	PrinterAttributesGroup     = 0x04
	UnsupportedAttributesGroup = 0x05
)

type ValueTag byte

type Attribute struct {
	Tag         ValueTag
	Name        string
	Values      [][]byte
	Collections [][]*Attribute
}

type Message struct {
	Version         [2]byte
	Code            int32
	RequestID       int32
	OperationGroups []*AttributeGroup
	Data            []byte
}

type AttributeGroup struct {
	GroupTag   byte
	Attributes []*Attribute
}

func DecodeMessage(r io.Reader) (*Message, error) {
	msg := &Message{}

	if _, err := io.ReadFull(r, msg.Version[:]); err != nil {
		return nil, fmt.Errorf("read version: %w", err)
	}

	var code uint16
	if err := binary.Read(r, binary.BigEndian, &code); err != nil {
		return nil, fmt.Errorf("read code: %w", err)
	}
	msg.Code = int32(code)

	var reqID int32
	if err := binary.Read(r, binary.BigEndian, &reqID); err != nil {
		return nil, fmt.Errorf("read request-id: %w", err)
	}
	msg.RequestID = reqID

	var currentGroup *AttributeGroup
	var currentAttr *Attribute
	var currentName string

	buf := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, fmt.Errorf("read tag: %w", err)
		}
		delimiter := buf[0]

		if delimiter == EndOfAttributesTag {
			break
		}

		if delimiter >= OperationAttributesGroup && delimiter <= 0x0f {
			if currentGroup != nil && currentAttr != nil {
				currentGroup.Attributes = append(currentGroup.Attributes, currentAttr)
				currentAttr = nil
			}
			currentGroup = &AttributeGroup{GroupTag: delimiter}
			msg.OperationGroups = append(msg.OperationGroups, currentGroup)
			currentName = ""
			continue
		}
		if currentGroup == nil {
			return nil, fmt.Errorf("attribute without a group")
		}

		tag := ValueTag(delimiter)

		nameLen, err := readInt16(r)
		if err != nil {
			return nil, fmt.Errorf("read name length: %w", err)
		}

		nameBytes := make([]byte, nameLen)
		if _, err := io.ReadFull(r, nameBytes); err != nil {
			return nil, fmt.Errorf("read name: %w", err)
		}
		name := string(nameBytes)

		valueLen, err := readInt16(r)
		if err != nil {
			return nil, fmt.Errorf("read value length: %w", err)
		}

		valueBytes := make([]byte, valueLen)
		if _, err := io.ReadFull(r, valueBytes); err != nil {
			return nil, fmt.Errorf("read value: %w", err)
		}

		if name == "" && (currentAttr == nil || currentAttr.Tag != tag) {
			return nil, fmt.Errorf("invalid attribute continuation")
		}
		if currentAttr != nil && name != "" {
			currentGroup.Attributes = append(currentGroup.Attributes, currentAttr)
			currentAttr = nil
		}

		nameToUse := name
		if nameToUse == "" {
			nameToUse = currentName
		}

		if currentAttr == nil {
			currentAttr = &Attribute{
				Tag:  tag,
				Name: nameToUse,
			}
			currentName = nameToUse
		}
		currentAttr.Values = append(currentAttr.Values, valueBytes)
		if tag == 0x34 {
			if len(valueBytes) != 0 {
				return nil, fmt.Errorf("collection value must be empty")
			}
			members, err := readCollection(r, 0)
			if err != nil {
				return nil, err
			}
			currentAttr.Collections = append(currentAttr.Collections, members)
		}
	}

	if currentGroup != nil && currentAttr != nil {
		currentGroup.Attributes = append(currentGroup.Attributes, currentAttr)
	}

	remaining, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read document: %w", err)
	}
	msg.Data = remaining

	return msg, nil
}

func EncodeMessage(msg *Message) ([]byte, error) {
	if msg == nil || msg.Code < 0 || msg.Code > 65535 {
		return nil, fmt.Errorf("invalid IPP message")
	}
	var buf bytes.Buffer

	buf.Write(msg.Version[:])

	if err := binary.Write(&buf, binary.BigEndian, int16(msg.Code)); err != nil {
		return nil, err
	}
	if err := binary.Write(&buf, binary.BigEndian, msg.RequestID); err != nil {
		return nil, err
	}

	for _, group := range msg.OperationGroups {
		if group == nil {
			return nil, fmt.Errorf("nil attribute group")
		}
		buf.WriteByte(group.GroupTag)

		for _, attr := range group.Attributes {
			if err := writeAttribute(&buf, attr, false, 0); err != nil {
				return nil, err
			}
		}
	}

	buf.WriteByte(EndOfAttributesTag)
	if msg.Data != nil {
		buf.Write(msg.Data)
	}
	return buf.Bytes(), nil
}

func writeAttribute(buf *bytes.Buffer, attr *Attribute, member bool, depth int) error {
	if attr == nil || attr.Name == "" || len(attr.Name) > 65535 || depth > 32 {
		return fmt.Errorf("invalid attribute or collection depth")
	}
	if member {
		writeRecord(buf, 0x4a, "", []byte(attr.Name))
	}
	values := attr.Values
	if attr.Tag == 0x34 {
		if len(attr.Collections) == 0 {
			return fmt.Errorf("collection requires members")
		}
		values = make([][]byte, len(attr.Collections))
	} else if len(values) == 0 {
		values = [][]byte{nil}
	}
	for i, val := range values {
		if len(val) > 65535 {
			return fmt.Errorf("attribute value too long")
		}
		name := ""
		if i == 0 && !member {
			name = attr.Name
		}
		writeRecord(buf, attr.Tag, name, val)
		if attr.Tag == 0x34 {
			for _, child := range attr.Collections[i] {
				if err := writeAttribute(buf, child, true, depth+1); err != nil {
					return err
				}
			}
			writeRecord(buf, 0x37, "", nil)
		}
	}
	return nil
}

func writeRecord(buf *bytes.Buffer, tag ValueTag, name string, value []byte) {
	buf.WriteByte(byte(tag))
	binary.Write(buf, binary.BigEndian, uint16(len(name)))
	buf.WriteString(name)
	binary.Write(buf, binary.BigEndian, uint16(len(value)))
	buf.Write(value)
}

func readCollection(r io.Reader, depth int) ([]*Attribute, error) {
	if depth > 32 {
		return nil, fmt.Errorf("collection nesting too deep")
	}
	var members []*Attribute
	var current *Attribute
	for {
		var tag [1]byte
		if _, err := io.ReadFull(r, tag[:]); err != nil {
			return nil, fmt.Errorf("read collection tag: %w", err)
		}
		nameLen, err := readInt16(r)
		if err != nil || nameLen != 0 {
			return nil, fmt.Errorf("collection records require zero name length")
		}
		length, err := readInt16(r)
		if err != nil {
			return nil, err
		}
		value := make([]byte, length)
		if _, err := io.ReadFull(r, value); err != nil {
			return nil, err
		}
		switch tag[0] {
		case 0x37:
			if length != 0 || (current != nil && len(current.Values) == 0) {
				return nil, fmt.Errorf("invalid endCollection")
			}
			return members, nil
		case 0x4a:
			if length == 0 || (current != nil && len(current.Values) == 0) {
				return nil, fmt.Errorf("invalid collection member name")
			}
			current = &Attribute{Name: string(value)}
			members = append(members, current)
		default:
			if current == nil || tag[0] < 0x10 || (len(current.Values) > 0 && current.Tag != ValueTag(tag[0])) {
				return nil, fmt.Errorf("invalid collection member value")
			}
			current.Tag = ValueTag(tag[0])
			current.Values = append(current.Values, value)
			if tag[0] == 0x34 {
				if length != 0 {
					return nil, fmt.Errorf("collection value must be empty")
				}
				nested, err := readCollection(r, depth+1)
				if err != nil {
					return nil, err
				}
				current.Collections = append(current.Collections, nested)
			}
		}
	}
}

func readInt16(r io.Reader) (uint16, error) {
	var v uint16
	if err := binary.Read(r, binary.BigEndian, &v); err != nil {
		return 0, err
	}
	return v, nil
}

func writeInt16(w io.Writer, v int16) error {
	return binary.Write(w, binary.BigEndian, v)
}

func (m *Message) FindAttribute(groupTag byte, name string) *Attribute {
	for _, g := range m.OperationGroups {
		if g.GroupTag == groupTag {
			for _, a := range g.Attributes {
				if a.Name == name {
					return a
				}
			}
		}
	}
	return nil
}

func (m *Message) FirstOpGroup() *AttributeGroup {
	for _, g := range m.OperationGroups {
		if g.GroupTag == OperationAttributesGroup {
			return g
		}
	}
	return nil
}

func (a *Attribute) StringValue() string {
	if len(a.Values) > 0 {
		return string(a.Values[0])
	}
	return ""
}

func (a *Attribute) IntValue() (int32, error) {
	if len(a.Values) == 0 || len(a.Values[0]) != 4 {
		return 0, fmt.Errorf("integer value must be four bytes")
	}
	var v int32
	if err := binary.Read(bytes.NewReader(a.Values[0]), binary.BigEndian, &v); err != nil {
		return 0, err
	}
	return v, nil
}
