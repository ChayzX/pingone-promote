// Package hclblocks finds top-level HCL blocks by byte range and edits files
// surgically: replacing, inserting or removing one block never changes a
// byte outside that block. This is the mechanism behind the additive merge
// (ADR 0015, "Scoped export and additive merge"): Terraform files in a
// config repo are never re-rendered as a whole.
package hclblocks

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// MaxFileSize bounds one parsed file.
const MaxFileSize = 64 << 20

// Block is one top-level block and its byte range in the file.
type Block struct {
	Type   string
	Labels []string
	// Start and End delimit the block text, from its type keyword to the
	// closing brace (End is exclusive).
	Start, End int
}

// Key is the block's identity: "resource.<type>.<label>" style, joined by
// dots. For resource blocks Address is more useful.
func (b Block) Key() string { return strings.Join(append([]string{b.Type}, b.Labels...), ".") }

// Address is "<type>.<label>" for resource and data blocks ("data." prefix
// for data), the label for variable/output/module blocks, and "" otherwise.
func (b Block) Address() string {
	switch {
	case b.Type == "resource" && len(b.Labels) == 2:
		return b.Labels[0] + "." + b.Labels[1]
	case b.Type == "data" && len(b.Labels) == 2:
		return "data." + b.Labels[0] + "." + b.Labels[1]
	case len(b.Labels) == 1:
		return b.Labels[0]
	}
	return ""
}

// ParseError is a syntax error; it carries the position, never the text.
type ParseError struct {
	File string
	Line int
	Msg  string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Msg)
}

// Parse parses src as HCL native syntax.
func Parse(src []byte, filename string) (*hclsyntax.Body, error) {
	if len(src) > MaxFileSize {
		return nil, &ParseError{File: filename, Msg: "file exceeds the size limit"}
	}
	f, diags := hclsyntax.ParseConfig(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		d := diags[0]
		line := 0
		if d.Subject != nil {
			line = d.Subject.Start.Line
		}
		return nil, &ParseError{File: filename, Line: line, Msg: d.Summary}
	}
	body, ok := f.Body.(*hclsyntax.Body)
	if !ok {
		return nil, &ParseError{File: filename, Msg: "not native HCL syntax"}
	}
	return body, nil
}

// Scan returns the file's top-level blocks in file order. Top-level
// attributes (as in a tfvars file) are allowed and ignored.
func Scan(src []byte, filename string) ([]Block, error) {
	body, err := Parse(src, filename)
	if err != nil {
		return nil, err
	}
	out := make([]Block, 0, len(body.Blocks))
	for _, b := range body.Blocks {
		r := b.Range()
		out = append(out, Block{Type: b.Type, Labels: append([]string(nil), b.Labels...), Start: r.Start.Byte, End: r.End.Byte})
	}
	return out, nil
}

// Text returns the block's bytes.
func (b Block) Text(src []byte) []byte { return src[b.Start:b.End] }

// Normalize returns block text in a canonical form for comparison:
// formatted with hclwrite.Format, trailing whitespace trimmed.
func Normalize(text []byte) []byte {
	return bytes.TrimSpace(hclwrite.Format(bytes.TrimSpace(text)))
}

// Equal reports whether two block texts are the same after Normalize.
func Equal(a, b []byte) bool { return bytes.Equal(Normalize(a), Normalize(b)) }

// Edit replaces src[Start:End] with Text.
type Edit struct {
	Start, End int
	Text       []byte
}

// Apply applies non-overlapping edits. Edits may be in any order.
func Apply(src []byte, edits []Edit) ([]byte, error) {
	es := append([]Edit(nil), edits...)
	sort.SliceStable(es, func(i, j int) bool { return es[i].Start < es[j].Start })
	var out bytes.Buffer
	pos := 0
	for _, e := range es {
		if e.Start < pos || e.End < e.Start || e.End > len(src) {
			return nil, errors.New("hclblocks: overlapping or out-of-range edit")
		}
		out.Write(src[pos:e.Start])
		out.Write(e.Text)
		pos = e.End
	}
	out.Write(src[pos:])
	return out.Bytes(), nil
}

