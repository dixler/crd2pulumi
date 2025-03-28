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
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	pschema "github.com/pulumi/pulumi/pkg/v3/codegen/schema"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	unstruct "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DefaultName specifies the default value for the package name
const DefaultName = "crds"
const tool = "crd2pulumi"

const (
	Boolean string = "boolean"
	Integer string = "integer"
	Number  string = "number"
	String  string = "string"
	Array   string = "array"
	Object  string = "object"
)

const anyTypeRef = "pulumi.json#/Any"

var anyTypeSpec = pschema.TypeSpec{
	Ref: anyTypeRef,
}
var arbitraryJSONTypeSpec = pschema.TypeSpec{
	Type:                 Object,
	AdditionalProperties: &anyTypeSpec,
}

var emptySpec = pschema.ComplexTypeSpec{
	ObjectTypeSpec: pschema.ObjectTypeSpec{
		Type:       Object,
		Properties: map[string]pschema.PropertySpec{},
	},
}

const objectMetaRef = "#/types/kubernetes:meta/v1:ObjectMeta"
const objectMetaToken = "kubernetes:meta/v1:ObjectMeta"

// Union type of integer and string
var intOrStringTypeSpec = pschema.TypeSpec{
	OneOf: []pschema.TypeSpec{
		{
			Type: Integer,
		},
		{
			Type: String,
		},
	},
}

func (pg *PackageGenerator) GetTypes() map[string]pschema.ComplexTypeSpec {
	types := map[string]pschema.ComplexTypeSpec{}
	for _, crg := range pg.CustomResourceGenerators {
		for version, schema := range crg.Schemas {
			resourceToken := getToken(crg.Group, version, crg.Kind)
			_, foundProperties, _ := unstruct.NestedMap(schema, "properties")
			if foundProperties {
				AddType(schema, resourceToken, types)
			}
			preserveUnknownFields, _, _ := unstruct.NestedBool(schema, "x-kubernetes-preserve-unknown-fields")
			if preserveUnknownFields {
				types[resourceToken] = emptySpec
			}
			if foundProperties || preserveUnknownFields {
				types[resourceToken].Properties["apiVersion"] = pschema.PropertySpec{
					TypeSpec: pschema.TypeSpec{
						Type: String,
					},
					Const: crg.Group + "/" + version,
				}
				types[resourceToken].Properties["kind"] = pschema.PropertySpec{
					TypeSpec: pschema.TypeSpec{
						Type: String,
					},
					Const: crg.Kind,
				}
				types[resourceToken].Properties["metadata"] = pschema.PropertySpec{
					TypeSpec: pschema.TypeSpec{
						Ref: objectMetaRef,
					},
				}
			}
		}
	}
	return types
}

// Returns the Pulumi package given a types map and a slice of the token types
// of every CustomResource. If includeObjectMetaType is true, then a
// ObjectMetaType type is also generated.
func genPackage(types map[string]pschema.ComplexTypeSpec, resourceTokens []string, includeObjectMetaType bool) (*pschema.Package, error) {
	if includeObjectMetaType {
		types[objectMetaToken] = pschema.ComplexTypeSpec{
			ObjectTypeSpec: pschema.ObjectTypeSpec{
				Type: "object",
			},
		}
	}

	packages := map[string]bool{DefaultName: true, "kubernetes": true}
	resources := map[string]pschema.ResourceSpec{}
	for _, baseRef := range resourceTokens {
		complexTypeSpec := types[baseRef]
		resources[baseRef] = pschema.ResourceSpec{
			ObjectTypeSpec:  complexTypeSpec.ObjectTypeSpec,
			InputProperties: complexTypeSpec.Properties,
		}
		packages[string(tokens.ModuleMember(baseRef).Package())] = true
	}

	allowedPackages := make([]string, 0, len(packages))
	for pkg := range packages {
		allowedPackages = append(allowedPackages, pkg)
	}
	sort.Strings(allowedPackages)

	pkgSpec := pschema.PackageSpec{
		Name:                DefaultName,
		Version:             Version,
		Types:               types,
		Resources:           resources,
		AllowedPackageNames: allowedPackages,
	}

	pkg, err := pschema.ImportSpec(pkgSpec, nil)
	if err != nil {
		return &pschema.Package{}, errors.Wrapf(err, "could not import spec")
	}

	if includeObjectMetaType {
		delete(types, objectMetaToken)
	}

	return pkg, nil
}

