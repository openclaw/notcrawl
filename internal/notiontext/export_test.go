package notiontext

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestExportFieldsSignedFiles(t *testing.T) {
	signedURL := url.URL{Scheme: "https", Host: "files.example", Path: "/file", Fragment: "part"}
	signedURL.RawQuery = url.Values{"X-Amz-Signature": {"redacted"}, "X-Amz-Credential": {"placeholder"}, "download": {"1"}}.Encode()
	signed := signedURL.String()
	clean := "https://files.example/file?download=1#part"
	ordinary := url.URL{Scheme: "https", Host: "example.test", RawQuery: url.Values{"token": {"ordinary"}, "query": {"keep"}}.Encode()}
	properties := `{"file":{"url":"` + signed + `"},"number":9007199254740993,"ordinary":{"url":"` + ordinary.String() + `"}}`
	envelope, err := json.Marshal(map[string]string{"PropertiesJSON": properties, "RawJSON": properties, "Text": "attachment <" + signed + ">"})
	if err != nil {
		t.Fatal(err)
	}
	original := map[string]string{"payload_json": string(envelope), "properties_json": properties, "text": "attachment <" + signed + ">", "unrelated": "user text token=unchanged"}
	got, err := ExportFields(original)
	if err != nil {
		t.Fatal(err)
	}
	if got["text"] != "attachment <"+clean+">" || got["unrelated"] != original["unrelated"] || original["properties_json"] != properties {
		t.Fatalf("projection changed unrelated/source fields: %#v", got)
	}
	for _, key := range []string{"payload_json", "properties_json"} {
		if strings.Contains(got[key], "X-Amz-") || !strings.Contains(got[key], "9007199254740993") || !strings.Contains(got[key], "token=ordinary") {
			t.Fatalf("invalid projection %s: %s", key, got[key])
		}
	}
	for _, input := range []string{`{"file":{"url":`, `{} {}`} {
		if _, err := ExportFields(map[string]string{"raw_json": input}); err == nil {
			t.Fatal("malformed protected JSON exported")
		}
	}
}

func TestExportFileURLPreservesOrdinaryQueries(t *testing.T) {
	ordinary := url.URL{Scheme: "https", Host: "example.test", RawQuery: url.Values{"token": {"keep"}, "signature": {"ordinary"}}.Encode()}
	for _, raw := range []string{ordinary.String(), "https://example.test/?q=%zz", "ordinary text X-Amz-Signature=keep"} {
		got, err := exportFileURL(raw)
		if err != nil || got != raw {
			t.Fatalf("ordinary value changed: %q %q %v", raw, got, err)
		}
	}
}

func TestExportFileURLLegacySignatures(t *testing.T) {
	for _, identity := range []string{"AWSAccessKeyId", "Key-Pair-Id"} {
		t.Run(identity, func(t *testing.T) {
			u := url.URL{Scheme: "https", Host: "files.example", Path: "/file", Fragment: "part"}
			u.RawQuery = url.Values{"Signature": {"redacted"}, identity: {"placeholder"}, "Expires": {"123"}, "download": {"1"}}.Encode()
			got, err := exportFileURL(u.String())
			if err != nil || got != "https://files.example/file?download=1#part" {
				t.Fatalf("legacy signature projection: %q %v", got, err)
			}
			for _, raw := range []string{
				strings.Replace(u.String(), "?", "?broken=%zz&", 1),
				strings.Replace(u.String(), "/file", "/%zz", 1),
			} {
				if _, err := exportFileURL(raw); err == nil {
					t.Fatal("malformed signed URL exported unchanged")
				}
				body, err := json.Marshal(map[string]string{"url": raw})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ExportFields(map[string]string{"raw_json": string(body)}); err == nil {
					t.Fatal("structured projection accepted malformed signed URL")
				}
			}
		})
	}
}

func TestExportFieldsPreservesUserPropertyNames(t *testing.T) {
	properties := `{"raw_json":[["ordinary text"]],"payload_json":"not JSON","PropertiesJSON":{"raw_json":"also ordinary"},"number":9007199254740993}`
	envelope, err := json.Marshal(map[string]string{"PropertiesJSON": properties, "RawJSON": properties})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ExportFields(map[string]string{"properties_json": properties, "payload_json": string(envelope)})
	if err != nil {
		t.Fatal(err)
	}
	var projectedEnvelope map[string]string
	if err := json.Unmarshal([]byte(got["payload_json"]), &projectedEnvelope); err != nil {
		t.Fatal(err)
	}
	decode := func(raw string) any {
		t.Helper()
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	want := decode(properties)
	for _, raw := range []string{got["properties_json"], projectedEnvelope["PropertiesJSON"], projectedEnvelope["RawJSON"]} {
		if !reflect.DeepEqual(decode(raw), want) {
			t.Fatalf("user properties changed: %s", raw)
		}
	}
}
