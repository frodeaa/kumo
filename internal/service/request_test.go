package service_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sivchari/kumo/internal/service"
)

const testNameAlice = "alice"

func TestReadJSONRequest(t *testing.T) {
	t.Parallel()

	type payload struct {
		Name string `json:"name"`
	}

	tests := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{name: "valid JSON decodes into v", body: `{"name":"alice"}`, want: testNameAlice},
		{name: "empty body is a no-op", body: "", want: ""},
		{name: "invalid JSON returns an error", body: `{not json`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), "POST", "/", strings.NewReader(tt.body))

			var p payload

			err := service.ReadJSONRequest(req, &p)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ReadJSONRequest() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr && p.Name != tt.want {
				t.Errorf("ReadJSONRequest() decoded name = %q, want %q", p.Name, tt.want)
			}
		})
	}
}

// epochOrText records which form its JSON arrived in, standing in for types
// such as CloudWatch's timestamps that decode themselves.
type epochOrText struct{ raw string }

func (e *epochOrText) UnmarshalJSON(data []byte) error {
	e.raw = string(data)

	return nil
}

type queryPayload struct {
	Description string
	Threshold   float64
	Value       *float64
	Period      int32
	Enabled     bool
	Values      []float64
	Dimensions  []struct {
		Name  string
		Value string
	}
	StartTime epochOrText
}

func TestReadJSONRequest_CoercesQueryValuesToFieldTypes(t *testing.T) {
	t.Parallel()

	body := `{
		"Description": 123,
		"Threshold": "1.0",
		"Value": "7.5",
		"Period": 60,
		"Enabled": "true",
		"Values": ["1.5", 2],
		"Dimensions": [{"Name": "Account", "Value": 123456789012345678901234567890}],
		"StartTime": "2025-10-16T10:00:00Z"
	}`

	req := httptest.NewRequestWithContext(t.Context(), "POST", "/", strings.NewReader(body))

	var p queryPayload
	if err := service.ReadJSONRequest(service.WithQueryProtocol(req), &p); err != nil {
		t.Fatalf("ReadJSONRequest() error = %v", err)
	}

	if p.Description != "123" || p.Threshold != 1 || p.Value == nil || *p.Value != 7.5 || p.Period != 60 || !p.Enabled {
		t.Errorf("scalars = %+v", p)
	}

	if len(p.Values) != 2 || p.Values[0] != 1.5 || p.Values[1] != 2 {
		t.Errorf("Values = %v, want [1.5 2]", p.Values)
	}

	if len(p.Dimensions) != 1 || p.Dimensions[0].Value != "123456789012345678901234567890" {
		t.Errorf("Dimensions = %+v, want the digits kept", p.Dimensions)
	}

	if p.StartTime.raw != `"2025-10-16T10:00:00Z"` {
		t.Errorf("StartTime got %s, want the original JSON for its own UnmarshalJSON", p.StartTime.raw)
	}
}

func TestReadJSONRequest_JSONProtocolStaysStrict(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(t.Context(), "POST", "/", strings.NewReader(`{"Threshold": "1.0"}`))

	var p queryPayload
	if err := service.ReadJSONRequest(req, &p); err == nil {
		t.Errorf("ReadJSONRequest() decoded a string into a float field outside the Query protocol: %+v", p)
	}
}