// Returns true if the given TypeSpec is of type any; returns false otherwise
func isAnyType(typeSpec pschema.TypeSpec) bool {
	return typeSpec.Ref == anyTypeRef
}

// AddType converts the given OpenAPI `schema` to a ObjectTypeSpec and adds it
// to the `types` map under the given `name`. Recursively converts and adds all
// nested schemas as well.
func AddType(schema map[string]interface{}, name string, types map[string]pschema.ComplexTypeSpec) {
	properties, foundProperties, _ := unstruct.NestedMap(schema, "properties")
	description, _, _ := unstruct.NestedString(schema, "description")
	schemaType, _, _ := unstruct.NestedString(schema, "type")
	required, _, _ := unstruct.NestedStringSlice(schema, "required")

	propertySpecs := map[string]pschema.PropertySpec{}
	for propertyName := range properties {
		propertySchema, _, _ := unstruct.NestedMap(properties, propertyName)
		propertyDescription, _, _ := unstruct.NestedString(propertySchema, "description")
		defaultValue, _, _ := unstruct.NestedFieldNoCopy(propertySchema, "default")
		propertySpecs[propertyName] = pschema.PropertySpec{
			TypeSpec:    GetTypeSpec(propertySchema, name+strings.Title(propertyName), types),
			Description: propertyDescription,
			Default:     defaultValue,
		}
	}

	// If the type wasn't specified but we found properties, then we can infer that the type is an object
	if foundProperties && schemaType == "" {
		schemaType = Object
	}

	types[name] = pschema.ComplexTypeSpec{
		ObjectTypeSpec: pschema.ObjectTypeSpec{
			Type:        schemaType,
			Properties:  propertySpecs,
			Required:    required,
			Description: description,
		}}
}

