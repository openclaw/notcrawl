package notiontext

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func desktopExportEnvelope() map[string]any {
	return map[string]any{
		"id": "block", "space_id": "space", "type": "file",
		"properties": `{}`, "content": `[]`, "format": `{}`, "collection_id": "",
		"created_time": json.Number("9007199254740993"), "last_edited_time": json.Number("9007199254740993"),
		"parent_id": "page", "parent_table": "block", "alive": json.Number("1"),
	}
}

func exportJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestExportFieldsDesktopEnvelopes(t *testing.T) {
	u := url.URL{Scheme: "https", Host: "files.example", Path: "/attachment"}
	u.RawQuery = url.Values{"X-Amz-Signature": {"redacted"}, "X-Amz-Credential": {"placeholder"}, "download": {"1"}}.Encode()
	properties := `{"number":9223372036854775807,"fraction":0.10000000000000001,"raw_json":"ordinary text","format":"not JSON"}`
	format := exportJSON(t, map[string]any{"display_source": u.String(), "number": json.Number("-9223372036854775808")})
	block := desktopExportEnvelope()
	block["properties"], block["format"] = properties, format
	for name, envelope := range map[string]map[string]any{
		"block": block,
		"collection": {
			"id": "collection", "space_id": "space", "parent_id": "page", "parent_table": "block",
			"name": "Collection", "schema": properties, "format": format,
		},
		"comment": {
			"id": "comment", "parent_id": "page", "space_id": "space", "text": "ordinary text",
			"content": format, "created_by_id": "user", "created_time": json.Number("9007199254740993"),
			"last_edited_time": json.Number("9007199254740993"), "alive": json.Number("1"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw := exportJSON(t, envelope)
			fallback := exportJSON(t, map[string]any{
				"Source": "desktop", "RawJSON": raw, "PropertiesJSON": properties, "FormatJSON": format,
				"DisplayOrder": json.Number("9007199254740993"),
			})
			for field, payload := range map[string]string{"raw_json": raw, "payload_json": fallback} {
				t.Run(field, func(t *testing.T) {
					input := map[string]string{"source": "desktop", field: payload}
					got, err := ExportFields(input)
					if err != nil {
						t.Fatal(err)
					}
					if input[field] != payload {
						t.Fatal("export changed the local payload")
					}
					if strings.Contains(got[field], "X-Amz-") {
						t.Fatal("Desktop capability survived export")
					}
					projected, err := decodeExportJSON(got[field])
					if err != nil {
						t.Fatal(err)
					}
					if field == "payload_json" {
						wrapper := projected.(map[string]any)
						if wrapper["DisplayOrder"] != json.Number("9007199254740993") {
							t.Fatal("fallback integer rounded")
						}
						projected, err = decodeExportJSON(wrapper["RawJSON"].(string))
						if err != nil {
							t.Fatal(err)
						}
					}
					object := projected.(map[string]any)
					if !reflect.DeepEqual(object["created_time"], envelope["created_time"]) {
						t.Fatal("outer integer rounded")
					}
					for _, column := range desktopEnvelopeJSONFields(envelope) {
						value, err := decodeExportJSON(object[column].(string))
						if err != nil {
							t.Fatal(err)
						}
						if column == "properties" || column == "schema" {
							want, err := decodeExportJSON(properties)
							if err != nil || !reflect.DeepEqual(value, want) {
								t.Fatalf("ordinary properties/numbers changed: %v", err)
							}
						}
						if column == "format" || (name == "comment" && column == "content") {
							fields := value.(map[string]any)
							if fields["display_source"] != "https://files.example/attachment?download=1" ||
								fields["number"] != json.Number("-9223372036854775808") {
								t.Fatal("invalid Desktop URL or numeric projection")
							}
						}
					}
				})
			}
		})
	}
}

func TestExportFieldsDesktopContextIsNotUserJSON(t *testing.T) {
	for _, source := range []string{"", "api", "notion-mcp", "desktop"} {
		for _, shape := range []string{"known", "extra property", "missing column", "missing identity", "nested property"} {
			if source == "desktop" && shape == "known" {
				continue
			}
			t.Run(source+"/"+shape, func(t *testing.T) {
				envelope := desktopExportEnvelope()
				envelope["format"] = "arbitrary user text, not JSON"
				switch shape {
				case "extra property":
					envelope["user_property"] = true
				case "missing column":
					delete(envelope, "parent_table")
				case "missing identity":
					envelope["id"] = ""
				case "nested property":
					envelope = map[string]any{"custom": envelope}
				}
				raw := exportJSON(t, envelope)
				got, err := ExportFields(map[string]string{"source": source, "raw_json": raw})
				if err != nil {
					t.Fatalf("reinterpreted an unrecognized envelope: %v", err)
				}
				before, err := decodeExportJSON(raw)
				if err != nil {
					t.Fatal(err)
				}
				after, err := decodeExportJSON(got["raw_json"])
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("changed arbitrary user content: %v", err)
				}
			})
		}
	}
	envelope := desktopExportEnvelope()
	envelope["properties"] = exportJSON(t, map[string]any{
		"format": "not JSON", "raw_json": "not JSON", "payload_json": "not JSON",
		"custom": desktopExportEnvelope(),
	})
	raw := exportJSON(t, envelope)
	if _, err := ExportFields(map[string]string{"source": "desktop", "raw_json": raw}); err != nil {
		t.Fatalf("provider properties were treated as storage columns: %v", err)
	}
}

func TestExportFieldsDesktopMalformedColumnsFailClosed(t *testing.T) {
	for _, column := range []string{"properties", "content", "format"} {
		for _, value := range []any{`{`, `{} {}`, json.Number("1")} {
			t.Run(column+"/"+exportJSON(t, value), func(t *testing.T) {
				envelope := desktopExportEnvelope()
				envelope[column] = value
				raw := exportJSON(t, envelope)
				for field, payload := range map[string]string{
					"raw_json":     raw,
					"payload_json": exportJSON(t, map[string]string{"Source": "desktop", "RawJSON": raw}),
				} {
					if _, err := ExportFields(map[string]string{"source": "desktop", field: payload}); err == nil {
						t.Fatal("malformed known Desktop column exported")
					}
				}
			})
		}
	}
	for _, empty := range []any{nil, ""} {
		envelope := desktopExportEnvelope()
		envelope["format"] = empty
		if _, err := ExportFields(map[string]string{"source": "desktop", "raw_json": exportJSON(t, envelope)}); err != nil {
			t.Fatalf("absent Desktop column rejected: %v", err)
		}
	}
}
