package main

import (
	"bytes"
	"fmt"
	"go/format"
	"go/token"
	"io"
	"io/ioutil"
	"strings"
	"text/template"
	"unicode"

	"github.com/cilium/ebpf"
)

const ebpfModule = "github.com/cilium/ebpf"

const commonRaw = `// Code generated by bpf2go; DO NOT EDIT.
{{- range .Tags }}
// +build {{ . }}
{{- end }}

package {{ .Package }}

import (
	"bytes"
	"fmt"
	"io"

	"{{ .Module }}"
)

type {{ .Name.Specs }} struct {
{{- range $name, $_ := .Programs }}
	Program{{ identifier $name }} *ebpf.ProgramSpec {{ tag $name }}
{{- end }}

{{- range $name, $_ := .Maps }}
	Map{{ identifier $name }} *ebpf.MapSpec {{ tag $name }}
{{- end }}

{{- range $name, $_ := .Sections }}
	Section{{ identifier $name }} *ebpf.MapSpec {{ tag $name }}
{{- end }}
}

func {{ .Name.NewSpecs }}() (*{{ .Name.Specs }}, error) {
	reader := bytes.NewReader({{ .Name.Bytes }})
	spec, err := ebpf.LoadCollectionSpecFromReader(reader)
	if err != nil {
		return nil, fmt.Errorf("can't load {{ .Name }}: %w", err)
	}

	specs := new({{ .Name.Specs }})
	if err := spec.LoadAndAssign(specs, nil); err != nil {
		return nil, fmt.Errorf("can't assign {{ .Name }}: %w", err)
	}

	return specs, nil
}

func (s *{{ .Name.Specs }}) CollectionSpec() *ebpf.CollectionSpec {
	return &ebpf.CollectionSpec{
		Programs: map[string]*ebpf.ProgramSpec{
{{- range $name, $_ := .Programs }}
			"{{ $name }}": s.Program{{ identifier $name }},
{{- end }}
		},
		Maps: map[string]*ebpf.MapSpec{
{{- range $name, $_ := .Maps }}
			"{{ $name }}": s.Map{{ identifier $name }},
{{- end }}
{{- range $name, $_ := .Sections }}
			"{{ $name }}": s.Section{{ identifier $name }},
{{- end }}
		},
	}
}

func (s *{{ .Name.Specs }}) Load(opts *ebpf.CollectionOptions) (*{{ .Name.Objects }}, error) {
	var objs {{ .Name.Objects }}
	if err := s.CollectionSpec().LoadAndAssign(&objs, opts); err != nil {
		return nil, err
	}
	return &objs, nil
}

func (s *{{ .Name.Specs }}) Copy() *{{ .Name.Specs }} {
	return &{{ .Name.Specs }}{
{{- range $name, $_ := .Programs }}
{{- with $id := identifier $name }}
		Program{{ $id }}: s.Program{{ $id }}.Copy(),
{{- end }}
{{- end }}

{{- range $name, $_ := .Maps }}
{{- with $id := identifier $name }}
		Map{{ $id }}: s.Map{{ $id }}.Copy(),
{{- end }}
{{- end }}

{{- range $name, $_ := .Sections }}
{{- with $id := identifier $name }}
		Section{{ $id }}: s.Section{{ $id }}.Copy(),
{{- end }}
{{- end }}
	}
}

type {{ .Name.Objects }} struct {
{{- range $name, $_ := .Programs }}
	Program{{ identifier $name }} *ebpf.Program {{ tag $name }}
{{- end }}

{{- range $name, $_ := .Maps }}
	Map{{ identifier $name }} *ebpf.Map {{ tag $name }}
{{- end }}

{{- range $name, $_ := .Sections }}
	Section{{ identifier $name }} *ebpf.Map {{ tag $name }}
{{- end }}
}

func (o *{{ .Name.Objects }}) Close() error {
	for _, closer := range []io.Closer{
{{- range $name, $_ := .Programs }}
		o.Program{{ identifier $name }},
{{- end }}

{{- range $name, $_ := .Maps }}
		o.Map{{ identifier $name}},
{{- end }}

{{- range $name, $_ := .Sections }}
		o.Section{{ identifier $name}},
{{- end }}
	} {
		if err := closer.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Do not access this directly.
var {{ .Name.Bytes }} = []byte("{{ .Bytes }}")

`