// GetTypeSpec returns the corresponding pschema.TypeSpec for a OpenAPI v3
// schema. Handles nested pschema.TypeSpecs in case the schema type is an array,
// object, or "combined schema" (oneOf, allOf, anyOf). Also recursively converts
// and adds all schemas of type object to the types map.
func GetTypeSpec(schema map[string]interface{}, name string, types map[string]pschema.ComplexTypeSpec) pschema.TypeSpec {
	if schema == nil {
		return anyTypeSpec
	}

	intOrString, foundIntOrString, _ := unstruct.NestedBool(schema, "x-kubernetes-int-or-string")
	if foundIntOrString && intOrString {
		return intOrStringTypeSpec
	}

	// If the schema is of the `oneOf` type: return a TypeSpec with the `OneOf`
	// field filled with the TypeSpec of all sub-schemas.
	oneOf, foundOneOf, _ := NestedMapSlice(schema, "oneOf")
	if foundOneOf {
		oneOfTypeSpecs := make([]pschema.TypeSpec, 0, len(oneOf))
		for i, oneOfSchema := range oneOf {
			oneOfTypeSpec := GetTypeSpec(oneOfSchema, name+"OneOf"+strconv.Itoa(i), types)
			if isAnyType(oneOfTypeSpec) {
				return anyTypeSpec
			}
			oneOfTypeSpecs = append(oneOfTypeSpecs, oneOfTypeSpec)
		}
		return pschema.TypeSpec{
			OneOf: oneOfTypeSpecs,
		}
	}

	// If the schema is of `allOf` type: combine `properties` and `required`
	// fields of sub-schemas into a single schema. Then return the `TypeSpec`
	// of that combined schema.
	allOf, foundAllOf, _ := NestedMapSlice(schema, "allOf")
	if foundAllOf {
		combinedSchema := CombineSchemas(true, allOf...)
		return GetTypeSpec(combinedSchema, name, types)
	}

	// If the schema is of `anyOf` type: combine only `properties` of
	// sub-schemas into a single schema, with all `properties` set to optional.
	// Then return the `TypeSpec` of that combined schema.
	anyOf, foundAnyOf, _ := NestedMapSlice(schema, "anyOf")
	if foundAnyOf {
		combinedSchema := CombineSchemas(false, anyOf...)
		return GetTypeSpec(combinedSchema, name, types)
	}

	preserveUnknownFields, foundPreserveUnknownFields, _ := unstruct.NestedBool(schema, "x-kubernetes-preserve-unknown-fields")
	if foundPreserveUnknownFields && preserveUnknownFields {
		return arbitraryJSONTypeSpec
	}

	// If the the schema wasn't some combination of other types (`oneOf`,
	// `allOf`, `anyOf`), then it must have a "type" field, otherwise we
	// cannot represent it. If we cannot represent it, we simply set it to be
	// any type.
	schemaType, foundSchemaType, _ := unstruct.NestedString(schema, "type")
	if !foundSchemaType {
		return anyTypeSpec
	}

	switch schemaType {
	case Array:
		items, _, _ := unstruct.NestedMap(schema, "items")
		arrayTypeSpec := GetTypeSpec(items, name, types)
		return pschema.TypeSpec{
			Type:  Array,
			Items: &arrayTypeSpec,
		}
	case Object:
		AddType(schema, name, types)
		// If `additionalProperties` has a sub-schema, then we generate a type for a map from string --> sub-schema type
		additionalProperties, foundAdditionalProperties, _ := unstruct.NestedMap(schema, "additionalProperties")
		if foundAdditionalProperties {
			additionalPropertiesTypeSpec := GetTypeSpec(additionalProperties, name, types)
			return pschema.TypeSpec{
				Type:                 Object,
				AdditionalProperties: &additionalPropertiesTypeSpec,
			}
		}
		// `additionalProperties: true` is equivalent to `additionalProperties: {}`, meaning a map from string -> any
		additionalPropertiesIsTrue, additionalPropertiesIsTrueFound, _ := unstruct.NestedBool(schema, "additionalProperties")
		if additionalPropertiesIsTrueFound && additionalPropertiesIsTrue {
			return pschema.TypeSpec{
				Type:                 Object,
				AdditionalProperties: &anyTypeSpec,
			}
		}
		// If no properties are found, then it can be arbitrary JSON
		_, foundProperties, _ := unstruct.NestedMap(schema, "properties")
		if !foundProperties {
			return arbitraryJSONTypeSpec
		}
		// If properties are found, then we must specify those in a seperate interface
		return pschema.TypeSpec{
			Type: Object,
			Ref:  "#/types/" + name,
		}
	case Integer:
		fallthrough
	case Boolean:
		fallthrough
	case String:
		fallthrough
	case Number:
		return pschema.TypeSpec{
			Type: schemaType,
		}
	default:
		return anyTypeSpec
	}
}

// CombineSchemas combines the `properties` fields of the given sub-schemas into
// a single schema. Returns nil if no schemas are given. Returns the schema if
// only 1 schema is given. If combineRequired == true, then each sub-schema's
// `required` fields are also combined. In this case the combined schema's
// `required` field is of type []interface{}, not []string.
func CombineSchemas(combineRequired bool, schemas ...map[string]interface{}) map[string]interface{} {
	if len(schemas) == 0 {
		return nil
	}
	if len(schemas) == 1 {
		return schemas[0]
	}

	combinedProperties := map[string]interface{}{}
	combinedRequired := make([]string, 0)

	for _, schema := range schemas {
		properties, _, _ := unstruct.NestedMap(schema, "properties")
		for propertyName := range properties {
			propertySchema, _, _ := unstruct.NestedMap(properties, propertyName)
			combinedProperties[propertyName] = propertySchema
		}
		if combineRequired {
			required, foundRequired, _ := unstruct.NestedStringSlice(schema, "required")
			if foundRequired {
				combinedRequired = append(combinedRequired, required...)
			}
		}
	}

	combinedSchema := map[string]interface{}{
		"type":       Object,
		"properties": combinedProperties,
	}
	if combineRequired {
		combinedSchema["required"] = toInterfaceSlice(combinedRequired)
	}
	return combinedSchema
}
