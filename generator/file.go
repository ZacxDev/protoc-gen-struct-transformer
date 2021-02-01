package generator

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/innovation-upstream/protoc-gen-struct-transformer/options"
	"github.com/innovation-upstream/protoc-gen-struct-transformer/source"
	"github.com/gogo/protobuf/proto"
	"github.com/gogo/protobuf/protoc-gen-gogo/descriptor"
	plugin "github.com/gogo/protobuf/protoc-gen-gogo/plugin"
)

var (
	// header is a header for each generated files.
	header = "// Code generated by protoc-gen-struct-transformer, version: %s. DO NOT EDIT.\n"

	// Next three variables are set by "make install" command and are used as
	// version information. See Makefile for details.
	version   = "<dev>"
	buildTime = "<build_time>"
)

// WriteStringer exposes two methods:
// Write(p []byte) (n int, err error)
// String() string.
type WriteStringer interface {
	io.Writer
	fmt.Stringer
}

// Version returns current generator version.
func Version() string {
	return fmt.Sprintf("version: %s\nbuild-time: %s\n", version, buildTime)
}

// output initializes io.Writer with information about current version.
func output() WriteStringer {
	return bytes.NewBufferString(fmt.Sprintf(header, version))
}

// fileHeader adds source file/package info into initialized header.
func fileHeader(srcFileName, srcFilePackage, dstPackage string) WriteStringer {
	w := output()

	fmt.Fprintln(w, "// source file:", srcFileName)
	fmt.Fprintln(w, "// source package:", srcFilePackage)
	fmt.Fprintln(w, "\npackage", dstPackage)

	return w
}

// CollectAllMessages processes all files passed within plugin request to
// collect info about all incoming messages. Generator should have information
// about all messages regardless have those messages transformer options or
// haven't.
func CollectAllMessages(req plugin.CodeGeneratorRequest) (MessageOptionList, error) {
	mol := MessageOptionList{}

	for _, f := range req.ProtoFile {
		for _, m := range f.MessageType {
			structName, _ := extractStructNameOption(m)

			so := messageOption{
				targetName: structName,
			}

			if len(m.OneofDecl) > 0 {
				hasInt64Value := false
				hasStringValue := false
				// Check if it implements a specific case of migration from Int64ToString
				for _, field := range m.Field {
					if field.Name != nil {
						if *field.Name == "int64_value" {
							hasInt64Value = true
						}
						if *field.Name == "string_value" {
							hasStringValue = true
						}
					}
				}

				int64ToStringOneOf := len(m.Field) == 2 && hasInt64Value && hasStringValue

				if int64ToStringOneOf && len(m.OneofDecl) == 1 {
					so.oneofDecl = *m.OneofDecl[0].Name
				}
			}

			mol[fmt.Sprintf("%s.%s", *f.Package, *m.Name)] = so
		}
	}

	return mol, nil
}

// modelsPath returns absolute path to file with models or an error if
// transformer.go_models_file_path option not found.
func modelsPath(m proto.Message) (string, error) {
	optionPath, err := getStringOption(m, options.E_GoModelsFilePath)
	if err != nil {
		return "", ErrFileSkipped
	}

	path, err := filepath.Abs(optionPath)
	if err != nil {
		return "", err
	}

	return path, nil
}

// ProcessFile processes .proto file and returns content as a string.
func ProcessFile(f *descriptor.FileDescriptorProto, packageName, helperPackageName *string, messages MessageOptionList, debug, usePackageInPath bool) (string, string, error) {
	path, err := modelsPath(f.Options)
	if err != nil {
		return "", "", err
	}

	structs, err := source.Parse(path, nil)
	if err != nil {
		return "", "", err
	}

	w := fileHeader(*f.Name, *f.Package, *packageName)

	if debug {
		p(w, "%s", messages)
	}

	repoPackage, err := getStringOption(f.Options, options.E_GoRepoPackage)
	if err != nil {
		repoPackage = "repo1"
	}

	protoPackage, err := getStringOption(f.Options, options.E_GoProtobufPackage)
	if err != nil {
		protoPackage = "pb1"
	}

	var data []*Data

	for _, m := range f.MessageType {
		fields, sno, err := processMessage(w, m, messages, structs, debug)
		if err != nil {
			if e, ok := err.(loggableError); ok {
				p(w, "// %s\n", e)
				continue
			}
			return "", "", err
		}

		prefixFields(fields, *helperPackageName)

		data = append(data,
			&Data{
				Src:        m.GetName(),
				SrcPref:    protoPackage,
				SrcFn:      "Pb",
				SrcPointer: "*",
				Dst:        sno,
				DstPref:    repoPackage,
				DstFn:      sno,
				Fields:     fields,
			})
	}

	if err := execTemplate(w, data); err != nil {
		return "", "", err
	}

	if err := processOneofFields(w, data); err != nil {
		return "", "", err
	}

	dir, filename := filepath.Split(*f.Name)
	pn := ""
	if usePackageInPath {
		pn = *packageName
	}
	fmt.Println(*f.Name)
	absPath := strings.Replace(filepath.Join(dir, pn, filename, "__", *f.Name), ".proto", "_transformer.go", -1)

	return absPath, w.String(), nil
}

// execTemplate executes main template twice with given data, second pass is
// used for generated reverse functions.
func execTemplate(w io.Writer, data []*Data) error {
	for _, d := range data {
		t, err := templateWithHelpers("messages")
		if err != nil {
			return err
		}

		if err := t.Execute(w, d); err != nil {
			return err
		}

		d.swap()

		if err := t.Execute(w, d); err != nil {
			return err
		}
	}

	return nil
}

// prefixFields adds prefix to fields' convertor functions if prefix is not an
// empty string and field has an attribute UsePackage == true,
func prefixFields(fields []Field, prefix string) {
	if prefix == "" {
		return
	}

	for i, f := range fields {
		if !f.UsePackage {
			continue
		}
		fields[i].ProtoToGoType = prefix + "." + f.ProtoToGoType
		fields[i].GoToProtoType = prefix + "." + f.GoToProtoType
	}
}