var (
	tplFuncs = map[string]interface{}{
		"identifier": identifier,
		"tag":        tag,
	}
	commonTemplate = template.Must(template.New("common").Funcs(tplFuncs).Parse(commonRaw))
)

type templateName string

func (n templateName) maybeExport(str string) string {
	if token.IsExported(string(n)) {
		return toUpperFirst(str)
	}

	return str
}

func (n templateName) Bytes() string {
	return "_" + toUpperFirst(string(n)) + "Bytes"
}

func (n templateName) Specs() string {
	return n.maybeExport(string(n) + "Specs")
}

func (n templateName) NewSpecs() string {
	return n.maybeExport("new" + toUpperFirst(string(n)) + "Specs")
}

func (n templateName) Objects() string {
	return n.maybeExport(string(n) + "Objects")
}

type writeArgs struct {
	pkg   string
	ident string
	tags  []string
	obj   io.Reader
	out   io.Writer
}

func writeCommon(args writeArgs) error {
	obj, err := ioutil.ReadAll(args.obj)
	if err != nil {
		return fmt.Errorf("read object file contents: %s", err)
	}

	spec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(obj))
	if err != nil {
		return fmt.Errorf("can't load BPF from ELF: %s", err)
	}

	sections := make(map[string]struct{})
	maps := make(map[string]struct{})
	for name := range spec.Maps {
		if strings.HasPrefix(name, ".") {
			sections[name] = struct{}{}
		} else {
			maps[name] = struct{}{}
		}
	}

	ctx := struct {
		Module   string
		Package  string
		Tags     []string
		Name     templateName
		Sections map[string]struct{}
		Maps     map[string]struct{}
		Programs map[string]*ebpf.ProgramSpec
		Bytes    string
	}{
		ebpfModule,
		args.pkg,
		args.tags,
		templateName(args.ident),
		sections,
		maps,
		spec.Programs,
		binaryString(obj),
	}

	var buf bytes.Buffer
	if err := commonTemplate.Execute(&buf, &ctx); err != nil {
		return fmt.Errorf("can't generate types: %s", err)
	}

	return writeFormatted(buf.Bytes(), args.out)
}

func binaryString(buf []byte) string {
	var builder strings.Builder
	for _, b := range buf {
		builder.WriteString(`\x`)
		builder.WriteString(fmt.Sprintf("%02x", b))
	}
	return builder.String()
}

func writeFormatted(src []byte, out io.Writer) error {
	formatted, err := format.Source(src)
	if err != nil {
		return fmt.Errorf("can't format source: %s", err)
	}

	_, err = out.Write(formatted)
	return err
}

func identifier(str string) string {
	prev := rune(-1)
	return strings.Map(func(r rune) rune {
		// See https://golang.org/ref/spec#Identifiers
		switch {
		case unicode.IsLetter(r):
			if prev == -1 {
				r = unicode.ToUpper(r)
			}

		case r == '_':
			switch {
			// The previous rune was deleted, or we are at the
			// beginning of the string.
			case prev == -1:
				fallthrough

			// The previous rune is a lower case letter or a digit.
			case unicode.IsDigit(prev) || (unicode.IsLetter(prev) && unicode.IsLower(prev)):
				// delete the current rune, and force the
				// next character to be uppercased.
				r = -1
			}

		case unicode.IsDigit(r):

		default:
			// Delete the current rune. prev is unchanged.
			return -1
		}

		prev = r
		return r
	}, str)
}

func tag(str string) string {
	return "`ebpf:\"" + str + "\"`"
}