// File is an editable Terraform file.
type File struct {
	Name   string
	Src    []byte
	Blocks []Block
}

// Load parses a file for editing.
func Load(name string, src []byte) (*File, error) {
	blocks, err := Scan(src, name)
	if err != nil {
		return nil, err
	}
	return &File{Name: name, Src: src, Blocks: blocks}, nil
}

// Find returns the index of the block with the given key, or -1. A key
// appearing more than once is an error (Terraform rejects it anyway).
func (f *File) Find(key string) (int, error) {
	found := -1
	for i, b := range f.Blocks {
		if b.Key() == key {
			if found >= 0 {
				return -1, fmt.Errorf("%s: %s is declared more than once", f.Name, key)
			}
			found = i
		}
	}
	return found, nil
}

// splice replaces f.Src[start:end] with ins, whose own blocks (parsed
// alone) are at offset at within ins. Blocks outside [start, end) keep
// their text and only move, so the file is not parsed again: inserting or
// replacing whole top-level blocks between others cannot change how the
// others parse. ok is false when ins is not made of whole blocks (the
// caller then parses the whole file).
func (f *File) splice(start, end int, ins []byte) (bool, error) {
	if len(f.Src)-(end-start)+len(ins) > MaxFileSize {
		return false, &ParseError{File: f.Name, Msg: "file exceeds the size limit"}
	}
	inner, err := Scan(ins, f.Name)
	if err != nil {
		return false, fmt.Errorf("hclblocks: edit produced invalid HCL in %s: %w", f.Name, err)
	}
	// Only blocks and whitespace: nothing else whose meaning could depend
	// on its neighbours.
	rest := ins
	for i := len(inner) - 1; i >= 0; i-- {
		rest = append(append([]byte(nil), rest[:inner[i].Start]...), rest[inner[i].End:]...)
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return false, nil
	}
	for _, b := range f.Blocks {
		if b.Start < end && b.End > start {
			if b.Start < start || b.End > end {
				return false, nil // the edit cuts a block: parse everything
			}
		}
	}
	src := make([]byte, 0, len(f.Src)-(end-start)+len(ins))
	src = append(src, f.Src[:start]...)
	src = append(src, ins...)
	src = append(src, f.Src[end:]...)
	shift := len(ins) - (end - start)
	blocks := make([]Block, 0, len(f.Blocks)+len(inner))
	for _, b := range f.Blocks {
		if b.End <= start {
			blocks = append(blocks, b)
		}
	}
	for _, b := range inner {
		b.Start += start
		b.End += start
		blocks = append(blocks, b)
	}
	for _, b := range f.Blocks {
		if b.Start >= end {
			b.Start += shift
			b.End += shift
			blocks = append(blocks, b)
		}
	}
	f.Src, f.Blocks = src, blocks
	return true, nil
}

// edit applies a splice, falling back to parsing the whole file.
func (f *File) edit(start, end int, ins []byte) error {
	ok, err := f.splice(start, end, ins)
	if err != nil || ok {
		return err
	}
	src := append(append(append([]byte(nil), f.Src[:start]...), ins...), f.Src[end:]...)
	return f.reload(src)
}

func (f *File) reload(src []byte) error {
	blocks, err := Scan(src, f.Name)
	if err != nil {
		return fmt.Errorf("hclblocks: edit produced invalid HCL in %s: %w", f.Name, err)
	}
	f.Src, f.Blocks = src, blocks
	return nil
}

// Replace swaps the text of the block with key for text. Bytes outside the
// block are unchanged.
func (f *File) Replace(key string, text []byte) error {
	i, err := f.Find(key)
	if err != nil {
		return err
	}
	if i < 0 {
		return fmt.Errorf("%s: %s not found", f.Name, key)
	}
	b := f.Blocks[i]
	return f.edit(b.Start, b.End, bytes.TrimSpace(text))
}

