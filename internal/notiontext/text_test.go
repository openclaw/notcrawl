package notiontext

import "testing"

func TestTitleFromProperties(t *testing.T) {
	got := TitleFromProperties(`{"title":[["Launch Plan"]]}`)
	if got != "Launch Plan" {
		t.Fatalf("got %q", got)
	}
}

func TestTitleFromPropertiesUsesStableFallback(t *testing.T) {
	for range 100 {
		if got := TitleFromProperties(`{"z":[["Zulu"]],"a":[["Alpha"]]}`); got != "Alpha" {
			t.Fatalf("fallback title = %q; want Alpha", got)
		}
	}
}

func TestTitleFromPropertiesPrefersNotionRichTextOnce(t *testing.T) {
	got := TitleFromProperties(`{
		"Name": {
			"id": "title",
			"type": "title",
			"title": [{
				"type": "text",
				"plain_text": "OpenClaw",
				"text": {"content": "OpenClaw"}
			}]
		}
	}`)
	if got != "OpenClaw" {
		t.Fatalf("got %q", got)
	}
}

func TestPlainPrefersNotionRichTextPlainTextOnce(t *testing.T) {
	got := Plain([]any{map[string]any{
		"type":       "text",
		"plain_text": "OpenClaw",
		"text": map[string]any{
			"content": "OpenClaw",
		},
	}})
	if got != "OpenClaw" {
		t.Fatalf("got %q", got)
	}
}

func TestPlainFallsBackToNotionTextContentOnce(t *testing.T) {
	got := Plain([]any{map[string]any{
		"type": "text",
		"text": map[string]any{
			"content": "OpenClaw",
		},
	}})
	if got != "OpenClaw" {
		t.Fatalf("got %q", got)
	}
}

func TestPlainHandlesLegacyNotionAnnotations(t *testing.T) {
	got := PlainFromJSON(`{"title":[["Marketing Customer Reference Rights",[["a","https://example.com/sheet"]]],[" "],["Product Marketing",[["b"]]]]}`)
	if got != "Marketing Customer Reference Rights <https://example.com/sheet> Product Marketing" {
		t.Fatalf("got %q", got)
	}
}

func TestCleanLegacyArtifacts(t *testing.T) {
	got := CleanLegacyArtifacts("Option A: b\nMarketing Customer Reference Rights a https://example.com/sheet\nm 35171240-10a3-80ff-95be-001c31559035 It works")
	if got != "Option A: Marketing Customer Reference Rights <https://example.com/sheet> @mention It works" {
		t.Fatalf("got %q", got)
	}
}

func TestCleanLegacyArtifactsRemovesMentionOpcodes(t *testing.T) {
	got := CleanLegacyArtifacts("reach out to ‣ 1b1d872b-594c-811a-ad82-0002ea4fc797 and ‣ p 24d71240-10a3-80ae-8bde-d59bf00682c0 00b8cbcf-c520-4790-999a-9c2940263721,,, see ‣ lm Weekly Walk")
	if got != "reach out to @mention and linked page, see ‣ Weekly Walk" {
		t.Fatalf("got %q", got)
	}
}

func TestCleanLegacyArtifactsCompactsRepeatedLinkedPages(t *testing.T) {
	got := CleanLegacyArtifacts("ask ‣ p 24d71240-10a3-80ae-8bde-d59bf00682c0 00b8cbcf-c520-4790-999a-9c2940263721, ‣ p 24d71240-10a3-80d3-a3b0-c06884bad333 00b8cbcf-c520-4790-999a-9c2940263721, ‣ p 1de71240-10a3-809a-98f9-ea6f4d8702b3 00b8cbcf-c520-4790-999a-9c2940263721 Add notes")
	if got != "ask linked pages Add notes" {
		t.Fatalf("got %q", got)
	}
}

func TestPlainWalksTitleOnlyOnce(t *testing.T) {
	got := Plain(map[string]any{
		"title": []any{map[string]any{
			"plain_text": "Roadmap",
			"text": map[string]any{
				"content": "Roadmap",
			},
		}},
	})
	if got != "Roadmap" {
		t.Fatalf("got %q", got)
	}
}

func TestSlug(t *testing.T) {
	got := Slug("Launch Plan / Q2")
	if got != "launch-plan-q2" {
		t.Fatalf("got %q", got)
	}
}

func TestSlugRemovesEmojiPathText(t *testing.T) {
	got := Slug("研究 🚀 / 計画 ✅")
	if got != "研究-計画" {
		t.Fatalf("got %q", got)
	}
}

func TestSlugRemovesUnsafePathText(t *testing.T) {
	got := Slug(`A/B\C:D*E?F"G<H>I|J`)
	if got != "a-b-c-d-e-f-g-h-i-j" {
		t.Fatalf("got %q", got)
	}
}

func TestSlugRemovesEmojiVariationSelectors(t *testing.T) {
	got := Slug("⚠️ Production Incident Guide")
	if got != "production-incident-guide" {
		t.Fatalf("got %q", got)
	}
}

func TestShortIDKeepsEnoughEntropyForDesktopIDs(t *testing.T) {
	got := ShortID("24f71240-0000-0000-0000-123456789abc")
	if got != "24f71240-56789abc" {
		t.Fatalf("got %q", got)
	}
}
