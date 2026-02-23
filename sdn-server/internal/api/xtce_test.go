package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sampleXTCE = `<?xml version="1.0" encoding="UTF-8"?>
<xtce:SpaceSystem xmlns:xtce="http://www.omg.org/spec/XTCE/20180204"
                  name="TestSpacecraft"
                  shortDescription="Test spacecraft for unit tests">
  <xtce:TelemetryMetaData>
    <xtce:ParameterTypeSet>
      <xtce:IntegerParameterType name="Temperature_Type" signed="true" sizeInBits="16">
        <xtce:UnitSet>
          <xtce:Unit description="Temperature">degC</xtce:Unit>
        </xtce:UnitSet>
        <xtce:IntegerDataEncoding sizeInBits="16" encoding="twosComplement"/>
        <xtce:ValidRange minInclusive="-40" maxInclusive="85"/>
      </xtce:IntegerParameterType>
      <xtce:FloatParameterType name="Voltage_Type" sizeInBits="32">
        <xtce:UnitSet>
          <xtce:Unit description="Potential">V</xtce:Unit>
        </xtce:UnitSet>
        <xtce:FloatDataEncoding sizeInBits="32" encoding="IEEE754_1985"/>
      </xtce:FloatParameterType>
      <xtce:EnumeratedParameterType name="Mode_Type" shortDescription="Operational mode">
        <xtce:IntegerDataEncoding sizeInBits="8" encoding="unsigned"/>
        <xtce:EnumerationList>
          <xtce:Enumeration value="0" label="OFF"/>
          <xtce:Enumeration value="1" label="STANDBY"/>
          <xtce:Enumeration value="2" label="ACTIVE"/>
        </xtce:EnumerationList>
      </xtce:EnumeratedParameterType>
      <xtce:BooleanParameterType name="Flag_Type" zeroStringValue="FALSE" oneStringValue="TRUE">
        <xtce:IntegerDataEncoding sizeInBits="1" encoding="unsigned"/>
      </xtce:BooleanParameterType>
    </xtce:ParameterTypeSet>
    <xtce:ParameterSet>
      <xtce:Parameter name="TEMP" parameterTypeRef="Temperature_Type" shortDescription="Temperature sensor"/>
      <xtce:Parameter name="VOLTAGE" parameterTypeRef="Voltage_Type" shortDescription="Battery voltage"/>
      <xtce:Parameter name="MODE" parameterTypeRef="Mode_Type" shortDescription="System mode"/>
      <xtce:Parameter name="FLAG" parameterTypeRef="Flag_Type" shortDescription="Status flag"/>
    </xtce:ParameterSet>
  </xtce:TelemetryMetaData>
</xtce:SpaceSystem>
`

func TestXTCEHandler_Convert_JSONSchema(t *testing.T) {
	handler := NewXTCEHandler()

	req := httptest.NewRequest(http.MethodPost, "/api/ingest/xtce", strings.NewReader(sampleXTCE))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	if !strings.Contains(string(body), `"$schema"`) {
		t.Errorf("Expected JSON Schema in response, got: %s", body)
	}

	if !strings.Contains(string(body), `"x-flatbuffer-type"`) {
		t.Errorf("Expected x-flatbuffer-type annotation, got: %s", body)
	}

	if !strings.Contains(string(body), `"TEMP"`) {
		t.Errorf("Expected TEMP property, got: %s", body)
	}
}

func TestXTCEHandler_Convert_FlatBuffer(t *testing.T) {
	handler := NewXTCEHandler()

	req := httptest.NewRequest(http.MethodPost, "/api/ingest/xtce", strings.NewReader(sampleXTCE))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Accept", "application/x-flatbuffers")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	if !strings.Contains(string(body), "table TestSpacecraft") {
		t.Errorf("Expected FlatBuffer table, got: %s", body)
	}

	if !strings.Contains(string(body), "TEMP:int16") {
		t.Errorf("Expected TEMP field, got: %s", body)
	}
}

func TestXTCEHandler_Convert_Enums(t *testing.T) {
	handler := NewXTCEHandler()

	req := httptest.NewRequest(http.MethodPost, "/api/ingest/xtce", strings.NewReader(sampleXTCE))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Accept", "application/x-flatbuffers")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), "enum Mode_Type") {
		t.Errorf("Expected Mode_Type enum, got: %s", body)
	}

	if !strings.Contains(string(body), "OFF = 0") {
		t.Errorf("Expected OFF enum value, got: %s", body)
	}
}

func TestXTCEHandler_Info(t *testing.T) {
	handler := NewXTCEHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/ingest/xtce", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	if !strings.Contains(string(body), "XTCE Ingestion API") {
		t.Errorf("Expected API info, got: %s", body)
	}
}

func TestXTCEConverter_ParseXTCE(t *testing.T) {
	converter := NewXTCEConverter()

	parsed, err := converter.parseXTCE(sampleXTCE)
	if err != nil {
		t.Fatalf("Failed to parse XTCE: %v", err)
	}

	if parsed.Name != "TestSpacecraft" {
		t.Errorf("Expected name 'TestSpacecraft', got '%s'", parsed.Name)
	}

	if len(parsed.ParameterTypes) != 4 {
		t.Errorf("Expected 4 parameter types, got %d", len(parsed.ParameterTypes))
	}

	if len(parsed.TelemetryParams) != 4 {
		t.Errorf("Expected 4 telemetry parameters, got %d", len(parsed.TelemetryParams))
	}

	// Check integer type
	tempType, ok := parsed.ParameterTypes["Temperature_Type"]
	if !ok {
		t.Error("Expected Temperature_Type")
	} else {
		if tempType.Type != "integer" {
			t.Errorf("Expected type 'integer', got '%s'", tempType.Type)
		}
		if tempType.SizeInBits != 16 {
			t.Errorf("Expected sizeInBits 16, got %d", tempType.SizeInBits)
		}
		if !tempType.Signed {
			t.Error("Expected signed=true")
		}
		if tempType.Unit != "degC" {
			t.Errorf("Expected unit 'degC', got '%s'", tempType.Unit)
		}
	}

	// Check enumerated type
	modeType, ok := parsed.ParameterTypes["Mode_Type"]
	if !ok {
		t.Error("Expected Mode_Type")
	} else {
		if modeType.Type != "enumerated" {
			t.Errorf("Expected type 'enumerated', got '%s'", modeType.Type)
		}
		if len(modeType.Enumerations) != 3 {
			t.Errorf("Expected 3 enumerations, got %d", len(modeType.Enumerations))
		}
	}
}

func TestXTCEConverter_Convert(t *testing.T) {
	converter := NewXTCEConverter()

	result, err := converter.Convert(nil, sampleXTCE, ConversionOptions{
		IncludeTelemetry: true,
		IncludeCommands:  true,
		GenerateEnums:    true,
	})
	if err != nil {
		t.Fatalf("Conversion failed: %v", err)
	}

	if result.Name != "TestSpacecraft" {
		t.Errorf("Expected name 'TestSpacecraft', got '%s'", result.Name)
	}

	if result.TelemetryCount != 4 {
		t.Errorf("Expected 4 telemetry parameters, got %d", result.TelemetryCount)
	}

	// Check JSON Schema
	if !strings.Contains(result.JSONSchemaString, `"x-flatbuffer-type"`) {
		t.Error("Expected x-flatbuffer-type in JSON Schema")
	}

	// Check FlatBuffer schema
	if !strings.Contains(result.FlatBufferSchema, "table TestSpacecraft") {
		t.Error("Expected TestSpacecraft table in FlatBuffer schema")
	}
}
