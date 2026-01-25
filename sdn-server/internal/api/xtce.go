// Package api provides HTTP API endpoints for the SDN server.
package api

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
)

var log = logging.Logger("sdn-api")

// XTCEHandler handles XTCE XML ingestion and conversion.
type XTCEHandler struct {
	converter *XTCEConverter
	mu        sync.RWMutex
}

// NewXTCEHandler creates a new XTCE handler.
func NewXTCEHandler() *XTCEHandler {
	return &XTCEHandler{
		converter: NewXTCEConverter(),
	}
}

// ServeHTTP handles HTTP requests for XTCE ingestion.
func (h *XTCEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.handleConvert(w, r)
	case http.MethodGet:
		h.handleInfo(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleConvert handles POST requests to convert XTCE to JSON Schema.
func (h *XTCEHandler) handleConvert(w http.ResponseWriter, r *http.Request) {
	// Read request body
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024)) // 10MB max
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read request body: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Determine content type
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/xml"
	}

	// Parse options from query parameters
	options := ConversionOptions{
		Namespace:        r.URL.Query().Get("namespace"),
		SchemaID:         r.URL.Query().Get("schema_id"),
		IncludeTelemetry: r.URL.Query().Get("telemetry") != "false",
		IncludeCommands:  r.URL.Query().Get("commands") != "false",
		GenerateEnums:    r.URL.Query().Get("enums") != "false",
	}

	// Convert XTCE to JSON Schema
	result, err := h.converter.Convert(r.Context(), string(body), options)
	if err != nil {
		http.Error(w, fmt.Sprintf("Conversion failed: %v", err), http.StatusBadRequest)
		return
	}

	// Determine response format
	accept := r.Header.Get("Accept")
	switch {
	case strings.Contains(accept, "application/x-flatbuffers"):
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(result.FlatBufferSchema))
	case strings.Contains(accept, "text/plain"):
		// Return both JSON Schema and FlatBuffer schema
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "=== JSON Schema ===\n%s\n\n=== FlatBuffer Schema ===\n%s\n",
			result.JSONSchemaString, result.FlatBufferSchema)
	default:
		// Default to JSON Schema
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(result.JSONSchemaString))
	}

	log.Infof("Converted XTCE document: %s (%d parameters, %d commands)",
		result.Name, result.TelemetryCount, result.CommandCount)
}

