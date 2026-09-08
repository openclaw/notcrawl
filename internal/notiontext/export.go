package notiontext

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sort"
	"strings"
)

// ExportFields projects provider records without modifying their local recovery
// payloads. Only structured URL fields identify capabilities; arbitrary text is
// changed only when it repeats a capability found in that same record.
func ExportFields(fields map[string]string) (map[string]string, error) {
	replacements := map[string]string{}
	var replacementKeys []string
	replaceText := false
	var visit func(any, string, bool) (any, error)
	visit = func(value any, key string, storageFields bool) (any, error) {
		switch v := value.(type) {
		case map[string]any:
			for k, child := range v {
				clean, err := visit(child, k, storageFields && key == "")
				if err != nil {
					return nil, err
				}
				v[k] = clean
			}
		case []any:
			for i, child := range v {
				clean, err := visit(child, key, false)
				if err != nil {
					return nil, err
				}
				v[i] = clean
			}
		case string:
			field := strings.ReplaceAll(strings.ToLower(key), "_", "")
			switch field {
			case "rawjson", "propertiesjson", "contentjson", "formatjson", "schemajson", "payloadjson":
				if !storageFields {
					break
				}
				if strings.TrimSpace(v) == "" {
					return v, nil
				}
				nested, err := decodeExportJSON(v)
				if err != nil {
					return nil, err
				}
				if field == "rawjson" && fields["source"] == "desktop" {
					if envelope, ok := nested.(map[string]any); ok {
						// Desktop's SQLite json_object envelopes retain TEXT JSON
						// columns as strings. Only those known outer shapes qualify.
						for _, column := range desktopEnvelopeJSONFields(envelope) {
							if envelope[column] == nil {
								continue
							}
							raw, ok := envelope[column].(string)
							if !ok {
								return nil, errors.New("cannot sanitize invalid Desktop JSON column for export")
							}
							if strings.TrimSpace(raw) == "" {
								continue
							}
							content, err := decodeExportJSON(raw)
							if err != nil {
								return nil, err
							}
							content, err = visit(content, "", false)
							if err != nil {
								return nil, err
							}
							encoded, err := json.Marshal(content)
							if err != nil {
								return nil, err
							}
							envelope[column] = string(encoded)
						}
					}
				}
				// Only source fallback payloads wrap storage columns. Provider
				// objects can contain arbitrary user property names.
				nested, err = visit(nested, "", field == "payloadjson")
				if err != nil {
					return nil, err
				}
				raw, err := json.Marshal(nested)
				return string(raw), err
			case "url", "icon", "cover", "displaysource":
				clean, err := exportFileURL(v)
				if err != nil {
					return nil, err
				}
				if clean != v {
					replacements[v] = clean
				}
				v = clean
			}
			if replaceText && strings.Contains(v, "://") {
				for _, from := range replacementKeys {
					v = strings.ReplaceAll(v, from, replacements[from])
				}
			}
			return v, nil
		}
		return value, nil
	}
	projected := make(map[string]string, len(fields))
	for key, value := range fields {
		projected[key] = value
	}
	// A second pass covers derived text encountered before its structured URL.
	for range 2 {
		for key, value := range projected {
			clean, err := visit(value, key, true)
			if err != nil {
				return nil, err
			}
			projected[key] = clean.(string)
		}
		if !replaceText {
			for from := range replacements {
				replacementKeys = append(replacementKeys, from)
			}
			// Longest first avoids replacing a URL prefix before the full URL.
			sort.Slice(replacementKeys, func(i, j int) bool {
				if len(replacementKeys[i]) == len(replacementKeys[j]) {
					return replacementKeys[i] < replacementKeys[j]
				}
				return len(replacementKeys[i]) > len(replacementKeys[j])
			})
			replaceText = true
		}
	}
	return projected, nil
}

func decodeExportJSON(raw string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, errors.New("cannot sanitize malformed provider JSON for export")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("cannot sanitize trailing provider JSON for export")
	}
	return value, nil
}

func desktopEnvelopeJSONFields(envelope map[string]any) []string {
	if id, ok := envelope["id"].(string); !ok || id == "" {
		return nil
	}
	// Match the complete column lists emitted by notiondesktop ingestion, not
	// arbitrary provider objects or user properties named "format"/"content".
	for _, shape := range []struct {
		keys    string
		columns []string
	}{
		{"id space_id type properties content collection_id created_time last_edited_time parent_id parent_table alive format", []string{"properties", "content", "format"}},
		{"id space_id parent_id parent_table name schema format", []string{"schema", "format"}},
		{"id parent_id space_id text content created_by_id created_time last_edited_time alive", []string{"content"}},
	} {
		keys := strings.Fields(shape.keys)
		if len(envelope) != len(keys) {
			continue
		}
		matches := true
		for _, key := range keys {
			if _, ok := envelope[key]; !ok {
				matches = false
				break
			}
		}
		if matches {
			return shape.columns
		}
	}
	return nil
}

func exportFileURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// A bad path escape must not hide valid query credentials.
		_, rawQuery, _ := strings.Cut(raw, "?")
		rawQuery, _, _ = strings.Cut(rawQuery, "#")
		query, _ := url.ParseQuery(rawQuery)
		v4, v2, cloudfront := fileURLSignatures(query)
		if strings.Contains(strings.ToLower(raw), "x-amz-") || v4 || v2 || cloudfront {
			return "", errors.New("cannot sanitize malformed signed file URL for export")
		}
		return raw, nil
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return raw, nil
	}
	query, err := url.ParseQuery(u.RawQuery)
	v4, v2, cloudfront := fileURLSignatures(query)
	if err != nil {
		if strings.Contains(strings.ToLower(u.RawQuery), "x-amz-") || v4 || v2 || cloudfront {
			return "", errors.New("cannot sanitize malformed signed file URL for export")
		}
		return raw, nil
	}
	if !v4 && !v2 && !cloudfront {
		return raw, nil
	}
	for key := range query {
		k := strings.ToLower(key)
		if (v4 && strings.HasPrefix(k, "x-amz-")) ||
			(v2 && (k == "signature" || k == "awsaccesskeyid" || k == "expires" || k == "security-token")) ||
			(cloudfront && (k == "signature" || k == "key-pair-id" || k == "policy" || k == "expires")) {
			query.Del(key)
		}
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func fileURLSignatures(query url.Values) (v4, v2, cloudfront bool) {
	lower := map[string]bool{}
	for key := range query {
		lower[strings.ToLower(key)] = true
	}
	return lower["x-amz-signature"] || lower["x-amz-credential"] || lower["x-amz-security-token"],
		lower["signature"] && lower["awsaccesskeyid"],
		lower["signature"] && lower["key-pair-id"]
}