// Insert adds a block. When sortWithin is true the block goes before the
// first block of the same type whose key sorts after it (terraformer keeps
// resource files sorted by label); otherwise, and when there is no such
// block, it is appended at the end. Existing bytes are never changed except
// that a missing final newline is added before appending.
func (f *File) Insert(key string, text []byte, sortWithin bool) error {
	if i, err := f.Find(key); err != nil {
		return err
	} else if i >= 0 {
		return fmt.Errorf("%s: %s already exists", f.Name, key)
	}
	text = bytes.TrimSpace(text)
	typ := strings.SplitN(key, ".", 2)[0]
	if sortWithin {
		for _, b := range f.Blocks {
			if b.Type == typ && b.Key() > key {
				return f.edit(b.Start, b.Start, append(append([]byte(nil), text...), "\n\n"...))
			}
		}
	}
	return f.appendGroup(text)
}

// appendGroup appends text after a blank line (alone when the file is
// empty), ending with a newline.
func (f *File) appendGroup(text []byte) error {
	if len(bytes.TrimSpace(f.Src)) == 0 {
		return f.edit(0, len(f.Src), append(append([]byte(nil), text...), '\n'))
	}
	var ins []byte
	if !bytes.HasSuffix(f.Src, []byte("\n")) {
		ins = append(ins, '\n')
	}
	ins = append(ins, '\n')
	ins = append(ins, text...)
	ins = append(ins, '\n')
	return f.edit(len(f.Src), len(f.Src), ins)
}

// Remove deletes a block and the blank lines that follow it.
func (f *File) Remove(key string) error {
	i, err := f.Find(key)
	if err != nil {
		return err
	}
	if i < 0 {
		return fmt.Errorf("%s: %s not found", f.Name, key)
	}
	b := f.Blocks[i]
	end := b.End
	for end < len(f.Src) && (f.Src[end] == '\n' || f.Src[end] == ' ' || f.Src[end] == '\t' || f.Src[end] == '\r') {
		end++
	}
	if end == len(f.Src) {
		// Keep exactly one final newline after the previous content.
		start := b.Start
		for start > 0 && (f.Src[start-1] == '\n' || f.Src[start-1] == ' ') {
			start--
		}
		out := append(append([]byte(nil), f.Src[:start]...), '\n')
		if start == 0 {
			out = nil
		}
		return f.reload(out)
	}
	src, err := Apply(f.Src, []Edit{{Start: b.Start, End: end}})
	if err != nil {
		return err
	}
	return f.reload(src)
}

// AppendToBlock adds body lines (already indented for the block) just
// before the closing brace of the block with key, as their own group after
// a blank line, so terraform fmt never realigns the existing attributes.
func (f *File) AppendToBlock(key string, lines []byte) error {
	i, err := f.Find(key)
	if err != nil {
		return err
	}
	if i < 0 {
		return fmt.Errorf("%s: %s not found", f.Name, key)
	}
	b := f.Blocks[i]
	closing := b.End - 1 // the "}"
	lineStart := closing
	for lineStart > b.Start && f.Src[lineStart-1] != '\n' {
		lineStart--
	}
	ins := []byte{}
	inner := bytes.TrimSpace(f.Src[b.Start:lineStart])
	if !bytes.HasSuffix(inner, []byte("{")) {
		ins = append(ins, '\n')
	}
	ins = append(ins, bytes.TrimRight(lines, "\n")...)
	ins = append(ins, '\n')
	if lineStart == b.Start || len(bytes.TrimSpace(f.Src[lineStart:closing])) > 0 {
		return fmt.Errorf("%s: %s has an unusual closing brace; edit it by hand", f.Name, key)
	}
	src, err := Apply(f.Src, []Edit{{Start: lineStart, End: lineStart, Text: ins}})
	if err != nil {
		return err
	}
	return f.reload(src)
}

// AppendText appends a group of top-level lines (attributes, for a tfvars
// file) after a blank line.
func (f *File) AppendText(text []byte) error {
	return f.appendGroup(bytes.TrimSpace(text))
}