// handleInfo returns information about the API.
func (h *XTCEHandler) handleInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]interface{}{
		"name":        "XTCE Ingestion API",
		"version":     "1.0.0",
		"description": "Convert XTCE XML to JSON Schema with x-flatbuffer annotations",
		"endpoints": map[string]interface{}{
			"POST /api/ingest/xtce": map[string]interface{}{
				"description":  "Convert XTCE XML to JSON Schema",
				"content_type": "application/xml or text/xml",
				"parameters": map[string]string{
					"namespace":  "FlatBuffer namespace (optional)",
					"schema_id":  "JSON Schema $id (optional)",
					"telemetry":  "Include telemetry parameters (default: true)",
					"commands":   "Include command definitions (default: true)",
					"enums":      "Generate FlatBuffer enums (default: true)",
				},
				"accept": map[string]string{
					"application/json":          "Returns JSON Schema (default)",
					"application/x-flatbuffers": "Returns FlatBuffer schema",
					"text/plain":                "Returns both schemas",
				},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// ConversionOptions holds options for XTCE conversion.
type ConversionOptions struct {
	Namespace        string
	SchemaID         string
	IncludeTelemetry bool
	IncludeCommands  bool
	GenerateEnums    bool
	FieldIDOffset    int
}

// ConversionResult holds the result of XTCE conversion.
type ConversionResult struct {
	Name              string
	Description       string
	JSONSchemaString  string
	FlatBufferSchema  string
	TelemetryCount    int
	CommandCount      int
	Warnings          []string
}

// XTCEConverter converts XTCE XML to JSON Schema and FlatBuffer schema.
type XTCEConverter struct {
	mu sync.Mutex
}

// NewXTCEConverter creates a new XTCE converter.
func NewXTCEConverter() *XTCEConverter {
	return &XTCEConverter{}
}

// Convert converts XTCE XML to JSON Schema and FlatBuffer schema.
func (c *XTCEConverter) Convert(ctx context.Context, xmlContent string, options ConversionOptions) (*ConversionResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Parse XTCE XML
	parsed, err := c.parseXTCE(xmlContent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse XTCE: %w", err)
	}

	// Generate JSON Schema
	jsonSchema := c.generateJSONSchema(parsed, options)
	jsonSchemaBytes, err := json.MarshalIndent(jsonSchema, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON Schema: %w", err)
	}

	// Generate FlatBuffer schema
	fbsSchema := c.generateFlatBufferSchema(parsed, options)

	return &ConversionResult{
		Name:              parsed.Name,
		Description:       parsed.Description,
		JSONSchemaString:  string(jsonSchemaBytes),
		FlatBufferSchema:  fbsSchema,
		TelemetryCount:    len(parsed.TelemetryParams),
		CommandCount:      len(parsed.CommandArgs),
		Warnings:          parsed.Warnings,
	}, nil
}

// ParsedXTCE holds parsed XTCE data.
type ParsedXTCE struct {
	Name            string
	Description     string
	ParameterTypes  map[string]*ParameterTypeInfo
	TelemetryParams []*Parameter
	CommandArgs     []*Argument
	Warnings        []string
}

// ParameterTypeInfo holds information about a parameter type.
type ParameterTypeInfo struct {
	Name            string
	Type            string // "integer", "float", "string", "enumerated", "boolean", "time", "binary"
	Description     string
	Signed          bool
	SizeInBits      int
	Encoding        string
	Unit            string
	UnitDescription string
	MinInclusive    *float64
	MaxInclusive    *float64
	Enumerations    []Enumeration
	BooleanLabels   *BooleanLabels
}

// Enumeration holds an enum value.
type Enumeration struct {
	Value       int
	Label       string
	Description string
}

// BooleanLabels holds labels for boolean values.
type BooleanLabels struct {
	Zero string
	One  string
}

// Parameter represents a telemetry parameter.
type Parameter struct {
	Name        string
	TypeRef     string
	Description string
}

// Argument represents a command argument.
type Argument struct {
	Name        string
	TypeRef     string
	Description string
}

// parseXTCE parses XTCE XML content.
func (c *XTCEConverter) parseXTCE(xmlContent string) (*ParsedXTCE, error) {
	// Define XML structures for parsing
	type XMLUnit struct {
		Description string `xml:"description,attr"`
		Text        string `xml:",chardata"`
	}

	type XMLUnitSet struct {
		Units []XMLUnit `xml:"Unit"`
	}

	type XMLIntegerDataEncoding struct {
		SizeInBits string `xml:"sizeInBits,attr"`
		Encoding   string `xml:"encoding,attr"`
	}

	type XMLFloatDataEncoding struct {
		SizeInBits string `xml:"sizeInBits,attr"`
		Encoding   string `xml:"encoding,attr"`
	}

	type XMLFixedSize struct {
		FixedValue string `xml:"FixedValue"`
	}

	type XMLSizeInBits struct {
		Fixed XMLFixedSize `xml:"Fixed"`
	}

	type XMLStringDataEncoding struct {
		Encoding   string       `xml:"encoding,attr"`
		SizeInBits XMLSizeInBits `xml:"SizeInBits"`
	}

	type XMLValidRange struct {
		MinInclusive string `xml:"minInclusive,attr"`
		MaxInclusive string `xml:"maxInclusive,attr"`
	}

	type XMLEnumeration struct {
		Value            string `xml:"value,attr"`
		Label            string `xml:"label,attr"`
		ShortDescription string `xml:"shortDescription,attr"`
	}

	type XMLEnumerationList struct {
		Enumerations []XMLEnumeration `xml:"Enumeration"`
	}

	type XMLIntegerParameterType struct {
		Name                 string                 `xml:"name,attr"`
		Signed               string                 `xml:"signed,attr"`
		SizeInBits           string                 `xml:"sizeInBits,attr"`
		ShortDescription     string                 `xml:"shortDescription,attr"`
		UnitSet              XMLUnitSet             `xml:"UnitSet"`
		IntegerDataEncoding  XMLIntegerDataEncoding `xml:"IntegerDataEncoding"`
		ValidRange           XMLValidRange          `xml:"ValidRange"`
	}

	type XMLFloatParameterType struct {
		Name               string               `xml:"name,attr"`
		SizeInBits         string               `xml:"sizeInBits,attr"`
		ShortDescription   string               `xml:"shortDescription,attr"`
		UnitSet            XMLUnitSet           `xml:"UnitSet"`
		FloatDataEncoding  XMLFloatDataEncoding `xml:"FloatDataEncoding"`
		ValidRange         XMLValidRange        `xml:"ValidRange"`
	}

	type XMLStringParameterType struct {
		Name               string                `xml:"name,attr"`
		ShortDescription   string                `xml:"shortDescription,attr"`
		StringDataEncoding XMLStringDataEncoding `xml:"StringDataEncoding"`
	}

	type XMLEnumeratedParameterType struct {
		Name                string                 `xml:"name,attr"`
		ShortDescription    string                 `xml:"shortDescription,attr"`
		IntegerDataEncoding XMLIntegerDataEncoding `xml:"IntegerDataEncoding"`
		EnumerationList     XMLEnumerationList     `xml:"EnumerationList"`
	}

	type XMLBooleanParameterType struct {
		Name                string                 `xml:"name,attr"`
		ZeroStringValue     string                 `xml:"zeroStringValue,attr"`
		OneStringValue      string                 `xml:"oneStringValue,attr"`
		ShortDescription    string                 `xml:"shortDescription,attr"`
		IntegerDataEncoding XMLIntegerDataEncoding `xml:"IntegerDataEncoding"`
	}

	type XMLParameterTypeSet struct {
		IntegerParameterTypes    []XMLIntegerParameterType    `xml:"IntegerParameterType"`
		FloatParameterTypes      []XMLFloatParameterType      `xml:"FloatParameterType"`
		StringParameterTypes     []XMLStringParameterType     `xml:"StringParameterType"`
		EnumeratedParameterTypes []XMLEnumeratedParameterType `xml:"EnumeratedParameterType"`
		BooleanParameterTypes    []XMLBooleanParameterType    `xml:"BooleanParameterType"`
	}

	type XMLParameter struct {
		Name             string `xml:"name,attr"`
		ParameterTypeRef string `xml:"parameterTypeRef,attr"`
		ShortDescription string `xml:"shortDescription,attr"`
	}

	type XMLParameterSet struct {
		Parameters []XMLParameter `xml:"Parameter"`
	}

	type XMLTelemetryMetaData struct {
		ParameterTypeSet XMLParameterTypeSet `xml:"ParameterTypeSet"`
		ParameterSet     XMLParameterSet     `xml:"ParameterSet"`
	}

	type XMLArgument struct {
		Name            string `xml:"name,attr"`
		ArgumentTypeRef string `xml:"argumentTypeRef,attr"`
		ShortDescription string `xml:"shortDescription,attr"`
	}

	type XMLArgumentList struct {
		Arguments []XMLArgument `xml:"Argument"`
	}

	type XMLMetaCommand struct {
		Name             string          `xml:"name,attr"`
		ShortDescription string          `xml:"shortDescription,attr"`
		ArgumentList     XMLArgumentList `xml:"ArgumentList"`
	}

	type XMLMetaCommandSet struct {
		MetaCommands []XMLMetaCommand `xml:"MetaCommand"`
	}

	type XMLCommandMetaData struct {
		ArgumentTypeSet  XMLParameterTypeSet `xml:"ArgumentTypeSet"`
		MetaCommandSet   XMLMetaCommandSet   `xml:"MetaCommandSet"`
	}

	type XMLSpaceSystem struct {
		Name               string              `xml:"name,attr"`
		ShortDescription   string              `xml:"shortDescription,attr"`
		TelemetryMetaData  XMLTelemetryMetaData `xml:"TelemetryMetaData"`
		CommandMetaData    XMLCommandMetaData   `xml:"CommandMetaData"`
	}

	// Parse XML
	var spaceSystem XMLSpaceSystem
	decoder := xml.NewDecoder(strings.NewReader(xmlContent))

	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("XML parsing error: %w", err)
		}

		if se, ok := token.(xml.StartElement); ok {
			// Handle both namespaced and non-namespaced SpaceSystem
			localName := se.Name.Local
			if localName == "SpaceSystem" {
				// Get attributes
				for _, attr := range se.Attr {
					if attr.Name.Local == "name" {
						spaceSystem.Name = attr.Value
					}
					if attr.Name.Local == "shortDescription" {
						spaceSystem.ShortDescription = attr.Value
					}
				}
				if err := decoder.DecodeElement(&spaceSystem, &se); err != nil {
					return nil, fmt.Errorf("failed to decode SpaceSystem: %w", err)
				}
				break
			}
		}
	}

	// Convert to ParsedXTCE
	result := &ParsedXTCE{
		Name:           spaceSystem.Name,
		Description:    spaceSystem.ShortDescription,
		ParameterTypes: make(map[string]*ParameterTypeInfo),
		Warnings:       []string{},
	}

	// Process parameter types
	for _, t := range spaceSystem.TelemetryMetaData.ParameterTypeSet.IntegerParameterTypes {
		info := &ParameterTypeInfo{
			Name:        t.Name,
			Type:        "integer",
			Description: t.ShortDescription,
			Signed:      t.Signed != "false",
			SizeInBits:  parseInt(t.SizeInBits, 32),
			Encoding:    firstNonEmpty(t.IntegerDataEncoding.Encoding, "unsigned"),
		}
		if len(t.UnitSet.Units) > 0 {
			info.Unit = t.UnitSet.Units[0].Text
			info.UnitDescription = t.UnitSet.Units[0].Description
		}
		if t.ValidRange.MinInclusive != "" {
			v := parseFloat(t.ValidRange.MinInclusive)
			info.MinInclusive = &v
		}
		if t.ValidRange.MaxInclusive != "" {
			v := parseFloat(t.ValidRange.MaxInclusive)
			info.MaxInclusive = &v
		}
		result.ParameterTypes[t.Name] = info
	}

	for _, t := range spaceSystem.TelemetryMetaData.ParameterTypeSet.FloatParameterTypes {
		info := &ParameterTypeInfo{
			Name:        t.Name,
			Type:        "float",
			Description: t.ShortDescription,
			SizeInBits:  parseInt(t.SizeInBits, 32),
			Encoding:    firstNonEmpty(t.FloatDataEncoding.Encoding, "IEEE754_1985"),
		}
		if len(t.UnitSet.Units) > 0 {
			info.Unit = t.UnitSet.Units[0].Text
			info.UnitDescription = t.UnitSet.Units[0].Description
		}
		if t.ValidRange.MinInclusive != "" {
			v := parseFloat(t.ValidRange.MinInclusive)
			info.MinInclusive = &v
		}
		if t.ValidRange.MaxInclusive != "" {
			v := parseFloat(t.ValidRange.MaxInclusive)
			info.MaxInclusive = &v
		}
		result.ParameterTypes[t.Name] = info
	}

	for _, t := range spaceSystem.TelemetryMetaData.ParameterTypeSet.StringParameterTypes {
		info := &ParameterTypeInfo{
			Name:        t.Name,
			Type:        "string",
			Description: t.ShortDescription,
			Encoding:    firstNonEmpty(t.StringDataEncoding.Encoding, "UTF-8"),
		}
		if t.StringDataEncoding.SizeInBits.Fixed.FixedValue != "" {
			info.SizeInBits = parseInt(t.StringDataEncoding.SizeInBits.Fixed.FixedValue, 0)
		}
		result.ParameterTypes[t.Name] = info
	}

	for _, t := range spaceSystem.TelemetryMetaData.ParameterTypeSet.EnumeratedParameterTypes {
		info := &ParameterTypeInfo{
			Name:        t.Name,
			Type:        "enumerated",
			Description: t.ShortDescription,
			SizeInBits:  parseInt(t.IntegerDataEncoding.SizeInBits, 8),
		}
		for _, e := range t.EnumerationList.Enumerations {
			info.Enumerations = append(info.Enumerations, Enumeration{
				Value:       parseInt(e.Value, 0),
				Label:       e.Label,
				Description: e.ShortDescription,
			})
		}
		result.ParameterTypes[t.Name] = info
	}

	for _, t := range spaceSystem.TelemetryMetaData.ParameterTypeSet.BooleanParameterTypes {
		info := &ParameterTypeInfo{
			Name:        t.Name,
			Type:        "boolean",
			Description: t.ShortDescription,
			SizeInBits:  parseInt(t.IntegerDataEncoding.SizeInBits, 1),
			BooleanLabels: &BooleanLabels{
				Zero: firstNonEmpty(t.ZeroStringValue, "false"),
				One:  firstNonEmpty(t.OneStringValue, "true"),
			},
		}
		result.ParameterTypes[t.Name] = info
	}

	// Process parameters
	for _, p := range spaceSystem.TelemetryMetaData.ParameterSet.Parameters {
		result.TelemetryParams = append(result.TelemetryParams, &Parameter{
			Name:        p.Name,
			TypeRef:     p.ParameterTypeRef,
			Description: p.ShortDescription,
		})
	}

	// Process command arguments
	for _, cmd := range spaceSystem.CommandMetaData.MetaCommandSet.MetaCommands {
		for _, arg := range cmd.ArgumentList.Arguments {
			result.CommandArgs = append(result.CommandArgs, &Argument{
				Name:        arg.Name,
				TypeRef:     arg.ArgumentTypeRef,
				Description: arg.ShortDescription,
			})
		}
	}

	return result, nil
}

// generateJSONSchema generates a JSON Schema from parsed XTCE.
func (c *XTCEConverter) generateJSONSchema(parsed *ParsedXTCE, options ConversionOptions) map[string]interface{} {
	schema := map[string]interface{}{
		"$schema":              "https://json-schema.org/draft/2019-09/schema",
		"title":                parsed.Name,
		"description":          parsed.Description,
		"type":                 "object",
		"additionalProperties": false,
	}

	if options.SchemaID != "" {
		schema["$id"] = options.SchemaID
	}

	properties := make(map[string]interface{})
	definitions := make(map[string]interface{})
	fieldID := options.FieldIDOffset

	// Add definitions for all types
	for name, typeInfo := range parsed.ParameterTypes {
		definitions[name] = c.typeToJSONSchemaProperty(typeInfo, fieldID)
		fieldID++
	}

	// Add properties from parameters
	if options.IncludeTelemetry {
		for _, param := range parsed.TelemetryParams {
			typeInfo, ok := parsed.ParameterTypes[param.TypeRef]
			if ok {
				prop := c.typeToJSONSchemaProperty(typeInfo, fieldID)
				if param.Description != "" {
					prop.(map[string]interface{})["description"] = param.Description
				}
				properties[param.Name] = prop
			}
			fieldID++
		}
	}

	// Add properties from command arguments
	if options.IncludeCommands {
		for _, arg := range parsed.CommandArgs {
			typeInfo, ok := parsed.ParameterTypes[arg.TypeRef]
			if ok {
				prop := c.typeToJSONSchemaProperty(typeInfo, fieldID)
				if arg.Description != "" {
					prop.(map[string]interface{})["description"] = arg.Description
				}
				properties["cmd_"+arg.Name] = prop
			}
			fieldID++
		}
	}

	schema["properties"] = properties
	schema["definitions"] = definitions

	return schema
}

// typeToJSONSchemaProperty converts a parameter type to JSON Schema property.
func (c *XTCEConverter) typeToJSONSchemaProperty(typeInfo *ParameterTypeInfo, fieldID int) interface{} {
	prop := map[string]interface{}{
		"x-flatbuffer-field-id": fieldID,
	}

	if typeInfo.Description != "" {
		prop["description"] = typeInfo.Description
	}

	if typeInfo.Unit != "" {
		prop["x-xtce-unit"] = typeInfo.Unit
	}
	if typeInfo.UnitDescription != "" {
		prop["x-xtce-unit-description"] = typeInfo.UnitDescription
	}
	if typeInfo.Encoding != "" {
		prop["x-xtce-encoding"] = typeInfo.Encoding
	}
	if typeInfo.SizeInBits > 0 {
		prop["x-xtce-encoding-size"] = typeInfo.SizeInBits
	}

	switch typeInfo.Type {
	case "integer":
		prop["type"] = "integer"
		prop["x-flatbuffer-type"] = integerToFlatBufferType(typeInfo.SizeInBits, typeInfo.Signed)
		if typeInfo.MinInclusive != nil {
			prop["minimum"] = *typeInfo.MinInclusive
		}
		if typeInfo.MaxInclusive != nil {
			prop["maximum"] = *typeInfo.MaxInclusive
		}

	case "float":
		prop["type"] = "number"
		prop["x-flatbuffer-type"] = floatToFlatBufferType(typeInfo.SizeInBits)
		if typeInfo.MinInclusive != nil {
			prop["minimum"] = *typeInfo.MinInclusive
		}
		if typeInfo.MaxInclusive != nil {
			prop["maximum"] = *typeInfo.MaxInclusive
		}

	case "string":
		prop["type"] = "string"
		prop["x-flatbuffer-type"] = "string"
		if typeInfo.SizeInBits > 0 {
			prop["maxLength"] = typeInfo.SizeInBits / 8
		}

	case "enumerated":
		prop["type"] = "string"
		prop["x-flatbuffer-type"] = "int32"
		var enumLabels []string
		enumValues := make(map[string]int)
		for _, e := range typeInfo.Enumerations {
			enumLabels = append(enumLabels, e.Label)
			enumValues[e.Label] = e.Value
		}
		prop["enum"] = enumLabels
		prop["x-xtce-enum-values"] = enumValues

	case "boolean":
		prop["type"] = "boolean"
		prop["x-flatbuffer-type"] = "bool"
		if typeInfo.BooleanLabels != nil {
			prop["x-xtce-boolean-labels"] = map[string]string{
				"zero": typeInfo.BooleanLabels.Zero,
				"one":  typeInfo.BooleanLabels.One,
			}
		}

	case "time":
		prop["type"] = "string"
		prop["format"] = "date-time"
		prop["x-flatbuffer-type"] = "int64"

	case "binary":
		prop["type"] = "string"
		prop["format"] = "binary"
		prop["x-flatbuffer-type"] = "[ubyte]"

	default:
		prop["type"] = "string"
		prop["x-flatbuffer-type"] = "[ubyte]"
	}

	return prop
}

// generateFlatBufferSchema generates a FlatBuffer schema from parsed XTCE.
func (c *XTCEConverter) generateFlatBufferSchema(parsed *ParsedXTCE, options ConversionOptions) string {
	var sb strings.Builder

	sb.WriteString("// Auto-generated FlatBuffer schema from XTCE\n")
	sb.WriteString(fmt.Sprintf("// Generated: %s\n", time.Now().UTC().Format(time.RFC3339)))
	sb.WriteString("\n")

	if options.Namespace != "" {
		sb.WriteString(fmt.Sprintf("namespace %s;\n\n", options.Namespace))
	}

	// File identifier (first 3 chars of name, uppercase)
	fileID := parsed.Name
	if len(fileID) > 3 {
		fileID = fileID[:3]
	}
	sb.WriteString(fmt.Sprintf("file_identifier \"$%s\";\n\n", strings.ToUpper(fileID)))

	// Generate enums
	if options.GenerateEnums {
		for name, typeInfo := range parsed.ParameterTypes {
			if typeInfo.Type == "enumerated" && len(typeInfo.Enumerations) > 0 {
				if typeInfo.Description != "" {
					sb.WriteString(fmt.Sprintf("/// %s\n", typeInfo.Description))
				}
				sb.WriteString(fmt.Sprintf("enum %s : int {\n", sanitizeName(name)))
				for _, e := range typeInfo.Enumerations {
					sb.WriteString(fmt.Sprintf("  %s = %d,\n", sanitizeName(e.Label), e.Value))
				}
				sb.WriteString("}\n\n")
			}
		}
	}

	// Generate main table
	if parsed.Description != "" {
		sb.WriteString(fmt.Sprintf("/// %s\n", parsed.Description))
	}
	sb.WriteString(fmt.Sprintf("table %s {\n", sanitizeName(parsed.Name)))

	fieldID := options.FieldIDOffset

	// Add telemetry fields
	if options.IncludeTelemetry {
		for _, param := range parsed.TelemetryParams {
			typeInfo, ok := parsed.ParameterTypes[param.TypeRef]
			if ok {
				desc := firstNonEmpty(param.Description, typeInfo.Description)
				if desc != "" {
					sb.WriteString(fmt.Sprintf("  /// %s\n", desc))
				}
				fbType := typeToFlatBufferType(typeInfo, options.GenerateEnums)
				sb.WriteString(fmt.Sprintf("  %s:%s (id: %d);\n", sanitizeName(param.Name), fbType, fieldID))
			}
			fieldID++
		}
	}

	// Add command argument fields
	if options.IncludeCommands {
		for _, arg := range parsed.CommandArgs {
			typeInfo, ok := parsed.ParameterTypes[arg.TypeRef]
			if ok {
				desc := firstNonEmpty(arg.Description, typeInfo.Description)
				if desc != "" {
					sb.WriteString(fmt.Sprintf("  /// %s\n", desc))
				}
				fbType := typeToFlatBufferType(typeInfo, options.GenerateEnums)
				sb.WriteString(fmt.Sprintf("  cmd_%s:%s (id: %d);\n", sanitizeName(arg.Name), fbType, fieldID))
			}
			fieldID++
		}
	}

	sb.WriteString("}\n\n")
	sb.WriteString(fmt.Sprintf("root_type %s;\n", sanitizeName(parsed.Name)))

	return sb.String()
}

// Helper functions

func parseInt(s string, defaultValue int) int {
	if s == "" {
		return defaultValue
	}
	var v int
	fmt.Sscanf(s, "%d", &v)
	return v
}

func parseFloat(s string) float64 {
	var v float64
	fmt.Sscanf(s, "%f", &v)
	return v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, ".", "_")
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, " ", "_")
	// Remove consecutive underscores
	for strings.Contains(name, "__") {
		name = strings.ReplaceAll(name, "__", "_")
	}
	// Prefix with underscore if starts with digit
	if len(name) > 0 && name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}

