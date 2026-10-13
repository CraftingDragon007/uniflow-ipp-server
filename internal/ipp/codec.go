package ipp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	DelimiterTag          = 0x01
	IntegerValueTag       = 0x21
	BooleanValueTag       = 0x22
	EnumValueTag          = 0x23
	KeywordValueTag       = 0x44
	URITag                = 0x45
	NameWithoutLanguageTag = 0x42
	NameWithLanguageTag   = 0x49
	CharsetValueTag       = 0x47
	NaturalLanguageValueTag = 0x48
	MimeMediaTypeTag      = 0x49
	OctetStringValueTag   = 0x30
	TextWithoutLanguageTag = 0x41
	TextWithLanguageTag   = 0x46

	EndOfAttributesTag = 0x03

	OperationAttributesGroup   = 0x01
	JobAttributesGroup         = 0x02
	PrinterAttributesGroup     = 0x04
	UnsupportedAttributesGroup = 0x05
)

type ValueTag byte

type Attribute struct {
	Tag   ValueTag
	Name  string
	Values [][]byte
}

type Message struct {
	Version      [2]byte
	Code         int32
	RequestID    int32
	OperationGroups []*AttributeGroup
	Data         []byte
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

	var code int16
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
	var currentTag ValueTag

	buf := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, fmt.Errorf("read tag: %w", err)
		}
		delimiter := buf[0]

		if delimiter == EndOfAttributesTag {
			break
		}

		if delimiter >= OperationAttributesGroup && delimiter <= UnsupportedAttributesGroup {
			if currentGroup != nil && currentAttr != nil {
				currentGroup.Attributes = append(currentGroup.Attributes, currentAttr)
				currentAttr = nil
			}
			currentGroup = &AttributeGroup{GroupTag: delimiter}
			msg.OperationGroups = append(msg.OperationGroups, currentGroup)
			continue
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

		if currentAttr != nil && name != "" && name != currentName {
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
		currentTag = tag
	}
	_ = currentTag

	if currentGroup != nil && currentAttr != nil {
		currentGroup.Attributes = append(currentGroup.Attributes, currentAttr)
	}

	remaining, _ := io.ReadAll(r)
	msg.Data = remaining

	return msg, nil
}

func EncodeMessage(msg *Message) ([]byte, error) {
	var buf bytes.Buffer

	buf.Write(msg.Version[:])

	if err := binary.Write(&buf, binary.BigEndian, int16(msg.Code)); err != nil {
		return nil, err
	}
	if err := binary.Write(&buf, binary.BigEndian, msg.RequestID); err != nil {
		return nil, err
	}

	for _, group := range msg.OperationGroups {
		buf.WriteByte(group.GroupTag)

		for _, attr := range group.Attributes {
			buf.WriteByte(byte(attr.Tag))

			if err := writeInt16(&buf, int16(len(attr.Name))); err != nil {
				return nil, err
			}
			buf.WriteString(attr.Name)

			for i, val := range attr.Values {
				if i > 0 {
					buf.WriteByte(byte(attr.Tag))
					if err := writeInt16(&buf, 0); err != nil {
						return nil, err
					}
				}

				if err := writeInt16(&buf, int16(len(val))); err != nil {
					return nil, err
				}
				buf.Write(val)
			}
		}
	}

	buf.WriteByte(EndOfAttributesTag)

	if msg.Data != nil {
		buf.Write(msg.Data)
	}

	return buf.Bytes(), nil
}

func readInt16(r io.Reader) (int16, error) {
	var v int16
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
	if len(a.Values) == 0 {
		return 0, fmt.Errorf("no values")
	}
	var v int32
	if err := binary.Read(bytes.NewReader(a.Values[0]), binary.BigEndian, &v); err != nil {
		return 0, err
	}
	return v, nil
}