package tests

import (
	"github.com/moredure/easyjson"
	"github.com/moredure/easyjson/jlexer"
	"github.com/moredure/easyjson/jwriter"
)

//easyjson:json
type NestedMarshaler struct {
	Value easyjson.MarshalerUnmarshaler
	Value2 int
}

type StructWithMarshaler struct {
	Value int
}

func (s *StructWithMarshaler) UnmarshalEasyJSON(w *jlexer.Lexer) {
	s.Value = w.Int()
}

func (s *StructWithMarshaler) MarshalEasyJSON(w *jwriter.Writer) {
	w.Int(s.Value)
}