func integerToFlatBufferType(sizeInBits int, signed bool) string {
	if signed {
		switch {
		case sizeInBits <= 8:
			return "int8"
		case sizeInBits <= 16:
			return "int16"
		case sizeInBits <= 32:
			return "int32"
		default:
			return "int64"
		}
	} else {
		switch {
		case sizeInBits <= 8:
			return "uint8"
		case sizeInBits <= 16:
			return "uint16"
		case sizeInBits <= 32:
			return "uint32"
		default:
			return "uint64"
		}
	}
}

func floatToFlatBufferType(sizeInBits int) string {
	if sizeInBits <= 32 {
		return "float32"
	}
	return "float64"
}

func typeToFlatBufferType(typeInfo *ParameterTypeInfo, useEnums bool) string {
	switch typeInfo.Type {
	case "integer":
		return integerToFlatBufferType(typeInfo.SizeInBits, typeInfo.Signed)
	case "float":
		return floatToFlatBufferType(typeInfo.SizeInBits)
	case "string":
		return "string"
	case "enumerated":
		if useEnums {
			return sanitizeName(typeInfo.Name)
		}
		return "int32"
	case "boolean":
		return "bool"
	case "time":
		return "long"
	case "binary":
		return "[ubyte]"
	default:
		return "[ubyte]"
	}
}
