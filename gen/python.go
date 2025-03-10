// Copyright 2016-2020, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gen

import (
	"bytes"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/pulumi/pulumi/pkg/v3/codegen/python"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
)

const pythonMetaFile = `from pulumi_kubernetes.meta.v1._inputs import *
import pulumi_kubernetes.meta.v1.outputs
`

func (pg *PackageGenerator) genPython(outputDir, name string) error {
	if files, err := pg.genPythonFiles(name); err != nil {
		return err
	} else if err := writeFiles(files, outputDir); err != nil {
		return err
	}
	return nil
}

func (pg *PackageGenerator) genPythonFiles(name string) (map[string]*bytes.Buffer, error) {
	pkg := pg.SchemaPackageWithObjectMetaType()

	oldName := pkg.Name
	pkg.Name = name
	pkg.Language[Python] = rawMessage(map[string]interface{}{
		"compatibility":       "kubernetes20",
		"moduleNameOverrides": pg.moduleToPackage(),
		"requires": map[string]string{
			"pulumi":   "\u003e=3.0.0,\u003c4.0.0",
			"pyyaml":   "\u003e=5.3",
			"requests": "\u003e=2.21.0,\u003c2.22.0",
		},
		"ignorePyNamePanic": true,
	})

	files, err := python.GeneratePackage(tool, pkg, nil)
	if err != nil {
		return nil, errors.Wrap(err, "could not generate Go package")
	}

	pkg.Name = oldName
	delete(pkg.Language, Python)

	pythonPackageDir := "pulumi_" + name

	// Remove unneeded files
	var unneededPythonFiles = []string{
		filepath.Join(pythonPackageDir, "README.md"),
	}
	for _, unneededFile := range unneededPythonFiles {
		delete(files, unneededFile)
	}

	// Replace _utilities.py with our own hard-coded version
	utilitiesPath := filepath.Join(pythonPackageDir, "_utilities.py")
	_, ok := files[utilitiesPath]
	contract.Assertf(ok, "missing _utilities.py file")
	files[utilitiesPath] = []byte(pythonUtilitiesFile)

	// Import the actual SDK ObjectMeta types in place of our placeholder ones
	if pg.HasSchemas() {
		metaPath := filepath.Join(pythonPackageDir, "meta/v1", "__init__.py")
		code, ok := files[metaPath]
		contract.Assertf(ok, "missing meta/v1/__init__.py file")
		files[metaPath] = append(code, []byte(pythonMetaFile)...)
	}

	buffers := map[string]*bytes.Buffer{}
	for name, code := range files {
		buffers[name] = bytes.NewBuffer(code)
	}
	return buffers, nil
}

const pythonUtilitiesFile = `from pulumi_kubernetes import _utilities


def get_env(*args):
    return _utilities.get_env(*args)


def get_env_bool(*args):
    return _utilities.get_env_bool(*args)


def get_env_int(*args):
    return _utilities.get_env_int(*args)


def get_env_float(*args):
    return _utilities.get_env_float(*args)


def get_version():
    return _utilities.get_version()
`
